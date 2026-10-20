package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }

type checkerFunc func(context.Context, Options) error

func (f checkerFunc) Check(c context.Context, o Options) error { return f(c, o) }
func setup(t *testing.T, host string) (Engine, Options) {
	t.Helper()
	base := t.TempDir()
	e := Engine{StateDir: filepath.Join(base, "records"), Checker: checkerFunc(func(context.Context, Options) error { return nil })}
	o := Options{Host: host, ConfigRoot: filepath.Join(base, "host"), RouterURL: "http://127.0.0.1:13145", Model: "model-test"}
	return e, o
}
func mustApply(t *testing.T, e Engine, o Options) *Plan {
	t.Helper()
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}
func mustRestore(t *testing.T, e Engine, o Options) {
	t.Helper()
	p, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(p); err != nil {
		t.Fatal(err)
	}
}
func put(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func get(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func name(host string) string {
	if host == "codex" {
		return "config.toml"
	}
	return "settings.json"
}

func TestRoundtripBackupAndUnrelatedChanges(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		for _, existing := range []bool{false, true} {
			t.Run(host+map[bool]string{false: "/new", true: "/existing"}[existing], func(t *testing.T) {
				e, o := setup(t, host)
				path := hostPath(o.ConfigRoot, name(host))
				var original []byte
				if existing {
					original = []byte("# retained\nmodel = \"previous\" # chosen\n[unrelated]\nvalue = 3\n")
					if host == "claude-code" {
						original = []byte("{\n // retained\n \"env\": {\"UNRELATED\": \"old-secret-fixture\"},\n \"model\": \"previous\", \"unknown\": 3\n}\n")
					}
					put(t, path, original)
				}
				p, err := e.Preview(o)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(e.StateDir); !os.IsNotExist(err) {
					t.Fatal("preview created records")
				}
				if !existing {
					if _, err = os.Stat(o.ConfigRoot); !os.IsNotExist(err) {
						t.Fatal("preview created host directory")
					}
				}
				summary, _ := json.Marshal(p.Summary())
				for _, secret := range []string{PlaceholderAPIKey, "old-secret-fixture"} {
					if bytes.Contains(summary, []byte(secret)) {
						t.Fatal("secret in preview")
					}
				}
				if err = e.Apply(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				journalBefore := get(t, e.recordPath(host, o.ConfigRoot))
				applied := get(t, path)
				mustApply(t, e, o)
				if !bytes.Equal(applied, get(t, path)) || !bytes.Equal(journalBefore, get(t, e.recordPath(host, o.ConfigRoot))) {
					t.Fatal("repeat apply changed bytes or first backup")
				}
				if runtime.GOOS != "windows" {
					for _, path := range []string{path, e.recordPath(host, o.ConfigRoot)} {
						st, _ := os.Stat(path)
						if st.Mode().Perm() != 0o600 {
							t.Fatal("secret file not private")
						}
					}
					st, _ := os.Stat(e.StateDir)
					if st.Mode().Perm() != 0o700 {
						t.Fatal("state not private")
					}
				}
				// User changes an unrelated value after connection; recovery preserves it.
				var changed []byte
				if host == "codex" {
					changed = append(applied, []byte("\n[user_after]\nkeep = true\n")...)
				} else {
					changed, err = edit.PatchJSON(applied, []edit.KV{{Path: "user_after", Value: true}}, nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				put(t, path, changed)
				mustRestore(t, e, o)
				restored := get(t, path)
				if !bytes.Contains(restored, []byte("user_after")) || bytes.Contains(restored, []byte(PlaceholderAPIKey)) {
					t.Fatal("restore lost user content or kept Router placeholder")
				}
				if existing && !bytes.Contains(restored, []byte("retained")) {
					t.Fatal("comment lost")
				}
				mustRestore(t, e, o)
				if !bytes.Equal(restored, get(t, path)) {
					t.Fatal("repeat restore changed file")
				}
			})
		}
	}
}
func TestExactRestoreAndNewFileRemoval(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		for _, existing := range []bool{false, true} {
			t.Run(host+map[bool]string{false: "/new", true: "/old"}[existing], func(t *testing.T) {
				e, o := setup(t, host)
				path := hostPath(o.ConfigRoot, name(host))
				orig := []byte("# original\nmodel='old'\n")
				if host == "claude-code" {
					orig = []byte("{\"unrelated\":123}\n")
				}
				if existing {
					put(t, path, orig)
				}
				mustApply(t, e, o)
				mustRestore(t, e, o)
				if existing {
					if !bytes.Equal(get(t, path), orig) {
						t.Fatal("original bytes not restored")
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("tool-created untouched file not removed")
				}
				mustRestore(t, e, o)
			})
		}
	}
}
func TestConflictsAndStalePreview(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			e, o := setup(t, host)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			path := hostPath(o.ConfigRoot, name(host))
			newData := []byte("model='new-user-choice'\n")
			if host == "claude-code" {
				newData = []byte("{\"user\":true}")
			}
			put(t, path, newData)
			if err = e.Apply(context.Background(), p); err == nil {
				t.Fatal("stale plan accepted")
			}
			if !bytes.Equal(get(t, path), newData) {
				t.Fatal("stale plan overwrote user bytes")
			}
			mustApply(t, e, o)
			data := get(t, path)
			data = bytes.Replace(data, []byte(o.Model), []byte("changed-by-user"), 1)
			put(t, path, data)
			if _, err = e.PreviewRestore(host, o.ConfigRoot); err == nil {
				t.Fatal("same-field edit not a conflict")
			}
			if !bytes.Equal(get(t, path), data) {
				t.Fatal("conflict wrote config")
			}
		})
	}
}
func TestCheckFailureAndInFlightChange(t *testing.T) {
	e, o := setup(t, "codex")
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.Checker = checkerFunc(func(context.Context, Options) error { return errors.New("fixture check failure") })
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("failed check applied")
	}
	if _, err = os.Stat(e.StateDir); !os.IsNotExist(err) {
		t.Fatal("failed check created journal")
	}
	e.Checker = checkerFunc(func(context.Context, Options) error {
		put(t, hostPath(o.ConfigRoot, "config.toml"), []byte("# concurrent user\n"))
		return nil
	})
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("change during check overwritten")
	}
	if string(get(t, hostPath(o.ConfigRoot, "config.toml"))) != "# concurrent user\n" {
		t.Fatal("concurrent bytes lost")
	}
}
func TestDamagedMissingRecordsLocksAndWriteFailures(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			e, o := setup(t, host)
			path := hostPath(o.ConfigRoot, name(host))
			bad := []byte("[broken")
			if host == "claude-code" {
				bad = []byte("{broken")
			}
			put(t, path, bad)
			if _, err := e.Preview(o); err == nil {
				t.Fatal("damage accepted")
			}
			if !bytes.Equal(get(t, path), bad) {
				t.Fatal("damage overwritten")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			mustApply(t, e, o)
			record := e.recordPath(host, o.ConfigRoot)
			recordBytes := get(t, record)
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			if _, err := e.PreviewRestore(host, o.ConfigRoot); err == nil {
				t.Fatal("missing backup accepted")
			}
			put(t, record, []byte("{}"))
			if _, err := e.PreviewRestore(host, o.ConfigRoot); err == nil {
				t.Fatal("damaged record accepted")
			}
			put(t, record, recordBytes)
			p, err := e.PreviewRestore(host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(o.ConfigRoot, ".api-router-host.lock"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err = e.Restore(p); err == nil {
				t.Fatal("busy lock accepted")
			}
			if err = os.Remove(filepath.Join(o.ConfigRoot, ".api-router-host.lock")); err != nil {
				t.Fatal(err)
			}
			e.write = func(string, []byte, uint32) error { return fs.ErrPermission }
			if err = e.Restore(p); err == nil {
				t.Fatal("record write failure ignored")
			}
			if !bytes.Contains(get(t, path), []byte(PlaceholderAPIKey)) {
				t.Fatal("config changed before restore journal saved")
			}
		})
	}
}
func TestPermissionsLinksAndBackupFailure(t *testing.T) {
	e, o := setup(t, "codex")
	path := hostPath(o.ConfigRoot, "config.toml")
	put(t, path, []byte("# user\n"))
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Preview(o); err == nil {
			t.Fatal("unreadable accepted")
		}
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		p, err := e.Preview(o)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Apply(context.Background(), p); err == nil {
			t.Fatal("read-only overwritten")
		}
		if string(get(t, path)) != "# user\n" {
			t.Fatal("read-only content lost")
		}
		if err = os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.write = func(string, []byte, uint32) error { return errors.New("disk full") }
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("backup failure ignored")
	}
	if string(get(t, path)) != "# user\n" {
		t.Fatal("host written before backup")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(t.TempDir(), "target")
		put(t, target, []byte("# target\n"))
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err = e.Preview(o); err == nil {
			t.Fatal("symlink followed")
		}
	}
}
func TestPartialWriteFailureAndOfflineRecovery(t *testing.T) {
	e, o := setup(t, "codex")
	e.Adapter = twoFileAdapter{builtinAdapter{o.Host}}
	put(t, hostPath(o.ConfigRoot, "config.toml"), []byte("# original user config\n"))
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture declares two files through the public adapter contract.
	calls := 0
	e.write = func(path string, b []byte, mode uint32) error {
		if filepath.Base(path) == "second.toml" {
			return errors.New("second write failed")
		}
		if filepath.Base(path) == "config.toml" {
			calls++
			if calls > 1 {
				return errors.New("rollback write failed")
			}
		}
		return steady.WriteFile(path, b, fs.FileMode(mode))
	}
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("partial failure ignored")
	}
	j, _, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil || j == nil || j.Status != "prepared" {
		t.Fatalf("missing pending recovery: %v", err)
	}
	// No Checker at all: recovery must work with Router stopped.
	e.write = nil
	e.Checker = nil
	mustRestore(t, e, o)
	if string(get(t, hostPath(o.ConfigRoot, "config.toml"))) != "# original user config\n" {
		t.Fatal("original file not recovered")
	}
	if _, err := os.Stat(hostPath(o.ConfigRoot, "second.toml")); !os.IsNotExist(err) {
		t.Fatal("partial recovery left created config")
	}
}
func TestPreparedJournalCrashPoints(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-write"}[applied], func(t *testing.T) {
			e, o := setup(t, "claude-code")
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(e.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			j := journal{Version: 1, Host: o.Host, Root: o.ConfigRoot, Status: "prepared", Files: p.files}
			if err = e.saveJournal(&j); err != nil {
				t.Fatal(err)
			}
			if applied {
				put(t, hostPath(o.ConfigRoot, name(o.Host)), p.files[0].After)
			}
			e.Checker = nil
			mustRestore(t, e, o)
			mustRestore(t, e, o)
		})
	}
}
func TestUnsafeURLRejected(t *testing.T) {
	e, o := setup(t, "codex")
	for _, u := range []string{"http://localhost:12", "http://127.0.0.1:12/app/codex/v1", "http://user:secret@127.0.0.1:12", "http://127.0.0.1:12?token=secret", "https://example.com"} {
		o.RouterURL = u
		if _, err := e.Preview(o); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}
func TestHTTPCheckerExactPathsAndSecrets(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			_, o := setup(t, host)
			var paths []string
			var mu sync.Mutex
			mode := "ok"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				currentMode := mode
				mu.Unlock()
				if r.URL.Path == "/healthz" {
					if r.Header.Get("Authorization") != "" {
						t.Error("health received credential")
					}
					_, _ = w.Write([]byte(`{"service":"api-router","status":"ok"}`))
					return
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("unexpected business authorization")
				}
				if currentMode == "error" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte("host-checker-secret-never-print"))
					return
				}
				if host == "codex" {
					_, _ = w.Write([]byte(`{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}]}`))
				} else {
					_, _ = w.Write([]byte(`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}]}`))
				}
			}))
			defer server.Close()
			o.RouterURL = server.URL
			c := HTTPChecker{ValidateApp: func(context.Context, Options) error { return nil }}
			if err := c.Check(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			want := "/app/codex/v1/responses"
			if host == "claude-code" {
				want = "/app/claude-code/v1/messages"
			}
			mu.Lock()
			gotPaths := strings.Join(paths, ",")
			mu.Unlock()
			if gotPaths != "/healthz,"+want {
				t.Fatalf("wrong paths: %v", gotPaths)
			}
			// A health response can never masquerade as complete host readiness.
			mu.Lock()
			paths = nil
			mu.Unlock()
			if err := ProductionChecker().Check(context.Background(), o); err == nil || !strings.Contains(err.Error(), "application validation unavailable") {
				t.Fatalf("production did not fail closed: %v", err)
			}
			mu.Lock()
			gotPaths = strings.Join(paths, ",")
			mu.Unlock()
			if gotPaths != "/healthz" {
				t.Fatal("missing app validation still invoked model")
			}
			mu.Lock()
			mode = "error"
			mu.Unlock()
			if err := c.Check(context.Background(), o); err == nil || strings.Contains(err.Error(), "host-checker-secret-never-print") {
				t.Fatal("HTTP error leaked body or succeeded")
			}
			// Callback errors may contain secrets; caller-facing error must not echo them.
			c.ValidateApp = func(context.Context, Options) error { return errors.New("host-checker-secret-never-print") }
			if err := c.Check(context.Background(), o); err == nil || strings.Contains(err.Error(), "host-checker-secret-never-print") {
				t.Fatal("app validator leaked secret")
			}
		})
	}
}

func TestRestoreResumesOriginalModeAfterCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode fixture")
	}
	e, o := setup(t, "codex")
	path := hostPath(o.ConfigRoot, "config.toml")
	original := []byte("# original mode\n")
	put(t, path, original)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	mustApply(t, e, o)
	j, _, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	j.Status = "restoring"
	if err = e.saveJournal(j); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash after original bytes were replaced but before chmod.
	put(t, path, original)
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	mustRestore(t, e, o)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("original mode was not recovered: %v %v", st, err)
	}
}

func TestProbeRejectsEmptyAndIncompleteEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`{"object":"response","status":"completed","output":[null]}`,
		`{"object":"response","status":"completed","output":[{"type":"message"}]}`,
		`{"object":"response","status":"incomplete","output":[]}`,
		`{"type":"message","content":[null]}`,
		`{"type":"message","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`,
		`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":""}]}`,
	} {
		for _, host := range []string{"codex", "claude-code"} {
			if validProbe(host, []byte(raw)) {
				t.Fatalf("accepted incomplete %s result", host)
			}
		}
	}
}

func TestPartialRestoreRetriesWithoutLosingUnrelatedEdits(t *testing.T) {
	e, o := setup(t, "codex")
	e.Adapter = twoFileAdapter{builtinAdapter{o.Host}}
	path := hostPath(o.ConfigRoot, "config.toml")
	original := []byte("model='original'\n# keep\n")
	put(t, path, original)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	secondPath := hostPath(o.ConfigRoot, "second.toml")
	put(t, secondPath, append(get(t, secondPath), []byte("\n[user]\nkeep=true\n")...))
	restore, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	e.write = func(path string, b []byte, mode uint32) error {
		if filepath.Base(path) == "second.toml" {
			return fs.ErrPermission
		}
		return steady.WriteFile(path, b, fs.FileMode(mode))
	}
	if err = e.Restore(restore); err == nil {
		t.Fatal("partial restore succeeded")
	}
	if !bytes.Equal(get(t, path), original) {
		t.Fatal("first restore step not completed")
	}
	e.write = nil
	mustRestore(t, e, o)
	mustRestore(t, e, o)
	content := get(t, secondPath)
	if !bytes.Contains(content, []byte("keep=true")) || bytes.Contains(content, []byte(PlaceholderAPIKey)) {
		t.Fatal("retry removed later user content or kept Router credential")
	}
}
func TestCheckerErrorsNeverExposeCredentials(t *testing.T) {
	e, o := setup(t, "codex")
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.Checker = checkerFunc(func(context.Context, Options) error { return errors.New("host-checker-secret-never-print") })
	if err = e.Apply(context.Background(), p); err == nil || strings.Contains(err.Error(), "host-checker-secret-never-print") {
		t.Fatal("checker error leaked credential")
	}
}

func TestChangedRecoveryRecordInvalidatesPreview(t *testing.T) {
	e, o := setup(t, "codex")
	mustApply(t, e, o)
	p, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	j.Status = "restoring"
	if err = e.saveJournal(j); err != nil {
		t.Fatal(err)
	}
	if err = e.Restore(p); err == nil || !strings.Contains(err.Error(), "record changed") {
		t.Fatal("stale recovery journal accepted", err)
	}
	mustRestore(t, e, o)
}
func TestFailedApplyRepairsModeWhenBytesAlreadyOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode fixture")
	}
	e, o := setup(t, "codex")
	path := hostPath(o.ConfigRoot, "config.toml")
	original := []byte("# original file\n")
	put(t, path, original)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.write = func(at string, b []byte, mode uint32) error {
		if at == path {
			if err := steady.WriteFile(at, original, 0o600); err != nil {
				return err
			}
			return errors.New("interrupted writer after bytes recovered")
		}
		return steady.WriteFile(at, b, fs.FileMode(mode))
	}
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("failed writer succeeded")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatal("failed apply left original bytes with changed permissions")
	}
}
