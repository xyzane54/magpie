package steady

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	winapi "golang.org/x/sys/windows"
)

// The reader and publisher must coordinate between processes, including
// relative, case-varied and extended spellings of the same pathname.
func TestReplacementGuardAcrossProcesses(t *testing.T) {
	t.Chdir(t.TempDir())
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"read", "stat", "rename", "write"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Accounts.json")
			if err := os.WriteFile(path, []byte("old accounts"), 0o600); err != nil {
				t.Fatal(err)
			}
			staged := path + ".tmp"
			if err := os.WriteFile(staged, []byte("new accounts"), 0o600); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(workingDirectory, path)
			if err != nil {
				t.Fatal(err)
			}
			apply := func() error {
				switch operation {
				case "read":
					_, err := ReadFile(strings.ToUpper(path))
					return err
				case "stat":
					_, err := Stat(path)
					return err
				case "rename":
					return Rename(staged, `\\?\`+path)
				default:
					return WriteFile(relative, []byte("new accounts"), 0o600)
				}
			}
			release := holdPathInProcess(t, path, false)
			if err := apply(); !errors.Is(err, winapi.ERROR_SHARING_VIOLATION) {
				t.Errorf("%s crossed another process's publication: %v", operation, err)
			}
			if body, err := os.ReadFile(path); err != nil || string(body) != "old accounts" {
				t.Errorf("a blocked publication changed the generation: %q, %v", body, err)
			}
			other := path + ".other"
			if err := os.WriteFile(other, []byte("independent"), 0o600); err != nil {
				t.Fatal(err)
			}
			if body, err := ReadFile(other); err != nil || string(body) != "independent" {
				t.Errorf("an unrelated pathname was blocked: %q, %v", body, err)
			}
			release()
			if err := apply(); err != nil {
				t.Fatalf("%s after the publisher released: %v", operation, err)
			}
		})
	}
}

func TestReplacementGuardRecoversAfterProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte("accounts"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := holdPathInProcess(t, path, true)
	// Keep the kernel object alive so the next wait observes abandonment,
	// rather than creating a new mutex after the old owner's exit.
	handle, err := openPathMutex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer winapi.CloseHandle(handle)
	release()
	if body, err := ReadFile(path); err != nil || string(body) != "accounts" {
		t.Fatalf("read after the owning process exited: %q, %v", body, err)
	}
}

func holdPathInProcess(t *testing.T, path string, abandon bool) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	mode := "--steady-guard-release"
	if abandon {
		mode = "--steady-guard-abandon"
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplacementGuardOtherProcess$", "--", mode, path)
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() {
		_, _ = input.Write([]byte("x"))
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("publication helper: %v", err)
		}
		cancel()
	})
	t.Cleanup(release)
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(output).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "ready" {
			err = fmt.Errorf("unexpected publication helper output: %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return release
}

func TestReplacementGuardOtherProcess(t *testing.T) {
	args := os.Args
	if len(args) < 3 || !strings.HasPrefix(args[len(args)-2], "--steady-guard-") {
		return
	}
	unlock, err := lockPath(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.CopyN(io.Discard, os.Stdin, 1)
	if args[len(args)-2] == "--steady-guard-abandon" {
		os.Exit(0)
	}
	unlock()
}
