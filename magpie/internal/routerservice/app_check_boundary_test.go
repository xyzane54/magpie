package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppCheckLocalManagementCredentialAndRevisionBoundaries(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	h, store, draft, path := checkFixture(t, upstream.URL, "chat", ModelOptions{})
	before, _ := os.ReadFile(path)
	for _, method := range []string{"GET", "HEAD", "DELETE"} {
		if w := providerRequest(t, h, method, "/api/apps/check", cloudHTTPManagement, draft); w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	if w := providerRequest(t, h, "POST", "/api/apps/check", "placeholder", draft); w.Code != 401 {
		t.Fatal("unguarded management", w.Code)
	}
	for _, tc := range []struct {
		mode   string
		mutate func(*http.Request)
	}{
		{"remote", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" }},
		{"host", func(r *http.Request) { r.Host = "evil.example" }},
		{"origin", func(r *http.Request) { r.Header.Set("Origin", "null") }},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			body, _ := json.Marshal(draft)
			r := httptest.NewRequest("POST", "http://127.0.0.1:13145/api/apps/check", bytes.NewReader(body))
			r.RemoteAddr = "127.0.0.1:1"
			r.Header.Set("Authorization", "Bearer "+cloudHTTPManagement)
			r.Header.Set("Content-Type", "application/json")
			tc.mutate(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code)
			}
		})
	}
	bad := draft
	bad.Name = cloudHTTPManagement
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, bad); w.Code != 403 || strings.Contains(w.Body.String(), cloudHTTPManagement) {
		t.Fatal("management token accepted", w.Code, w.Body.String())
	}
	bad = draft
	bad.ID = "loomy"
	bad.CredentialMode = "forward-authorization"
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, bad); w.Code != 409 || !strings.Contains(w.Body.String(), "connection_check_unsupported") {
		t.Fatal(w.Code, w.Body.String())
	}
	bad = draft
	bad.RedactionEnabled = true
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, bad); w.Code != 400 {
		t.Fatal("redaction accepted", w.Code)
	}
	bad = draft
	bad.Cloud = routerconfig.AppCloud{}
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, bad); w.Code != 409 {
		t.Fatal("missing target", w.Code)
	}
	bad = draft
	bad.ExpectedProviderRevision = 99
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, bad); w.Code != 409 || !strings.Contains(w.Body.String(), "provider_conflict") {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("rejected drafts reached cloud", calls.Load())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejections saved")
	}
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := providerInputFor(upstream.URL + "/v1")
	p.ID = draft.Cloud.ProviderID
	p.Revision = 1
	p.Name = "changed since check"
	if _, err := store.SaveCloudProvider(p); err != nil {
		t.Fatal(err)
	}
	draft.ExpectedProviderRevision = 1
	if w := providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, draft); w.Code != 409 || !strings.Contains(w.Body.String(), "provider_conflict") {
		t.Fatal("stale verified save", w.Code, w.Body.String())
	}
	draft.ExpectedProviderRevision = 2
	if w := providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, draft); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft); w.Code != 409 || !strings.Contains(w.Body.String(), "profile_conflict") {
		t.Fatal("stale app check", w.Code, w.Body.String())
	}
}

func TestAppCheckCompleteTextRequired(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"empty", `{"id":"test","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`},
		{"tools", cloudHTTPReply("chat", true)},
		{"interrupted", "data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				kind := "application/json"
				if tc.name == "interrupted" {
					kind = "text/event-stream"
				}
				w.Header().Set("Content-Type", kind)
				io.WriteString(w, tc.reply)
			}))
			defer upstream.Close()
			h, _, draft, _ := checkFixture(t, upstream.URL, "chat", ModelOptions{})
			w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
			if w.Code != 502 || strings.Contains(w.Body.String(), `"verified":true`) || calls.Load() != 1 {
				t.Fatal(w.Code, w.Body.String(), calls.Load())
			}
		})
	}
}

// Expire only once the fake upstream has received the request. A 50ms timer
// also timed configuration/JSON/scheduling work and could expire before the
// first attempt on Windows. Real socket/body budget timers are covered by
// TestAppCheckBodySharesDeadline; this case verifies in-flight expiry semantics.
type appCheckControlledDeadline struct {
	context.Context
	expired  chan struct{}
	once     sync.Once
	deadline time.Time
}

func (c *appCheckControlledDeadline) Done() <-chan struct{}       { return c.expired }
func (c *appCheckControlledDeadline) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *appCheckControlledDeadline) Err() error {
	select {
	case <-c.expired:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *appCheckControlledDeadline) expire() { c.once.Do(func() { close(c.expired) }) }

func TestAppCheckRetriesCancelAndBudget(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, cloudHTTPReply("chat", false))
		}))
		defer upstream.Close()
		h, _, draft, _ := checkFixture(t, upstream.URL, "chat", ModelOptions{})
		w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
		if w.Code != 200 || calls.Load() != 2 {
			t.Fatal(w.Code, w.Body.String(), calls.Load())
		}
	})
	t.Run("cancel-retry-wait", func(t *testing.T) {
		var calls atomic.Int32
		waiting := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(503)
		}))
		defer upstream.Close()
		h, _, draft, _ := checkFixture(t, upstream.URL, "chat", ModelOptions{Observe: func(e ModelEvent) {
			if e.Event.Kind == "retry_wait_started" {
				close(waiting)
				cancel()
			}
		}})
		data, _ := json.Marshal(draft)
		r := httptest.NewRequest("POST", "http://127.0.0.1:13145/api/apps/check", bytes.NewReader(data)).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Authorization", "Bearer "+cloudHTTPManagement)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		select {
		case <-waiting:
		default:
			t.Fatal("no retry wait")
		}
		if w.Code != 499 || calls.Load() != 1 {
			t.Fatal(w.Code, w.Body.String(), calls.Load())
		}
	})
	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int32
		ctx := &appCheckControlledDeadline{Context: context.Background(), expired: make(chan struct{}), deadline: time.Now().Add(10 * time.Second)}
		defer ctx.expire()
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.Copy(io.Discard, r.Body)
			ctx.expire()
			<-r.Context().Done()
		}))
		defer upstream.Close()
		h, _, draft, _ := checkFixture(t, upstream.URL, "chat", ModelOptions{Budget: 5 * time.Second})
		data, _ := json.Marshal(draft)
		r := httptest.NewRequest("POST", "http://127.0.0.1:13145/api/apps/check", bytes.NewReader(data)).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Authorization", "Bearer "+cloudHTTPManagement)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 504 || calls.Load() != 1 {
			t.Fatal(w.Code, w.Body.String(), calls.Load())
		}
	})
}
