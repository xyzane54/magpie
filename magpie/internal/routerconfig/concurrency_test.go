package routerconfig

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// Simulate a cooperative writer in another process by holding the on-disk
// directory lock. Every public reader/writer must wait and respect cancellation.
func TestConfigurationOperationsWaitAndCancel(t *testing.T) {
	operations := map[string]func(context.Context, *Store) error{
		"load":       func(c context.Context, s *Store) error { _, e := s.LoadContext(c); return e },
		"initialize": func(c context.Context, s *Store) error { _, e := s.InitializeContext(c); return e },
		"update": func(c context.Context, s *Store) error {
			return s.UpdateContext(c, func(Document) error { return nil })
		},
		"apps":      func(c context.Context, s *Store) error { _, e := s.AppsContext(c); return e },
		"providers": func(c context.Context, s *Store) error { _, e := s.CloudProvidersContext(c); return e },
		"resolve-provider": func(c context.Context, s *Store) error {
			_, e := s.ResolveCloudProviderContext(c, providerDraft())
			return e
		},
		"save-provider": func(c context.Context, s *Store) error {
			_, e := s.SaveCloudProviderContext(c, providerDraft())
			return e
		},
		"delete-provider": func(c context.Context, s *Store) error { return s.DeleteCloudProviderContext(c, "missing", 1) },
		"save-app":        func(c context.Context, s *Store) error { _, e := s.SaveAppContext(c, appDraft("codex", "")); return e },
		"delete-app":      func(c context.Context, s *Store) error { return s.DeleteAppContext(c, "codex", 0) },
		"resolve-app":     func(c context.Context, s *Store) error { _, e := s.ResolveAppContext(c, "codex", "", ""); return e },
		"resolve-draft": func(c context.Context, s *Store) error {
			_, e := s.ResolveAppDraftContext(c, appDraft("codex", ""), "")
			return e
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			s := providerSetup(t)
			path := s.layout.ConfigFile() + ".lock"
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			var wg sync.WaitGroup
			wg.Add(1)
			defer func() { cancel(); _ = os.Remove(path); wg.Wait() }()
			go func() { defer wg.Done(); done <- operation(ctx, New(s.layout)) }()
			// A short period held by a foreign writer is ordinary contention.
			select {
			case err := <-done:
				cancel()
				t.Fatalf("did not wait for writer: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("canceled operation still running")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("stole foreign lock", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			unchanged(t, s.layout.ConfigFile(), fixture)
			// Operation has finished before releasing the foreign lock; it cannot
			// wake up later and mutate the file or start a detached read.
		})
	}
}

func TestForeignWriterReleaseAndRevisionConflict(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	in := providerDraft()
	in.ID, in.Revision = p.ID, p.Revision
	path := s.layout.ConfigFile() + ".lock"
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	var wg sync.WaitGroup
	defer func() { _ = os.Remove(path); wg.Wait() }()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := New(s.layout).SaveCloudProvider(in); done <- e }()
	}
	close(start)
	select {
	case e := <-done:
		t.Fatalf("writer did not wait: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			success++
		} else if errors.Is(e, ErrProviderConflict) {
			conflict++
		} else {
			t.Error(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d revision conflicts=%d", success, conflict)
	}
}

func TestIndependentSnapshotsAndCorruption(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveApp(appDraft("codex", p.ID)); err != nil {
		t.Fatal(err)
	}
	first, err := s.ResolveApp("codex", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target, e := New(s.layout).ResolveApp("codex", "", "")
			if e != nil {
				t.Error(e)
				return
			}
			target.Provider.Headers["X-Tenant"] = "mutated"
			target.Provider.ExtraBody["prompt_cache_key"] = nil
		}()
	}
	wg.Wait()
	if first.Provider.Headers["X-Tenant"] != "test-private-tenant" {
		t.Fatal("request snapshots share mutable headers")
	}
	for _, path := range []string{s.layout.ConfigFile(), s.layout.CredentialsFile()} {
		original, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		put(t, path, "{broken")
		if _, e := s.ResolveApp("codex", "", ""); e == nil {
			t.Fatal("cached snapshot hid corruption")
		}
		if _, e := s.SaveApp(appDraft("other-app", p.ID)); e == nil {
			t.Fatal("overwrote damaged configuration")
		}
		unchanged(t, path, "{broken")
		put(t, path, string(original))
	}
}

func TestLocalQueuedReadCancellationAndBudget(t *testing.T) {
	s := providerSetup(t)
	ownerCtx, stopOwner := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopOwner()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	go func() {
		done <- s.UpdateContext(ownerCtx, func(Document) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ownerCtx.Done():
				return ownerCtx.Err()
			}
		})
	}()
	defer func() {
		unblock()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("local writer never acquired lock")
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
		}
		read := make(chan error, 1)
		go func() { _, err := New(s.layout).LoadContext(ctx); read <- err }()
		if !deadline {
			select {
			case err := <-read:
				cancel()
				t.Fatalf("queued read returned early: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
		}
		err := <-read
		cancel()
		want := context.Canceled
		if deadline {
			want = context.DeadlineExceeded
		}
		if !errors.Is(err, want) {
			t.Fatalf("queued read: %v, want %v", err, want)
		}
	}
	unblock()
	if _, err := New(s.layout).Load(); err != nil {
		t.Fatal("canceled queue poisoned subsequent read", err)
	}
}
