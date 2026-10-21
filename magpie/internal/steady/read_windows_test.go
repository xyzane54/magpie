package steady

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	winapi "golang.org/x/sys/windows"
)

func TestSharedReadLongPaths(t *testing.T) {
	root := t.TempDir()
	directory := root
	for range 6 {
		directory = filepath.Join(directory, strings.Repeat("x", 40))
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "织流账户.json")
	if err := os.WriteFile(path, []byte("accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, relative} {
		for _, read := range []func(string) ([]byte, error){ReadFile, ReadThere} {
			if body, err := read(name); err != nil || string(body) != "accounts" {
				t.Fatalf("long path %q: %q, %v", name, body, err)
			}
		}
	}
}

func TestSharedReadFollowsLinkOutsideParent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "actual.json")
	if err := os.WriteFile(target, []byte("accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias.json")
	if err := os.Symlink(target, link); errors.Is(err, winapi.ERROR_PRIVILEGE_NOT_HELD) {
		t.Skip("Windows does not permit creating the symlink fixture")
	} else if err != nil {
		t.Fatal(err)
	}
	for _, read := range []func(string) ([]byte, error){ReadFile, ReadThere} {
		if body, err := read(link); err != nil || string(body) != "accounts" {
			t.Fatalf("link to another parent: %q, %v", body, err)
		}
	}
}

func TestSharedReadKeepsAbsenceAndReadRestrictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logins.json")
	for _, read := range []func(string) ([]byte, error){ReadFile, ReadThere} {
		if _, err := read(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("genuine absence: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte("accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			syscall.CloseHandle(handle)
		}
	}()
	for _, stat := range []func(string) (os.FileInfo, error){Stat, StatOnce} {
		if info, err := stat(path); err != nil || info.Size() != int64(len("accounts")) {
			t.Fatalf("metadata must not require read-data access: %v, %v", info, err)
		}
	}
	for _, read := range []func(string) ([]byte, error){ReadFile, ReadThere} {
		if body, err := read(path); !errors.Is(err, winapi.ERROR_SHARING_VIOLATION) || len(body) != 0 {
			t.Fatalf("read restriction must remain unknown: %q, %v", body, err)
		}
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	closed = true
	if body, err := ReadFile(path); err != nil || string(body) != "accounts" {
		t.Fatalf("file after reader restriction ends: %q, %v", body, err)
	}
}
