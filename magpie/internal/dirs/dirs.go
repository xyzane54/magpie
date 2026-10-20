// Package dirs owns API-router's paths. It never probes Magpie's files or
// portable markers, and resolving paths does not create any directories.
package dirs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Layout is resolved once at startup and passed to config and runtime owners.
// Install is read-only. Data holds durable downloads; Cache can be discarded.
type Layout struct {
	Install string `json:"install"`
	Config  string `json:"config"`
	Data    string `json:"data"`
	Models  string `json:"models"`
	Runtime string `json:"runtime"`
	Cache   string `json:"cache"`
	State   string `json:"state"`
	Logs    string `json:"logs"`
}

func (l Layout) ConfigFile() string      { return filepath.Join(l.Config, "config.json") }
func (l Layout) CredentialsFile() string { return filepath.Join(l.Config, "credentials.json") }
func (l Layout) ManifestFile() string    { return filepath.Join(l.Models, "manifest.json") }
func (l Layout) LogFile() string         { return filepath.Join(l.Logs, "router.jsonl") }
func (l Layout) ServiceLock() string     { return filepath.Join(l.State, "router.lock") }

// Resolve uses OS conventions, never the current working directory or an
// executable-adjacent data folder. Relative OS directory variables are errors.
func Resolve() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Layout{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return Layout{}, err
	}
	return resolve(runtime.GOOS, home, exe, os.Getenv)
}

func resolve(goos, home, exe string, getenv func(string) string) (Layout, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(exe) {
		return Layout{}, fmt.Errorf("api-router requires absolute home and executable paths")
	}
	base := func(key, fallback string) (string, error) {
		p := getenv(key)
		if p == "" {
			p = fallback
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("%s must be an absolute directory, got %q", key, p)
		}
		return filepath.Join(p, "api-router"), nil
	}
	l := Layout{Install: filepath.Dir(exe)}
	var err error
	switch goos {
	case "linux":
		if l.Config, err = base("XDG_CONFIG_HOME", filepath.Join(home, ".config")); err != nil {
			return Layout{}, err
		}
		if l.Data, err = base("XDG_DATA_HOME", filepath.Join(home, ".local", "share")); err != nil {
			return Layout{}, err
		}
		if l.Cache, err = base("XDG_CACHE_HOME", filepath.Join(home, ".cache")); err != nil {
			return Layout{}, err
		}
		if l.State, err = base("XDG_STATE_HOME", filepath.Join(home, ".local", "state")); err != nil {
			return Layout{}, err
		}
		l.Logs = filepath.Join(l.State, "logs")
	case "darwin":
		l.Config = filepath.Join(home, "Library", "Application Support", "api-router")
		l.Data = l.Config
		l.Cache = filepath.Join(home, "Library", "Caches", "api-router")
		l.State = filepath.Join(l.Data, "state")
		l.Logs = filepath.Join(home, "Library", "Logs", "api-router")
	case "windows":
		if l.Config, err = base("APPDATA", ""); err != nil {
			return Layout{}, err
		}
		if l.Data, err = base("LOCALAPPDATA", ""); err != nil {
			return Layout{}, err
		}
		l.Cache = filepath.Join(l.Data, "cache")
		l.State = filepath.Join(l.Data, "state")
		l.Logs = filepath.Join(l.Data, "logs")
	default:
		return Layout{}, fmt.Errorf("api-router does not support OS %q", goos)
	}
	l.Models = filepath.Join(l.Data, "models")
	l.Runtime = filepath.Join(l.Data, "runtime")
	return l, nil
}

// Ensure creates only API-router's writable directories, with private modes.
// Existing application directories are tightened too; shared OS parents are
// never chmodded. Windows uses the user's inherited directory ACLs.
func (l Layout) Ensure() error {
	for _, p := range []string{l.Config, l.Data, l.Models, l.Runtime, l.Cache, l.State, l.Logs} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("api-router directory must be absolute: %q", p)
		}
	}
	for _, p := range []string{l.Config, l.Data, l.Models, l.Runtime, l.Cache, l.State, l.Logs} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("api-router directory must not be a symlink: %s", p)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}
