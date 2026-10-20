package routercontrol

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
)

func TestCopiedStartCommandKeepsLayoutAndRestoresCallerEnvironment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("executes the PowerShell command shown by the Windows product")
	}
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "user's directory $with spaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	l := dirs.Layout{Config: filepath.Join(root, "roaming", "api-router"), Data: filepath.Join(root, "local", "api-router")}
	// This stand-in observes what the actual CLI would inherit, without
	// reading user configuration or starting a real service.
	exe := filepath.Join(root, "observe.ps1")
	if err := os.WriteFile(exe, []byte(`param($action, $verb)
@{appdata=$env:APPDATA; localappdata=$env:LOCALAPPDATA; action=$action; verb=$verb} | ConvertTo-Json -Compress
`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := StartCommand(l, exe) + `; @{appdata=$env:APPDATA; localappdata=$env:LOCALAPPDATA} | ConvertTo-Json -Compress`
	cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Env = append(os.Environ(), "APPDATA=caller-roaming", "LOCALAPPDATA=caller-local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("copied command failed: %v: %s", err, out)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	var child, caller map[string]string
	if err := decoder.Decode(&child); err != nil {
		t.Fatalf("child layout missing: %v: %s", err, out)
	}
	if err := decoder.Decode(&caller); err != nil {
		t.Fatalf("caller environment missing: %v: %s", err, out)
	}
	if child["appdata"] != filepath.Dir(l.Config) || child["localappdata"] != filepath.Dir(l.Data) || child["action"] != "service" || child["verb"] != "start" {
		t.Fatalf("copied command switched layouts: %#v", child)
	}
	if caller["appdata"] != "caller-roaming" || caller["localappdata"] != "caller-local" {
		t.Fatalf("copied command changed the caller environment: %#v", caller)
	}
}
