package routerservice

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerhost"
)

func containsSecret(values []string, secret string) bool {
	for _, v := range values {
		if secret != "" && strings.Contains(v, secret) {
			return true
		}
	}
	return false
}

// loomyHostAPI edits only an explicitly supplied local OpenCode directory.
// Opening the page and saving a Profile never call this operation. Each write
// re-previews the original files and requires the user's matching digest.
func loomyHostAPI(w http.ResponseWriter, r *http.Request, info Info, token, stateDir string) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetReadDeadline(deadline)
	var in struct {
		Action     string   `json:"action"`
		ConfigRoot string   `json:"configRoot"`
		Model      string   `json:"model"`
		Models     []string `json:"models"`
		Digest     string   `json:"digest"`
	}
	if !providerInput(w, r, &in) {
		w.Header().Set("Connection", "close")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	fail := func(status int, code, message string) {
		providerJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	if stateDir == "" {
		fail(503, "host_recovery_unavailable", "Host recovery directory unavailable")
		return
	}
	if token != "" && (strings.Contains(in.ConfigRoot, token) || strings.Contains(in.Model, token) || containsSecret(in.Models, token)) {
		fail(403, "credential_misuse", "Management credentials cannot be used in host settings")
		return
	}
	o := routerhost.NormalizeOptions(routerhost.Options{Host: "loomy", ConfigRoot: in.ConfigRoot, RouterURL: "http://" + info.Address, Model: in.Model, Models: in.Models})
	e := routerhost.Engine{StateDir: stateDir, Adapter: routerhost.LoomyAdapter{}, Checker: routerhost.LoomyChecker(token)}
	if in.Action == "check" {
		if err := e.Checker.Check(ctx, o); err != nil {
			fail(409, "host_check_failed", "Check the Loomy stored Profile, provider, model and Router connection")
			return
		}
		providerJSON(w, 200, map[string]bool{"verified": true})
		return
	}
	var p *routerhost.Plan
	var err error
	switch in.Action {
	case "preview", "apply":
		p, err = e.Preview(o)
	case "restore-preview", "restore":
		p, err = e.PreviewRestore("loomy", in.ConfigRoot)
	default:
		fail(400, "invalid_host_action", "Unsupported Loomy operation")
		return
	}
	if err != nil {
		fail(409, "host_plan_conflict", "Cannot safely preview: check the directory, JSON, managed fields and recovery record")
		return
	}
	if in.Action == "apply" || in.Action == "restore" {
		if in.Digest == "" || subtle.ConstantTimeCompare([]byte(in.Digest), []byte(p.Digest)) != 1 {
			fail(409, "host_plan_changed", "Host configuration changed; preview again")
			return
		}
		if in.Action == "apply" {
			err = e.Apply(ctx, p)
		} else {
			err = e.Restore(p)
		}
		if err != nil {
			fail(409, "host_operation_failed", "Operation stopped; preserve the recovery record and use offline restore if needed")
			return
		}
	}
	providerJSON(w, 200, map[string]any{"action": in.Action, "digest": p.Digest, "plan": p.Summary(), "baseURL": o.RouterURL + "/app/loomy/v1", "requestURL": o.RouterURL + "/app/loomy/v1/chat/completions", "stateDir": stateDir})
}

func cleanAutoRoot(raw string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(raw))
	if root == "." || !filepath.IsAbs(root) || root != strings.TrimSpace(raw) {
		return "", errors.New("host configuration directory must be an absolute clean path")
	}
	return root, nil
}

func resolveAutoHostRoot(host, configRoot, homeDir, _ string) (string, error) {
	if strings.TrimSpace(configRoot) != "" {
		return cleanAutoRoot(configRoot)
	}
	if host == "workbuddy" && strings.TrimSpace(homeDir) != "" {
		h, err := cleanAutoRoot(homeDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".workbuddy"), nil
	}
	return autoHostConfigRoot(host)
}

func autoHostConfigRoot(host string) (string, error) {
	switch host {
	case "workbuddy":
		for _, key := range []string{"WORKBUDDY_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR"} {
			if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
				return cleanAutoRoot(raw)
			}
		}
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("WorkBuddy configuration directory cannot be determined; set WORKBUDDY_CONFIG_DIR")
		}
		return filepath.Join(home, ".workbuddy"), nil
	case "loomy":
		for _, key := range []string{"LOOMY_CONFIG_DIR", "LOOMY_OPENCODE_DIR"} {
			if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
				root, err := cleanAutoRoot(raw)
				if err != nil {
					return "", err
				}
				if _, err := os.Stat(filepath.Join(root, "opencode.json")); err != nil {
					return "", errors.New("Loomy configuration directory does not contain opencode.json; set LOOMY_CONFIG_DIR to the installed Loomy directory")
				}
				return root, nil
			}
		}
		return discoverLoomyConfigRoot()
	default:
		return "", fmt.Errorf("unsupported automatic host: %s", host)
	}
}

func discoverLoomyConfigRoot() (string, error) {
	public := strings.TrimSpace(os.Getenv("PUBLIC"))
	if public == "" && runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			public = filepath.Join(filepath.Dir(home), "Public")
		}
	}
	if public == "" {
		return "", errors.New("Loomy configuration directory was not found; set LOOMY_CONFIG_DIR to its opencode directory")
	}
	entries, err := os.ReadDir(filepath.Join(public, "Loomy"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("Loomy configuration directory was not found; set LOOMY_CONFIG_DIR to its opencode directory")
		}
		return "", errors.New("Loomy configuration directory cannot be inspected; set LOOMY_CONFIG_DIR")
	}
	var found []string
	for _, entry := range entries {
		root := filepath.Join(public, "Loomy", entry.Name(), "opencode")
		if entry.IsDir() {
			if st, statErr := os.Stat(filepath.Join(root, "opencode.json")); statErr == nil && !st.IsDir() {
				found = append(found, root)
			}
		}
	}
	if len(found) == 1 {
		return filepath.Clean(found[0]), nil
	} else if len(found) > 1 {
		return "", errors.New("multiple Loomy configuration directories were found; set LOOMY_CONFIG_DIR to the active opencode directory")
	}
	return "", errors.New("Loomy configuration directory was not found; set LOOMY_CONFIG_DIR to its opencode directory")
}
