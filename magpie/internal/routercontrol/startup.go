package routercontrol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/filelock"
)

// An OS lock serializes native starts and is released even if the CLI exits
// unexpectedly. Keep the file/inode in place for processes already waiting.
func lockStartup(ctx context.Context, l dirs.Layout) (func(), error) {
	if err := safePath(l.State); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.State, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(l.State, "service-start.lock")
	if err := safePath(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		locked, err := filelock.TryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() { filelock.Unlock(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// Called under lockStartup. A reused PID proves the recorded process exited;
// that unrelated process and any listener on the former port are left alone.
func archiveExitedLock(l dirs.Layout) error {
	r, err := ReadRecord(l)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil || !samePath(exe, r.Executable) {
		return errors.New("another executable owns this layout; use its own installed CLI")
	}
	before, err := readRegular(RecordFile(l))
	if err != nil {
		return err
	}
	stamp, actual, err := processIdentity(r.PID)
	if err == nil && stamp == r.ProcessStamp && samePath(actual, r.Executable) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("cannot establish whether the recorded service exited: %w", err)
	}
	entries, err := os.ReadDir(l.ServiceLock())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "instance.json" && entry.Name() != "management-token" {
			return errors.New("exited service lock contains unknown files; no lock was changed")
		}
		if _, err := readRegular(filepath.Join(l.ServiceLock(), entry.Name())); err != nil {
			return err
		}
	}
	after, err := readRegular(RecordFile(l))
	if err != nil || !bytes.Equal(before, after) {
		return errors.New("service identity changed during startup; retry")
	}
	archive := filepath.Join(l.State, "router.lock.exited-"+time.Now().UTC().Format("20060102-150405.000000000"))
	return os.Rename(l.ServiceLock(), archive)
}
