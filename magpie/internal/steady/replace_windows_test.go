package steady

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A reader that permits replacement may keep reading the old generation.
// Publishing the next one must not leave the path missing or wait for that
// reader to finish. MoveFileEx cannot provide this while the old file is open.
func TestReplaceWhileReadingPreviousGeneration(t *testing.T) {
	for _, operation := range []string{"Rename", "WriteFile"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "logins.json")
			if err := os.WriteFile(path, []byte("old accounts"), 0o600); err != nil {
				t.Fatal(err)
			}
			reader, err := openReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if operation == "Rename" {
				staged := filepath.Join(dir, "logins.json.tmp")
				if err := os.WriteFile(staged, []byte("new accounts"), 0o600); err != nil {
					t.Fatal(err)
				}
				err = Rename(staged, path)
			} else {
				err = WriteFile(path, []byte("new accounts"), 0o600)
			}
			if err != nil {
				body, readErr := os.ReadFile(path)
				_, statErr := os.Stat(path)
				t.Fatalf("publishing while an old reader permits replacement: %v; new reader: %q, %v; stat: %v", err, body, readErr, statErr)
			}
			if body, err := os.ReadFile(path); err != nil || string(body) != "new accounts" {
				t.Fatalf("new reader: %q, %v", body, err)
			}
			if body, err := io.ReadAll(reader); err != nil || string(body) != "old accounts" {
				t.Fatalf("previous reader: %q, %v", body, err)
			}
		})
	}
}

func TestRenameMissingSourceKeepsPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logins.json")
	if err := os.WriteFile(path, []byte("old accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(filepath.Join(dir, "missing"), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing source: %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "old accounts" {
		t.Fatalf("previous generation: %q, %v", body, err)
	}
}

func TestReadOnlyDestinationKeepsPreviousGeneration(t *testing.T) {
	for _, operation := range []string{"Rename", "WriteFile"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "logins.json")
			if err := os.WriteFile(path, []byte("old accounts"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Error(err)
				}
			})
			var err error
			if operation == "Rename" {
				staged := filepath.Join(dir, "logins.json.tmp")
				if err := os.WriteFile(staged, []byte("new accounts"), 0o600); err != nil {
					t.Fatal(err)
				}
				err = Rename(staged, path)
			} else {
				err = WriteFile(path, []byte("new accounts"), 0o600)
				files, globErr := filepath.Glob(filepath.Join(dir, ".logins.json.*"))
				if globErr != nil || len(files) != 0 {
					t.Fatalf("failed write left staging files: %v, %v", files, globErr)
				}
			}
			if err == nil || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("read-only destination must fail without reporting absence: %v", err)
			}
			if body, err := os.ReadFile(path); err != nil || string(body) != "old accounts" {
				t.Fatalf("previous generation: %q, %v", body, err)
			}
		})
	}
}

func TestRenameBetweenDirectories(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	for _, directory := range []string{from, to} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	staged, destination := filepath.Join(from, "staged"), filepath.Join(to, "data")
	if err := os.WriteFile(staged, []byte("new accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(staged, destination); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "new accounts" {
		t.Fatalf("destination: %q, %v", body, err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source was not moved: %v", err)
	}
}

func TestRenameFileToDirectoryWithTrailingSeparator(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, []byte("new accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(staged, dir+string(os.PathSeparator)); err == nil {
		t.Fatal("a file replaced its parent directory")
	}
	if body, err := os.ReadFile(staged); err != nil || string(body) != "new accounts" {
		t.Fatalf("staged source: %q, %v", body, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "staged" {
		t.Fatalf("directory destination was changed: %v, %v", entries, err)
	}
}
