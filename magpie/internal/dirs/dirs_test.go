package dirs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestPlatformPaths(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "bin", "api-router")
	join := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	for _, tt := range []struct {
		os                  string
		config, data, cache string
		state, logs         string
	}{
		{"linux", join(".config", "api-router"), join(".local", "share", "api-router"), join(".cache", "api-router"), join(".local", "state", "api-router"), join(".local", "state", "api-router", "logs")},
		{"darwin", join("Library", "Application Support", "api-router"), join("Library", "Application Support", "api-router"), join("Library", "Caches", "api-router"), join("Library", "Application Support", "api-router", "state"), join("Library", "Logs", "api-router")},
		{"windows", join("Roaming", "api-router"), join("Local", "api-router"), join("Local", "api-router", "cache"), join("Local", "api-router", "state"), join("Local", "api-router", "logs")},
	} {
		t.Run(tt.os, func(t *testing.T) {
			l, err := resolve(tt.os, home, exe, func(k string) string {
				return map[string]string{"APPDATA": join("Roaming"), "LOCALAPPDATA": join("Local")}[k]
			})
			if err != nil {
				t.Fatal(err)
			}
			want := Layout{filepath.Dir(exe), tt.config, tt.data, filepath.Join(tt.data, "models"), filepath.Join(tt.data, "runtime"), tt.cache, tt.state, tt.logs}
			if l != want {
				t.Fatalf("paths = %+v; want %+v", l, want)
			}
			if _, err := os.Stat(l.Config); !os.IsNotExist(err) {
				t.Fatalf("resolving paths wrote a directory: %v", err)
			}
		})
	}
}

func TestDirectoryOverrides(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "bin", "api-router")
	for _, platform := range []struct {
		os   string
		keys []string
	}{
		{"linux", []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}},
		{"windows", []string{"APPDATA", "LOCALAPPDATA"}},
	} {
		vars := map[string]string{}
		for _, k := range platform.keys {
			vars[k] = filepath.Join(home, k)
		}
		get := func(k string) string { return vars[k] }
		l, err := resolve(platform.os, home, exe, get)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range platform.keys {
			found := false
			for _, p := range []string{l.Config, l.Data, l.Cache, l.State} {
				found = found || p == filepath.Join(vars[k], "api-router")
			}
			if !found {
				t.Errorf("%s override ignored: %+v", k, l)
			}
			old := vars[k]
			vars[k] = "relative"
			if _, err := resolve(platform.os, home, exe, get); err == nil {
				t.Errorf("relative %s silently used the working directory", k)
			}
			vars[k] = old
		}
	}
}

func TestEnsurePrivateAndOutsideInstall(t *testing.T) {
	home, install := t.TempDir(), t.TempDir()
	l, err := resolve("linux", home, filepath.Join(install, "api-router"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.Config, l.Data, l.Models, l.Runtime, l.Cache, l.State, l.Logs} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
			t.Errorf("directory not private: %s %v", p, fi.Mode())
		}
	}
	files, err := os.ReadDir(install)
	if err != nil || len(files) != 0 {
		t.Fatalf("wrote into install: %v %v", files, err)
	}
}

func TestEnsureRejectsLegacyDirectoryLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	home, legacy := t.TempDir(), t.TempDir()
	l, err := resolve("linux", home, filepath.Join(home, "bin", "api-router"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(l.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacy, l.Config); err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err == nil {
		t.Fatal("accepted a configuration directory symlink")
	}
	fi, err := os.Stat(legacy)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("changed legacy directory permissions: %v %v", fi, err)
	}
	files, err := os.ReadDir(legacy)
	if err != nil || len(files) != 0 {
		t.Fatalf("wrote into legacy directory: %v %v", files, err)
	}
}
