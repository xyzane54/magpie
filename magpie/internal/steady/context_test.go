package steady

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCanceledReadDoesNotRetryWindowsSharingFailure(t *testing.T) {
	oldWindows, oldRead := windows, readFile
	defer func() { windows, readFile = oldWindows, oldRead }()
	windows = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	readFile = func(string) ([]byte, error) { calls++; cancel(); return nil, fs.ErrPermission }
	if _, err := ReadFileContext(ctx, "unused"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("read retried after cancellation: calls=%d err=%v", calls, err)
	}
}

func TestCanceledWriteDoesNotCommitAfterFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldSync := Sync
	defer func() { Sync = oldSync }()
	Sync = func(f *os.File) error { err := oldSync(f); cancel(); return err }
	if err := WriteFileContext(ctx, path, []byte("new"), 0o600); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "old" {
		t.Fatal("canceled write committed", err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.json.*"))
	if err != nil || len(files) != 0 {
		t.Fatal("canceled write left temporary files", files, err)
	}
}
