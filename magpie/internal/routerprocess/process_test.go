package routerprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/yetone/magpie/internal/routerprocess/fakeserver"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--host" {
		fakeserver.Main()
	}
	if len(os.Args) > 1 && os.Args[1] == "--routerprocess-parent" {
		runParentHelper()
		return
	}
	testenv.Main(m)
}

func fixtureSpec(t *testing.T, extra ...string) Spec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "fixture.gguf")
	if err := os.WriteFile(model, []byte("not real weights"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Spec{Executable: executable, ModelPath: model, Backend: "cpu", ExtraArgs: extra}
}

func startFixture(t *testing.T, extra ...string) Process {
	t.Helper()
	p, err := (NativeFactory{}).Start(context.Background(), fixtureSpec(t, extra...))
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
	return p
}

func awaitHealth(t *testing.T, p Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for {
		if p.Health(ctx) == nil {
			return
		}
		select {
		case <-p.Done():
			t.Fatalf("fixture exited before health: %v", p.Err())
		case <-ctx.Done():
			t.Fatal("fixture never healthy")
		case <-timer.C:
		}
	}
}

func awaitDone(t *testing.T, p Process) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("owned child was not reaped")
	}
}

func TestNativeWireLifecycleAndConcurrentStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := (NativeFactory{}).Start(ctx, fixtureSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	// Launch-context cancellation must not own the process lifetime.
	cancel()
	awaitHealth(t, p)
	if !strings.HasPrefix(p.Endpoint(), "http://127.0.0.1:") || p.PID() <= 0 {
		t.Fatalf("bad process identity: %s %d", p.Endpoint(), p.PID())
	}
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	response, err := client.Post(p.Endpoint()+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "fake completion") {
		t.Fatalf("completion: %s, %v", body, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := p.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	awaitDone(t, p)
	if err := p.Health(context.Background()); err == nil {
		t.Fatal("exited process reported healthy")
	}
}

func TestStartupFailureAndRuntimeCrash(t *testing.T) {
	t.Run("startup", func(t *testing.T) {
		p := startFixture(t, "--fake-start-fail")
		awaitDone(t, p)
		if p.Err() == nil || p.Health(context.Background()) == nil {
			t.Fatal("startup failure lost")
		}
	})
	t.Run("crash", func(t *testing.T) {
		p := startFixture(t)
		awaitHealth(t, p)
		client := NewHTTPClient()
		defer client.CloseIdleConnections()
		response, _ := client.Post(p.Endpoint()+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"messages":[{}],"fake":{"mode":"crash"}}`))
		if response != nil {
			response.Body.Close()
		}
		awaitDone(t, p)
		if p.Err() == nil {
			t.Fatal("runtime crash lost")
		}
	})
}

func TestHealthParsesStatusAndRefusesRedirects(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"loading"}`, `{"status":"ok"}more`, `{"status":`, strings.Repeat(" ", 4097)} {
		t.Run(fmt.Sprintf("body %d", len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			p := nativeProcess{endpoint: server.URL, done: make(chan struct{}), client: NewHTTPClient()}
			defer p.client.CloseIdleConnections()
			if p.Health(context.Background()) == nil {
				t.Fatal("invalid HTTP 200 health accepted")
			}
		})
	}
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	p := nativeProcess{endpoint: redirect.URL, done: make(chan struct{}), client: NewHTTPClient()}
	defer p.client.CloseIdleConnections()
	if p.Health(context.Background()) == nil || reached.Load() {
		t.Fatal("health followed a redirect")
	}
	if p.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("local client has a proxy path")
	}
}

func TestLoadingAndHTTP200Unhealthy(t *testing.T) {
	for _, args := range [][]string{{"--fake-unhealthy"}, {"--fake-health-status", "loading"}} {
		p := startFixture(t, args...)
		client := NewHTTPClient()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", p.Endpoint()+"/health", nil)
			response, err := client.Do(req)
			if err == nil {
				response.Body.Close()
				break
			}
			if ctx.Err() != nil {
				t.Fatal("fake did not listen")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if p.Health(ctx) == nil {
			t.Fatal("unhealthy fake accepted")
		}
		cancel()
		client.CloseIdleConnections()
	}
}

func TestValidationPreventsContractOverrides(t *testing.T) {
	spec := fixtureSpec(t)
	for _, args := range [][]string{{"--host=0.0.0.0"}, {"--parallel", "2"}, {"--no-cache-prompt"}, {"--model-url", "https://invalid"}} {
		copy := spec
		copy.ExtraArgs = args
		if _, err := (NativeFactory{}).Start(context.Background(), copy); err == nil {
			t.Fatalf("accepted override: %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (NativeFactory{}).Start(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch: %v", err)
	}
	args := strings.Join(arguments(spec, 8123), " ")
	for _, fixed := range []string{"--host 127.0.0.1", "--port 8123", "--parallel 1", "--cache-reuse 256", "--jinja", "--n-gpu-layers 0"} {
		if !strings.Contains(args, fixed) {
			t.Errorf("missing fixed argument %q: %s", fixed, args)
		}
	}
}

type helperRecord struct {
	PID      int
	Endpoint string
}

// The parent helper deliberately does not Stop: its abrupt termination tests
// OS ownership, independently of the Router's normal shutdown hook.
func runParentHelper() {
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		os.Exit(31)
	}
	var spec Spec
	if json.Unmarshal(data, &spec) != nil {
		os.Exit(32)
	}
	p, err := (NativeFactory{}).Start(context.Background(), spec)
	if err != nil {
		os.Exit(33)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for p.Health(ctx) != nil {
		if ctx.Err() != nil {
			os.Exit(34)
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ = json.Marshal(helperRecord{PID: p.PID(), Endpoint: p.Endpoint()})
	if os.WriteFile(os.Args[3], data, 0o600) != nil {
		os.Exit(35)
	}
	for {
		time.Sleep(time.Hour)
	}
}
