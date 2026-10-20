package routerhost

import (
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/steady"
)

func TestApplyVerifiesBackupBeforeChangingHost(t *testing.T) {
	e, o := setup(t, "claude-code")
	path := hostPath(o.ConfigRoot, "settings.json")
	original := []byte("{\"env\":{\"UNRELATED\":\"private-fixture\"}}\n")
	put(t, path, original)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.write = func(at string, b []byte, mode uint32) error {
		if filepath.Dir(at) == e.StateDir {
			b = b[:len(b)/2]
		}
		return steady.WriteFile(at, b, fs.FileMode(mode))
	}
	if err = e.Apply(context.Background(), p); err == nil {
		t.Error("apply accepted an incomplete backup reported as successful")
	}
	if !bytes.Equal(get(t, path), original) {
		t.Error("host changed without a verified backup")
	}
}

func TestApplyDoesNotOverwriteConcurrentRecoveryRecord(t *testing.T) {
	e, o := setup(t, "codex")
	path := hostPath(o.ConfigRoot, "config.toml")
	original := []byte("model='original'\n")
	put(t, path, original)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	record := e.recordPath(o.Host, o.ConfigRoot)
	other := []byte("{\"concurrent-record\":true}\n")
	e.write = func(at string, b []byte, mode uint32) error {
		if err := steady.WriteFile(at, b, fs.FileMode(mode)); err != nil {
			return err
		}
		if at == path && bytes.Equal(b, p.files[0].After) {
			put(t, record, other)
		}
		return nil
	}
	if err = e.Apply(context.Background(), p); err == nil || !strings.Contains(err.Error(), "record") {
		t.Error("concurrent recovery record was not reported as a conflict")
	}
	if !bytes.Equal(get(t, record), other) {
		t.Error("concurrent recovery record was overwritten")
	}
	if !bytes.Equal(get(t, path), original) {
		t.Error("our host edit was not rolled back")
	}
}
