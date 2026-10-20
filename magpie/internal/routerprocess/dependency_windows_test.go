package routerprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const dependencyProbeEnv = "ROUTERPROCESS_TEST_DEPENDENCY_PROBE"
const dependencyVersionProbeEnv = "ROUTERPROCESS_TEST_VERSION_PROBE"

type dependencyLaunchRecord struct {
	Directory, PATH, SystemRoot, Backend, Model string
}

// Only the explicitly spawned test executable enters either probe. The PE
// fixtures beside it are data for validation, not libraries loaded by this Go
// executable. Real CRT loading is a separate native acceptance check.
func init() {
	if len(os.Args) < 2 {
		return
	}
	if output := os.Getenv(dependencyProbeEnv); output != "" && os.Args[1] == "--host" {
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(61)
		}
		record := dependencyLaunchRecord{Directory: dir, PATH: os.Getenv("PATH"), SystemRoot: os.Getenv("SystemRoot"), Backend: os.Getenv("GGML_BACKEND_PATH"), Model: os.Getenv("LLAMA_ARG_MODEL")}
		data, _ := json.Marshal(record)
		if os.WriteFile(output, data, 0o600) != nil {
			os.Exit(62)
		}
	}
	if mode := os.Getenv(dependencyVersionProbeEnv); mode != "" && os.Args[1] == "--version" {
		if mode == "success" {
			fmt.Println("fixture b10828 version")
			os.Exit(0)
		}
		if os.WriteFile(mode, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			os.Exit(63)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
}

func dependencyNativeFixture(t *testing.T) Spec {
	t.Helper()
	executable, deps := dependencyFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	dest, err := os.OpenFile(executable, os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy test engine: %v, %v", copyErr, closeErr)
	}
	model := filepath.Join(filepath.Dir(executable), "fixture.gguf")
	if err := os.WriteFile(model, []byte("not real weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Spec{Executable: executable, ModelPath: model, Backend: "cpu", RuntimeDependencies: deps}
}

func TestWindowsDependencyLaunchUsesVerifiedDirectoryAndIsolatedEnvironment(t *testing.T) {
	spec := dependencyNativeFixture(t)
	untrusted := t.TempDir()
	t.Chdir(untrusted)
	t.Setenv("PATH", untrusted)
	t.Setenv("GGML_BACKEND_PATH", untrusted)
	t.Setenv("LLAMA_ARG_MODEL", "untrusted.gguf")
	output := filepath.Join(t.TempDir(), "child.json")
	spec.Env = []string{dependencyProbeEnv + "=" + output}
	p, err := (NativeFactory{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	awaitHealth(t, p)
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var record dependencyLaunchRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	windowsDir, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if !sameDependencyPath(record.Directory, filepath.Dir(spec.Executable)) || record.PATH != systemDir+";"+windowsDir || record.SystemRoot != windowsDir || record.Backend != "" || record.Model != "" {
		t.Fatalf("unisolated child: %+v", record)
	}
	if os.Getenv("PATH") != untrusted || os.Getenv("GGML_BACKEND_PATH") != untrusted || os.Getenv("LLAMA_ARG_MODEL") != "untrusted.gguf" {
		t.Fatal("launch changed the parent environment")
	}
}

func TestWindowsRuntimeVersionUsesOwnedIsolatedProbeAndJoinsCancellation(t *testing.T) {
	spec := dependencyNativeFixture(t)
	spec.Env = []string{dependencyVersionProbeEnv + "=success"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := RuntimeVersion(ctx, spec)
	if err != nil || !strings.Contains(string(output), "fixture b10828 version") {
		t.Fatalf("version: %s, %v", output, err)
	}
	pidPath := filepath.Join(t.TempDir(), "pid")
	spec.Env = []string{dependencyVersionProbeEnv + "=" + pidPath}
	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()
	result := make(chan error, 1)
	go func() { _, err := RuntimeVersion(probeCtx, spec); result <- err }()
	joined := false
	t.Cleanup(func() {
		cancelProbe()
		if !joined {
			select {
			case <-result:
			case <-time.After(30 * time.Second):
				t.Error("version probe cleanup did not join")
			}
		}
	})
	var pid int
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for pid == 0 {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(string(data))
		}
		if pid > 0 {
			break
		}
		select {
		case err := <-result:
			joined = true
			t.Fatalf("probe stopped before readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("probe never signaled readiness")
		case <-ticker.C:
		}
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	cancelProbe()
	select {
	case err := <-result:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("probe cancellation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("probe did not join cancellation")
	}
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("probe still alive after return: state=%d err=%v", state, err)
	}
}
