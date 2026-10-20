package routerconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Bound the entire storage operation, including the local queue and a foreign
// writer or leftover lock. A caller's shorter deadline always wins.
const storageBudget = 5 * time.Second

type configGate struct {
	turn  chan struct{}
	users int
}

var configGates = struct {
	sync.Mutex
	paths map[string]*configGate
}{paths: make(map[string]*configGate)}

// Include waiters in users so two Store instances can never create separate
// live gates for one path. Discard idle entries (and never retain credentials).
func localGate(ctx context.Context, path string) (func(), error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	configGates.Lock()
	g := configGates.paths[path]
	if g == nil {
		g = &configGate{turn: make(chan struct{}, 1)}
		configGates.paths[path] = g
	}
	g.users++
	configGates.Unlock()
	drop := func() {
		configGates.Lock()
		g.users--
		if g.users == 0 {
			delete(configGates.paths, path)
		}
		configGates.Unlock()
	}
	select {
	case g.turn <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.turn
			drop()
			return nil, err
		}
		return func() { <-g.turn; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// All config reads and writes use the same local queue and disk transaction
// lock. Only existence means contention; permission/I/O errors fail directly.
// Never steal a lock after a crash or read through another process's commit.
func (s *Store) lockContext(parent context.Context) (context.Context, func(), error) {
	return s.transactionContext(parent, true)
}

func (s *Store) readLockContext(parent context.Context) (context.Context, func(), error) {
	return s.transactionContext(parent, false)
}

func (s *Store) transactionContext(parent context.Context, create bool) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(parent, storageBudget)
	release, err := localGate(ctx, s.layout.ConfigFile())
	if err != nil {
		cancel()
		return nil, nil, err
	}
	fail := func(err error) (context.Context, func(), error) { release(); cancel(); return nil, nil, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if create {
		if err := s.layout.Ensure(); err != nil {
			return fail(err)
		}
	} else {
		// A read/healthcheck on an uninitialized installation must not create
		// product directories. Once present, this directory hosts the same
		// transaction lock as writers, including a first initialization.
		fi, err := os.Lstat(s.layout.Config)
		if err != nil {
			return fail(err)
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("config directory must be a real directory"))
		}
	}
	p := s.layout.ConfigFile() + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		err := os.Mkdir(p, 0o700)
		if err == nil {
			unlock := func() { _ = os.Remove(p); release(); cancel() }
			if err := ctx.Err(); err != nil {
				unlock()
				return nil, nil, err
			}
			return ctx, unlock, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fail(fmt.Errorf("config lock: %w", err))
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(fmt.Errorf("config lock (another writer or leftover lock): %w", ctx.Err()))
		case <-timer.C:
		}
	}
}

// A fresh, owned snapshot, never a shared decoded map or a timestamp cache.
// Both files are read under one transaction; resolution and upstream I/O run
// after releasing the locks. Invalid files never fall back to an old snapshot.
func (s *Store) snapshot(ctx context.Context) (providerFiles, error) {
	ctx, unlock, err := s.readLockContext(ctx)
	if err != nil {
		return providerFiles{}, err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err == nil {
		err = ctx.Err()
	}
	return f, err
}
