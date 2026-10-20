package routerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const fixture = `{"schemaVersion":1,"instanceId":"fixture-instance","listen":{"address":"127.0.0.1:13145"},"apps":{},"providers":{},"redaction":{"enabled":false},"runtime":{},"future":{"nested":[1,"keep me"]}}`

func setup(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	l := dirs.Layout{
		Install: filepath.Join(root, "install"), Config: filepath.Join(root, "config"),
		Data: filepath.Join(root, "data"), Models: filepath.Join(root, "data", "models"),
		Runtime: filepath.Join(root, "data", "runtime"), Cache: filepath.Join(root, "cache"),
		State: filepath.Join(root, "state"), Logs: filepath.Join(root, "state", "logs"),
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	return New(l), l.ConfigFile()
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func unchanged(t *testing.T, path, content string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != content {
		t.Fatalf("file was changed: %s = %q (%v)", path, b, err)
	}
}

func TestInitializeOnlyMissing(t *testing.T) {
	s, p := setup(t)
	if _, err := s.Load(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing config: %v", err)
	}
	d, err := s.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if string(d["redaction"]) != `{"enabled":false}` || !bytes.Contains(d["listen"], []byte("127.0.0.1:")) {
		t.Fatalf("unsafe defaults: %s", d)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Initialize()
	if err != nil || !bytes.Equal(again["instanceId"], d["instanceId"]) {
		t.Fatalf("reinitialized identity: %s %v", again, err)
	}
	unchanged(t, p, string(b))
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v", fi.Mode())
	}
}

func TestDamagedConfigNeverOverwritten(t *testing.T) {
	for _, content := range []string{
		"", " \n", "{broken", "null", "[]", "{}",
		string(bytes.ReplaceAll([]byte(fixture), []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`))),
		string(bytes.ReplaceAll([]byte(fixture), []byte(`"apps":{}`), []byte(`"apps":null`))),
	} {
		t.Run(content, func(t *testing.T) {
			s, p := setup(t)
			put(t, p, content)
			put(t, p+".bak", fixture)
			if _, err := s.Load(); err == nil {
				t.Fatal("damaged config loaded as defaults or backup")
			}
			if _, err := s.Initialize(); err == nil {
				t.Fatal("damaged config initialized as empty")
			}
			called := false
			if err := s.Update(func(Document) error { called = true; return nil }); err == nil || called {
				t.Fatalf("damaged config edited: callback=%v error=%v", called, err)
			}
			unchanged(t, p, content)
			unchanged(t, p+".bak", fixture)
		})
	}
}

func TestUnreadableConfigNeverOverwritten(t *testing.T) {
	s, p := setup(t)
	// A directory is a deterministic read error on every OS, even as root.
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(); err == nil {
		t.Fatal("unreadable config replaced")
	}
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Fatalf("unreadable config path changed: %v %v", fi, err)
	}
	if runtime.GOOS == "windows" {
		return // chmod does not implement Windows ACLs
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	put(t, p, fixture)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("current user bypasses mode permissions; directory read-error case passed")
	}
	if _, err := s.Initialize(); err == nil {
		t.Fatal("permission error treated as missing")
	}
	if err := s.Update(func(Document) error { t.Fatal("edited unreadable config"); return nil }); err == nil {
		t.Fatal("permission error overwritten")
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	unchanged(t, p, fixture)
}

func TestUpdatePreservesUnknownAndBacksUp(t *testing.T) {
	s, p := setup(t)
	put(t, p, fixture)
	if err := s.Update(func(d Document) error {
		d["redaction"] = json.RawMessage(`{"enabled":true}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unchanged(t, p+".bak", fixture)
	d, err := s.Load()
	if err != nil || !bytes.Contains(d["future"], []byte("keep me")) || !bytes.Contains(d["redaction"], []byte("true")) {
		t.Fatalf("edit lost data: %s %v", d, err)
	}
	for _, path := range []string{p, p + ".bak"} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Fatalf("private file mode: %s %v", path, fi.Mode())
		}
	}
}

func TestFailedUpdateKeepsOriginal(t *testing.T) {
	for _, failure := range []string{"edit", "schema", "backup", "flush"} {
		t.Run(failure, func(t *testing.T) {
			s, p := setup(t)
			put(t, p, fixture)
			if failure == "backup" {
				if err := os.Mkdir(p+".bak", 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "flush" {
				old := steady.Sync
				steady.Sync = func(f *os.File) error {
					if strings.HasPrefix(filepath.Base(f.Name()), ".config.json.bak.") {
						return old(f)
					}
					return errors.New("injected flush failure")
				}
				t.Cleanup(func() { steady.Sync = old })
			}
			err := s.Update(func(d Document) error {
				d["redaction"] = json.RawMessage(`{"enabled":true}`)
				if failure == "edit" {
					return errors.New("edit failed")
				}
				if failure == "schema" {
					d["schemaVersion"] = json.RawMessage(`99`)
				}
				return nil
			})
			if err == nil {
				t.Fatal("failed update reported success")
			}
			unchanged(t, p, fixture)
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".config.json.*"))
			if len(files) != 0 {
				t.Fatalf("temporary files left: %v", files)
			}
		})
	}
}

func TestWriterLockAndDanglingLink(t *testing.T) {
	s, p := setup(t)
	if err := os.Mkdir(p+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(); err == nil {
		t.Fatal("ignored another writer's lock")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("initialized while locked: %v", err)
	}
	if err := os.Remove(p + ".lock"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // Windows symlink privileges vary; lock coverage still applies.
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(); err == nil {
		t.Fatal("replaced dangling config symlink")
	}
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: %v %v", fi, err)
	}
}
