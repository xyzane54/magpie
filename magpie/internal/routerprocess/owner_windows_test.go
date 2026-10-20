package routerprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"golang.org/x/sys/windows"
)

func TestWindowsAbruptParentExitKillsOwnedModel(t *testing.T) {
	spec := fixtureSpec(t)
	dir := t.TempDir()
	specFile, recordFile := filepath.Join(dir, "spec.json"), filepath.Join(dir, "child.json")
	data, _ := json.Marshal(spec)
	if err := os.WriteFile(specFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Plain proc.Command here is deliberate: an outer proc.Tree job would
	// mask a missing child-ownership job in NativeFactory.
	parent := proc.Command(spec.Executable, "--routerprocess-parent", specFile, recordFile)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			parent.Process.Kill()
			parent.Wait()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	var record helperRecord
	for {
		data, err := os.ReadFile(recordFile)
		if err == nil && json.Unmarshal(data, &record) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parent never reported its healthy child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(record.PID))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	waited = true
	status, err := windows.WaitForSingleObject(child, 30_000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("parent exited but model PID %d remained: status=%d error=%v", record.PID, status, err)
	}
}
