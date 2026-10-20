package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/steady"
)

type twoFileAdapter struct{ builtinAdapter }

func (a twoFileAdapter) Contract() AdapterContract {
	c := a.builtinAdapter.Contract()
	c.Files = append(c.Files, FileSpec{Name: "second.toml", Fields: c.Files[0].Fields})
	return c
}
func (a twoFileAdapter) Build(o Options, input []FilePlan) ([]FilePlan, error) {
	var files []FilePlan
	for _, f := range input {
		built, err := a.builtinAdapter.Build(o, []FilePlan{f})
		if err != nil {
			return nil, err
		}
		files = append(files, built...)
	}
	return files, nil
}

func TestReadFailureBlocksPreviewApplyAndRestore(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			e, o := setup(t, host)
			path := hostPath(o.ConfigRoot, name(host))
			original := []byte("# private fixture\n")
			if host == "claude-code" {
				original = []byte("{\"unrelated\":true}\n")
			}
			put(t, path, original)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			e.read = func(at string) ([]byte, error) {
				if at == path {
					return nil, fs.ErrPermission
				}
				return os.ReadFile(at)
			}
			if _, err = e.Preview(o); err == nil {
				t.Fatal("read failure became an empty preview")
			}
			if err = e.Apply(context.Background(), p); err == nil {
				t.Fatal("read failure allowed apply")
			}
			if !bytes.Equal(get(t, path), original) {
				t.Fatal("unreadable source changed")
			}
			if _, err = os.Stat(e.StateDir); !os.IsNotExist(err) {
				t.Fatal("read failure created backup state")
			}
			e.read = nil
			mustApply(t, e, o)
			r, err := e.PreviewRestore(host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			applied := get(t, path)
			e.read = func(string) ([]byte, error) { return nil, fs.ErrPermission }
			if _, err = e.PreviewRestore(host, o.ConfigRoot); err == nil {
				t.Fatal("unreadable record accepted")
			}
			if err = e.Restore(r); err == nil {
				t.Fatal("unreadable record allowed restore")
			}
			if !bytes.Equal(get(t, path), applied) {
				t.Fatal("failed restore changed host")
			}
		})
	}
}

func TestBackupTimeSourceChangeIsPreserved(t *testing.T) {
	e, o := setup(t, "codex")
	path := hostPath(o.ConfigRoot, "config.toml")
	put(t, path, []byte("model='original'\n"))
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	user := []byte("model='concurrent-user'\n")
	e.write = func(at string, b []byte, mode uint32) error {
		if err := steady.WriteFile(at, b, fs.FileMode(mode)); err != nil {
			return err
		}
		if filepath.Dir(at) == e.StateDir {
			put(t, path, user)
		}
		return nil
	}
	if err = e.Apply(context.Background(), p); err == nil {
		t.Fatal("source conflict accepted")
	}
	if !bytes.Equal(get(t, path), user) {
		t.Fatal("concurrent user configuration overwritten")
	}
	j, _, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil || j == nil || j.Status != "prepared" {
		t.Fatalf("pending backup lost: %v", err)
	}
}

func TestInvalidRecoveryVersionAndChecksumBlockWrites(t *testing.T) {
	for _, kind := range []string{"version", "checksum", "adapter-version"} {
		t.Run(kind, func(t *testing.T) {
			e, o := setup(t, "claude-code")
			mustApply(t, e, o)
			r, err := e.PreviewRestore(o.Host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			j, _, err := e.readJournal(o.Host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "version":
				j.Version++
				j.Checksum = j.checksum()
			case "checksum":
				j.Checksum = strings.Repeat("0", 64)
			case "adapter-version":
				j.Adapter.Version++
				j.Checksum = j.checksum()
			}
			b, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			record := e.recordPath(o.Host, o.ConfigRoot)
			put(t, record, b)
			path := hostPath(o.ConfigRoot, "settings.json")
			before := get(t, path)
			if _, err = e.Preview(o); err == nil {
				t.Fatal("incompatible record allowed apply preview")
			}
			if _, err = e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
				t.Fatal("incompatible record allowed restore preview")
			}
			if err = e.Restore(r); err == nil {
				t.Fatal("stale restore accepted incompatible record")
			}
			if !bytes.Equal(before, get(t, path)) || !bytes.Equal(b, get(t, record)) {
				t.Fatal("rejected record changed files")
			}
		})
	}
}

func TestLegacyBuiltInBackupRemainsRecoverable(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			e, o := setup(t, host)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			put(t, hostPath(o.ConfigRoot, name(host)), p.files[0].After)
			if err = os.MkdirAll(e.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			j := journal{Version: 1, Host: o.Host, Root: o.ConfigRoot, Status: "applied", Files: p.files}
			if err = e.saveJournal(&j); err != nil {
				t.Fatal(err)
			}
			e.Checker = nil
			mustRestore(t, e, o)
			mustRestore(t, e, o)
		})
	}
}

func TestAdapterFileNamesArePortableAndDistinct(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside", `C:stream`, `a\b`, "config.toml.", "config.toml ", "CON", "nul.json", "COM1.json", "COM¹.json", "LPT9", "bad\nname", "bad*name", ".operation.lock"} {
		if validFileName(name) {
			t.Errorf("unsafe or aliased filename accepted: %q", name)
		}
	}
	for _, name := range []string{"config.toml", "provider.jsonc", "models-v1.json"} {
		if !validFileName(name) {
			t.Errorf("ordinary filename rejected: %q", name)
		}
	}
}
