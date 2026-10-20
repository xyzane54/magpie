package routercontrol

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
)

func startupRecordFixture(t *testing.T, live bool) dirs.Layout {
	t.Helper()
	l := dirs.Layout{State: t.TempDir()}
	if err := os.Mkdir(l.ServiceLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	stamp, exe, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		stamp += "-previous-process-before-PID-reuse"
	}
	raw, _ := json.Marshal(Record{PID: os.Getpid(), Executable: exe, ProcessStamp: stamp, InstanceID: "fixture", Address: "127.0.0.1:13145"})
	if err := os.WriteFile(RecordFile(l), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.ServiceLock(), "management-token"), []byte("fixture-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNativeStartupArchivesExitedIdentityAndPreservesLiveOrUnknownLocks(t *testing.T) {
	for _, kind := range []string{"exited-reused-PID", "live", "unknown-file"} {
		t.Run(kind, func(t *testing.T) {
			l := startupRecordFixture(t, kind == "live")
			if kind == "unknown-file" {
				if err := os.WriteFile(filepath.Join(l.ServiceLock(), "other"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := archiveExitedLock(l)
			if kind == "unknown-file" && err == nil || kind != "unknown-file" && err != nil {
				t.Fatal(kind, err)
			}
			archives, err := filepath.Glob(filepath.Join(l.State, "router.lock.exited-*"))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "exited-reused-PID" {
				if len(archives) != 1 {
					t.Fatal("exited lock was not archived")
				}
				if _, err := os.Stat(filepath.Join(archives[0], "instance.json")); err != nil {
					t.Fatal("archival lost the instance record", err)
				}
			} else if len(archives) != 0 {
				t.Fatal("live or unknown lock was moved")
			}
		})
	}
}

func TestNativeStartupOSLockSerializesAndReleases(t *testing.T) {
	l := dirs.Layout{State: t.TempDir()}
	unlock, err := lockStartup(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if other, err := lockStartup(ctx, l); err == nil {
		other()
		t.Fatal("another start acquired the held OS lock")
	}
	unlock()
	again, err := lockStartup(context.Background(), l)
	if err != nil {
		t.Fatal("released OS startup lock remained held", err)
	}
	again()
}
