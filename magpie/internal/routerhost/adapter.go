package routerhost

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// Adapter is a pure format boundary. Build and Restore receive private copies
// of bytes read by Engine; they must not do I/O, discover credentials or check
// Router. Build returns exactly the declared files, retaining Before/Existed.
// Restore must reject changed managed fields and preserve all other user data.
// Authentication and application identity belong to the caller's Checker;
// neither a placeholder nor a particular Router app ID is required here.
type Adapter interface {
	Contract() AdapterContract
	Build(Options, []FilePlan) ([]FilePlan, error)
	Restore(FilePlan, []byte) ([]byte, error)
}

// AdapterContract is public, non-secret metadata bound into plans and backups.
// Version must change when editing/restoration semantics change. An incompatible
// version blocks recovery rather than guessing how an old format should work.
type AdapterContract struct {
	ID      string
	Version int
	Files   []FileSpec
}

type FileSpec struct {
	Name   string
	Fields []string // printable field names only; never values or credentials
}

var adapterID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func (e Engine) adapter(host string) (Adapter, AdapterContract, error) {
	a := e.Adapter
	if a == nil {
		if err := validateHost(host); err != nil {
			return nil, AdapterContract{}, err
		}
		a = builtinAdapter{host}
	}
	c := a.Contract()
	if c.ID != host || !adapterID.MatchString(c.ID) || c.Version < 1 || len(c.Files) == 0 {
		return nil, c, fmt.Errorf("invalid or mismatched host adapter contract")
	}
	seen := map[string]bool{}
	c.Files = append([]FileSpec(nil), c.Files...)
	for i, f := range c.Files {
		if !validFileName(f.Name) || seen[strings.ToLower(f.Name)] || len(f.Fields) == 0 {
			return nil, c, fmt.Errorf("invalid or duplicate adapter file declaration")
		}
		seen[strings.ToLower(f.Name)] = true
		c.Files[i].Fields = append([]string(nil), f.Fields...)
	}
	// Freeze metadata even if the adapter reuses its own slices.
	return a, c, nil
}

func checkJournalContract(j *journal, c AdapterContract) error {
	if j == nil {
		return nil
	}
	stored := j.Adapter
	if stored == nil {
		if validateHost(j.Host) != nil {
			return fmt.Errorf("recovery record has no adapter version")
		}
		legacy := legacyV1Contract(j.Host)
		stored = &legacy
	}
	if !sameContract(*stored, c) {
		return fmt.Errorf("recovery adapter version or contract conflict; use the matching adapter to restore")
	}
	if len(j.Files) != len(c.Files) {
		return fmt.Errorf("recovery file set does not match adapter contract")
	}
	for i, f := range j.Files {
		if f.Name != c.Files[i].Name {
			return fmt.Errorf("recovery file set does not match adapter contract")
		}
	}
	return nil
}

func validFileName(name string) bool {
	if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") ||
		strings.ContainsAny(name, "\\/:*?\"<>|\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	runes := []rune(stem)
	if len(runes) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.ContainsRune("123456789¹²³", runes[3]) {
		return false
	}
	return true
}

func cloneFile(f FilePlan) FilePlan {
	f.Before = bytes.Clone(f.Before)
	f.After = bytes.Clone(f.After)
	return f
}

func sameContract(a, b AdapterContract) bool { return reflect.DeepEqual(a, b) }

type builtinAdapter struct{ host string }

func (a builtinAdapter) Contract() AdapterContract {
	return legacyV1Contract(a.host)
}

// Frozen metadata for backups made before Adapter existed. A future built-in
// version must return its own contract, leaving this v1 declaration unchanged.
func legacyV1Contract(host string) AdapterContract {
	name := "config.toml"
	fields := []string{"model", "model_provider", "model_providers.api_router.name",
		"model_providers.api_router.base_url", "model_providers.api_router.wire_api",
		"model_providers.api_router.experimental_bearer_token", "model_providers.api_router.requires_openai_auth",
		"model_providers.api_router.supports_websockets"}
	if host == "claude-code" {
		name = "settings.json"
		fields = []string{"env.ANTHROPIC_BASE_URL", "env.ANTHROPIC_AUTH_TOKEN", "env.ANTHROPIC_MODEL"}
	}
	return AdapterContract{ID: host, Version: 1, Files: []FileSpec{{Name: name, Fields: fields}}}
}

func (a builtinAdapter) Build(o Options, files []FilePlan) ([]FilePlan, error) {
	base, err := hostBaseURL(a.host, o.RouterURL)
	if err != nil {
		return nil, err
	}
	if len(files) != 1 {
		return nil, fmt.Errorf("invalid built-in adapter input")
	}
	f := files[0]
	if f.Existed && a.host == "claude-code" && strings.TrimSpace(string(f.Before)) == "" {
		return nil, fmt.Errorf("empty Claude Code settings.json is invalid; repair it before applying")
	}
	if a.host == "codex" {
		f.After, err = buildCodex(f.Before, o, base)
	} else {
		f.After, err = buildClaude(f.Before, o, base)
	}
	return []FilePlan{f}, err
}

func (a builtinAdapter) Restore(f FilePlan, current []byte) ([]byte, error) {
	return RestoreContent(a.host, f, current)
}
