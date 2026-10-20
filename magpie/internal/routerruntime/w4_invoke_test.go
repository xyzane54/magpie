package routerruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Invoke is the business entry point. A healthy process alone is not an
// acceptance result, so a caller must register versioned evidence first.
func TestInvokeRequiresVersionedCapabilityBeforeLocalDispatch(t *testing.T) {
	m, _, f, _ := managerFixture(t, nil)
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if err := m.Invoke(context.Background(), Execution, body, func(*http.Response) error { return nil }); !errors.Is(err, ErrCapability) {
		t.Fatalf("Invoke without an acceptance suite returned %v", err)
	}
	if f.count.Load() != 0 {
		t.Fatal("capability rejection started the local process")
	}
}

// The consume callback owns response-body consumption. The execution slot must
// remain occupied while it is reading/processing the body, so a second request
// cannot run concurrently with a streamed first response.
func TestInvokeHoldsExecutionSlotThroughConsumeCallback(t *testing.T) {
	var requests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) {
		f.endpoint = server.URL
	})
	if err := m.CheckCapability(context.Background(), Check{
		Purpose: Execution,
		Suite:   "execution-fixture-v1",
		Run: func(context.Context, string) (string, error) {
			return "fixture-evidence", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],"stream":true}`)
	consuming := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- m.Invoke(context.Background(), Execution, body, func(r *http.Response) error {
			if _, err := io.ReadAll(r.Body); err != nil {
				return err
			}
			close(consuming)
			<-release
			return nil
		})
	}()
	select {
	case <-consuming:
	case <-time.After(5 * time.Second):
		t.Fatal("first Invoke did not consume its response")
	}
	eventually(t, func() bool { return m.Status().Active == 1 })

	secondEntered := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		second <- m.Use(context.Background(), Execution, func(context.Context, string) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second request entered before the first consume callback returned")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	if requests != 1 {
		mu.Unlock()
		t.Fatalf("unexpected local HTTP requests while consume was blocked: %d", requests)
	}
	mu.Unlock()

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued request did not run after consume callback returned")
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
