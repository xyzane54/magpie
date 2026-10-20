package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/steady"
)

// Keep a real management commit in progress until every request has started.
// The delay models disk flush latency; no request outcome depends on scheduling.
func TestConfigContentionHTTP(t *testing.T) {
	for _, differentApps := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-app", true: "different-apps"}[differentApps], func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				b, _ := io.ReadAll(r.Body)
				app := "codex"
				if strings.Contains(string(b), "claude-code-marker") {
					app = "claude-code"
				}
				if r.Header.Get("Authorization") != "Bearer key-"+app || r.URL.Path != "/"+app+"/v1/chat/completions" {
					t.Error("app/provider/credential crossed at upstream")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
			}))
			defer upstream.Close()
			layout, _ := fixture(t)
			store := routerconfig.New(layout)
			h := handler(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store)
			for _, app := range []string{"codex", "claude-code"} {
				cloudHTTPBind(t, h, upstream.URL+"/"+app, "chat", app, "key-"+app)
			}
			call := func(i int) *httptest.ResponseRecorder {
				app := "codex"
				if differentApps && i%2 != 0 {
					app = "claude-code"
				}
				return cloudHTTPCall(t, h, context.Background(), app, "chat", map[string]any{"model": "profile-pinned", "messages": []map[string]string{{"role": "user", "content": app + "-marker"}}}, "Bearer placeholder", "")
			}
			for i := 0; i < 3; i++ {
				if w := call(i); w.Code != 200 {
					t.Fatalf("serial: %d %s", w.Code, w.Body.String())
				}
			}
			unblock := pauseConfigCommit(t, func() error {
				return routerconfig.New(layout).Update(func(d routerconfig.Document) error { d["runtime"] = json.RawMessage(`{"test":true}`); return nil })
			})
			const n = 32
			started := make(chan struct{}, n)
			results := make(chan *httptest.ResponseRecorder, n)
			for i := 0; i < n; i++ {
				go func(i int) { started <- struct{}{}; results <- call(i) }(i)
			}
			for i := 0; i < n; i++ {
				<-started
			}
			// Old code immediately returns 503 while the commit is held. Fixed
			// code waits; the generous timeout is only a test deadlock guard.
			select {
			case w := <-results:
				if w.Code != 200 {
					t.Errorf("normal lock contention: %d %s", w.Code, w.Body.String())
				}
				results <- w
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			for i := 0; i < n; i++ {
				w := <-results
				if w.Code != 200 {
					t.Errorf("concurrent: %d %s", w.Code, w.Body.String())
				}
			}
			if calls.Load() != 3+n {
				t.Errorf("effective outbound attempts=%d, want %d", calls.Load(), 3+n)
			}
		})
	}
}

// Pause after new immutable credentials are on disk, before config's commit.
// Install the hook before goroutines and join the writer before restoring it.
func pauseConfigCommit(t *testing.T, write func() error) func() {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	old := steady.Sync
	var pause, resume sync.Once
	steady.Sync = func(f *os.File) error {
		if strings.HasPrefix(filepath.Base(f.Name()), ".config.json.") && !strings.HasPrefix(filepath.Base(f.Name()), ".config.json.bak.") {
			pause.Do(func() { close(entered); <-release })
		}
		return old(f)
	}
	done := make(chan struct{})
	var writeErr error
	go func() { defer close(done); writeErr = write() }()
	unblock := func() { resume.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		<-done
		steady.Sync = old
		if writeErr != nil {
			t.Error("held write:", writeErr)
		}
	})
	select {
	case <-entered:
	case <-done:
		t.Fatal("writer failed before reaching commit", writeErr)
	case <-time.After(3 * time.Second):
		t.Fatal("writer never reached commit")
	}
	return unblock
}

func TestConfigCommitWithHTTPManagementAndInference(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		app, version := "codex", "1"
		if strings.Contains(string(body), "claude-code-marker") {
			app = "claude-code"
		}
		if strings.Contains(r.URL.Path, "-2/") {
			version = "2"
		}
		if r.URL.Path != "/"+app+"-"+version+"/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+app+"-key-"+version || !strings.Contains(string(body), `"model":"profile-pinned"`) {
			t.Error("mixed app/provider/credential version at upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	h := handler(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store)
	var codex routerconfig.CloudProvider
	for _, app := range []string{"codex", "claude-code"} {
		in := providerInputFor(upstream.URL + "/" + app + "-1/v1")
		key := app + "-key-1"
		in.APIKey = &key
		p := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", cloudHTTPManagement, in))
		if app == "codex" {
			codex = p
		}
		w := providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{ID: app, Name: app, Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "profile-pinned"}, CredentialMode: routerconfig.StoredCredential})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	in := providerInputFor(upstream.URL + "/codex-2/v1")
	key := "codex-key-2"
	in.APIKey = &key
	in.ID, in.Revision = codex.ID, codex.Revision
	release := pauseConfigCommit(t, func() error { _, err := routerconfig.New(layout).SaveCloudProvider(in); return err })
	results := make(chan *httptest.ResponseRecorder, 44)
	var wg sync.WaitGroup
	launch := func(call func() *httptest.ResponseRecorder) {
		wg.Add(1)
		go func() { defer wg.Done(); results <- call() }()
	}
	for i := 0; i < 32; i++ {
		app := "codex"
		if i%2 != 0 {
			app = "claude-code"
		}
		launch(func() *httptest.ResponseRecorder {
			return cloudHTTPCall(t, h, context.Background(), app, "chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": app + "-marker"}}}, "Bearer placeholder", "")
		})
	}
	for i := 0; i < 8; i++ {
		path := "/api/apps"
		if i%2 != 0 {
			path = "/api/cloud-providers"
		}
		launch(func() *httptest.ResponseRecorder { return providerRequest(t, h, "GET", path, cloudHTTPManagement, nil) })
	}
	// Two editors with the same revision: exactly one wins after waiting.
	for i := 0; i < 2; i++ {
		launch(func() *httptest.ResponseRecorder {
			return providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{ID: "custom-app", Name: "custom", CredentialMode: routerconfig.StoredCredential})
		})
	}
	// A provider editor opened before the held commit must remain a conflict.
	launch(func() *httptest.ResponseRecorder {
		return providerRequest(t, h, "POST", "/api/cloud-providers", cloudHTTPManagement, in)
	})
	release()
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for w := range results {
		switch w.Code {
		case 200:
			success++
		case 409:
			conflict++
		default:
			t.Errorf("interleaved HTTP: %d %s", w.Code, w.Body.String())
		}
	}
	if success != 41 || conflict != 2 || calls.Load() != 32 {
		t.Fatalf("success=%d conflict=%d effective attempts=%d", success, conflict, calls.Load())
	}
	resolved, err := store.ResolveApp("codex", "", "")
	if err != nil || resolved.Provider.APIKey != key || resolved.Provider.Config.BaseURL != in.BaseURL || resolved.Provider.Config.Revision != codex.Revision+1 {
		t.Fatal("new target was not committed consistently", err)
	}
}

func TestConfigWaitHTTPCancellationAndBudget(t *testing.T) {
	for _, ledgerEnabled := range []bool{false, true} {
		for _, cancelRequest := range []bool{false, true} {
			t.Run(map[bool]string{false: "plain", true: "ledger"}[ledgerEnabled]+"/"+map[bool]string{false: "budget", true: "cancel"}[cancelRequest], func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
				}))
				defer upstream.Close()
				layout, _ := fixture(t)
				store := routerconfig.New(layout)
				options := ModelOptions{Budget: 60 * time.Millisecond}
				if cancelRequest {
					options.Budget = 3 * time.Second
				}
				if ledgerEnabled {
					l, err := routerledger.Open(filepath.Join(layout.State, "requests.sqlite"), routerledger.Options{})
					if err != nil {
						t.Fatal(err)
					}
					defer l.Close()
					options.Ledger = l
				}
				h := handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store, options)
				cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "test-only-key")
				lock := layout.ConfigFile() + ".lock"
				if err := os.Mkdir(lock, 0o700); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(lock)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan *httptest.ResponseRecorder, 1)
				finished := make(chan struct{})
				defer func() { cancel(); _ = os.Remove(lock); <-finished }()
				go func() {
					defer close(finished)
					done <- cloudHTTPCall(t, h, ctx, "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
				}()
				if cancelRequest {
					select {
					case w := <-done:
						t.Fatalf("did not wait for configuration: %d %s", w.Code, w.Body.String())
					case <-time.After(30 * time.Millisecond):
					}
					cancel()
				}
				var w *httptest.ResponseRecorder
				select {
				case w = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("configuration wait outlived request")
				}
				code, status := "budget_exhausted", 504
				if cancelRequest {
					code, status = "request_canceled", 499
				}
				if w.Code != status || !strings.Contains(w.Body.String(), code) || calls.Load() != 0 {
					t.Fatalf("%d %s attempts=%d", w.Code, w.Body.String(), calls.Load())
				}
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 {
					t.Fatal("canceled request resumed after unlock")
				}
				// A live request after release proves the store was not poisoned.
				options.Budget = 3 * time.Second
				live := handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store, options)
				w = cloudHTTPCall(t, live, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatal("store failed after canceled waiter", w.Code, calls.Load())
				}
			})
		}
	}
}
