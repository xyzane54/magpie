// Package routerhost previews, journals and reverses explicit host configuration
// edits. It never discovers accounts, imports credentials or starts a service.
package routerhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/steady"
)

const PlaceholderAPIKey = "api-router-local-placeholder"

type Options struct {
	Host, ConfigRoot, RouterURL, Model string
	Models                             []string
}

func (o Options) EffectiveModels() []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range o.Models {
		m := strings.TrimSpace(raw)
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		if m := strings.TrimSpace(o.Model); m != "" {
			out = []string{m}
		}
	}
	return out
}

func (o Options) PrimaryModel() string {
	if models := o.EffectiveModels(); len(models) > 0 {
		return models[0]
	}
	return o.Model
}

func (o Options) effectiveModelSet() map[string]bool {
	set := map[string]bool{}
	for _, m := range o.EffectiveModels() {
		set[m] = true
	}
	return set
}

func NormalizeOptions(o Options) Options {
	models := o.EffectiveModels()
	if len(o.Models) > 0 {
		o.Models = models
	}
	if len(models) > 0 {
		o.Model = models[0]
	}
	return o
}

type FilePlan struct {
	Name          string
	Before, After []byte
	Existed       bool
}

type Checker interface {
	Check(context.Context, Options) error
}

type Engine struct {
	StateDir string
	Checker  Checker
	Adapter  Adapter
	write    func(string, []byte, uint32) error
	read     func(string) ([]byte, error)
}

type snapshot struct {
	FilePlan
	Mode uint32
}

type Plan struct {
	Digest     string
	Action     string
	Host       string
	options    Options
	files      []snapshot
	recordHash string
	stateDir   string
	contract   AdapterContract
	builtin    bool
}

func (p *Plan) Summary() any {
	type file struct {
		Path   string   `json:"path"`
		Change string   `json:"change"`
		Fields []string `json:"fields"`
	}
	files := make([]file, 0, len(p.files))
	for _, f := range p.files {
		change := "modify"
		if equal(f.Before, f.After) {
			change = "unchanged"
		} else if !f.Existed {
			change = "create"
		}
		if p.Action == "restore" && f.After == nil && f.Existed {
			change = "remove tool-created file"
		}
		var fields []string
		for _, spec := range p.contract.Files {
			if spec.Name == f.Name {
				fields = append(fields, spec.Fields...)
			}
		}
		files = append(files, file{hostPath(p.options.ConfigRoot, f.Name), change, fields})
	}
	endpoint, requestURL := p.options.RouterURL, ""
	if endpoint != "" && (p.builtin || p.Host == "loomy") {
		endpoint = strings.TrimRight(endpoint, "/") + "/app/" + p.Host + "/v1"
		requestURL = endpoint + "/responses"
		if p.Host == "claude-code" {
			requestURL = endpoint + "/messages"
		} else if p.Host == "loomy" {
			requestURL = endpoint + "/chat/completions"
		}
	}
	configBase := endpoint
	if p.builtin && p.Host == "claude-code" {
		configBase = strings.TrimSuffix(endpoint, "/v1")
	}
	credentialSource := "adapter-defined; credentials omitted"
	if p.builtin {
		credentialSource = "non-secret placeholder"
	} else if p.Host == "loomy" {
		credentialSource = "stored Router provider key; host uses a non-secret placeholder; useSessionAuth:false"
	}
	return struct {
		Action, Host, Digest                                     string
		Files                                                    []file
		RouterURL, RequestURL, Model, CredentialSource, Recovery string
		Warning                                                  string `json:",omitempty"`
	}{p.Action, p.Host, p.Digest, files, configBase, requestURL, p.options.PrimaryModel(), credentialSource,
		"api-router host restore-preview / restore; available while Router is stopped", p.warning()}
}
func (p *Plan) warning() string {
	if p.Action == "restore" {
		return "Restoring bypasses Router: subsequent requests no longer use its routing or redaction."
	}
	return "Liveness alone does not establish model readiness. Apply requires all three connection checks."
}
func sum(b []byte) string               { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func equal(a, b []byte) bool            { return bytes.Equal(a, b) }
func hostPath(root, name string) string { return filepath.Join(root, name) }

func validateRoot(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("use a clean absolute directory path")
	}
	for at := p; ; at = filepath.Dir(at) {
		st, err := os.Lstat(at)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot inspect directory %s", at)
		}
		if err == nil && !st.IsDir() {
			return fmt.Errorf("directory must not contain symlinks or non-directories: %s", at)
		}
		if filepath.Dir(at) == at {
			break
		}
	}
	return nil
}
func validateHost(host string) error {
	if host != "codex" && host != "claude-code" {
		return fmt.Errorf("host must be codex or claude-code")
	}
	return nil
}
func validateOptions(o Options) error {
	if err := validateRoot(o.ConfigRoot); err != nil {
		return err
	}
	u, err := url.Parse(o.RouterURL)
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || u.Host == "" || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return fmt.Errorf("router URL must be an HTTP literal loopback origin, without credentials, path, query or fragment")
	}
	for _, raw := range o.Models {
		if strings.TrimSpace(raw) == "" || len(raw) > 256 || strings.ContainsAny(raw, "\r\n\x00") {
			return fmt.Errorf("a nonempty model ID is required")
		}
	}
	models := o.EffectiveModels()
	if len(models) == 0 || (len(o.Models) == 0 && (len(o.Model) > 256 || strings.ContainsAny(o.Model, "\r\n\x00"))) {
		return fmt.Errorf("a nonempty model ID is required")
	}
	for _, m := range models {
		if len(m) > 256 || strings.ContainsAny(m, "\r\n\x00") {
			return fmt.Errorf("a nonempty model ID is required")
		}
	}
	return nil
}
func (e Engine) validateState(root string) error {
	if err := validateRoot(e.StateDir); err != nil {
		return err
	}
	a, b := filepath.Clean(e.StateDir), filepath.Clean(root)
	if runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
		return fmt.Errorf("state and host configuration directories must be separate")
	}
	return nil
}
func (e Engine) recordPath(host, root string) string {
	return filepath.Join(e.StateDir, host+"-"+sum([]byte(root))+".json")
}
func (e Engine) writePrivate(path string, b []byte, mode uint32) error {
	if e.write != nil {
		return e.write(path, b, mode)
	}
	return steady.WriteFile(path, b, fs.FileMode(mode))
}
func planDigest(p *Plan) string {
	b, _ := json.Marshal(struct {
		Action, Host, State, Record string
		Options                     Options
		Files                       []snapshot
		Contract                    AdapterContract
		Builtin                     bool
	}{p.Action, p.Host, p.stateDir, p.recordHash, p.options, p.files, p.contract, p.builtin})
	return sum(b)
}
func (e Engine) readSnapshot(root string, f FilePlan) (snapshot, error) {
	if !validFileName(f.Name) {
		return snapshot{}, fmt.Errorf("unsupported host file name")
	}
	path := hostPath(root, f.Name)
	s := snapshot{FilePlan: FilePlan{Name: f.Name, After: bytes.Clone(f.After)}, Mode: 0o600}
	st, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o444 == 0 {
		return s, fmt.Errorf("cannot inspect regular readable host configuration: %s", path)
	}
	read := e.read
	if read == nil {
		read = steady.ReadThere
	}
	data, err := read(path)
	if err != nil {
		return s, fmt.Errorf("cannot read host configuration: %s", path)
	}
	s.Before, s.Existed, s.Mode = data, true, uint32(st.Mode().Perm())
	return s, nil
}
