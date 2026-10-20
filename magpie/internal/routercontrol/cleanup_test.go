package routercontrol

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/proc"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupFailureCleanupOwnsExactChild(t *testing.T) {
	if os.Args[len(os.Args)-1] == "router-control-cleanup-child" {
		for {
			time.Sleep(time.Minute)
		}
	}
	for _, own := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "changed-record"}[own], func(t *testing.T) {
			l := dirs.Layout{State: t.TempDir()}
			if err := os.Mkdir(l.ServiceLock(), 0o700); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := proc.Command(exe, "-test.run=^TestStartupFailureCleanupOwnsExactChild$", "--", "router-control-cleanup-child")
			hide(cmd)
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer cmd.Process.Kill()
			stamp, actual, err := processIdentity(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			owned := Record{PID: cmd.Process.Pid, Executable: actual, ProcessStamp: stamp, Address: "127.0.0.1:9", InstanceID: "synthetic-start"}
			r := owned
			if !own {
				r.ProcessStamp = "changed-by-another-owner"
			}
			b, _ := json.Marshal(r)
			if err = os.WriteFile(RecordFile(l), b, 0o600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(l.ServiceLock(), "management-token"), []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err = cleanupStartBudget(l, owned, cmd.Process, done, 150*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if Verify(owned) == nil {
				t.Fatal("failed startup child remains alive")
			}
			_, err = os.Stat(l.ServiceLock())
			if own && !os.IsNotExist(err) {
				t.Fatal("owned startup lock leaked", err)
			}
			if !own && err != nil {
				t.Fatal("another instance record was removed", err)
			}
		})
	}
}
func TestRestartLogCannotFollowLink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "unrelated.txt")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(base, "logs")
	if err := os.Mkdir(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(logs, "service-restart.log")); err != nil {
		t.Skip("symlink unavailable: ", err)
	}
	if _, err := SpawnRestart(dirs.Layout{Logs: logs}); err == nil {
		t.Fatal("restart followed an unrelated log link")
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "unchanged" {
		t.Fatal("unrelated log modified", err)
	}
}
