package routerservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

func cloudHTTPObservedHandler(t *testing.T, options ModelOptions) http.Handler {
	t.Helper()
	layout, _ := fixture(t)
	return handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, routerconfig.New(layout), options)
}

func TestCloudHTTPActualAttemptsBudgetAndCompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		success    bool
		wantCalls  int
		budget     time.Duration
	}{
		{"retry-success", 503, "0", true, 2, time.Second},
		{"two-attempt-cap", 429, "0", false, 2, time.Second},
		{"permanent-refusal", 401, "0", false, 1, time.Second},
		{"retry-after-too-long", 503, "4", false, 1, time.Second},
		{"shared-budget", 429, "3", false, 1, 300 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var bodies []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
				if n == 2 && tc.success {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply("chat", false))
					return
				}
				w.Header().Set("Retry-After", tc.retryAfter)
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":"private raw body bound-provider-key"}`)
			}))
			defer upstream.Close()
			var events []ModelEvent
			var completed []ModelCompletion
			h := cloudHTTPObservedHandler(t, ModelOptions{Budget: tc.budget, Observe: func(e ModelEvent) { events = append(events, e) }, Complete: func(c ModelCompletion) { completed = append(completed, c) }})
			cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
			w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
			if calls.Load() != int32(tc.wantCalls) || len(completed) != 1 {
				t.Fatalf("calls=%d completions=%d body=%s", calls.Load(), len(completed), w.Body.String())
			}
			c := completed[0]
			if c.RequestID != w.Header().Get("X-Request-ID") || c.AppID != "codex" || c.Result.Attempts != tc.wantCalls || len(c.Result.AttemptDetails) != tc.wantCalls {
				t.Fatalf("uncorrelated completion: %+v", c)
			}
			// This upstream deliberately omitted usage. An unreported value stays unknown.
			if c.Result.Usage != nil {
				t.Fatalf("unknown usage invented: %+v", c.Result.Usage)
			}
			if tc.success {
				if w.Code != 200 || c.ErrorCode != "" || !c.Result.OutputCommitted {
					t.Fatalf("success: %d %+v %s", w.Code, c, w.Body.String())
				}
			} else if w.Code == 200 || c.ErrorCode == "" || c.Result.OutputCommitted {
				t.Fatalf("failure: %d %+v %s", w.Code, c, w.Body.String())
			}
			count := 0
			for _, e := range events {
				if e.RequestID != c.RequestID || e.AppID != "codex" {
					t.Fatal("uncorrelated event")
				}
				if e.Event.Kind == "attempt_started" {
					count++
				}
			}
			if count != tc.wantCalls {
				t.Fatalf("actual attempts duplicated: %d events=%+v", count, events)
			}
			if len(bodies) == 2 && bodies[0] != bodies[1] {
				t.Fatal("retry changed target/model/body")
			}
			safe, _ := json.Marshal(struct {
				Events     []ModelEvent
				Completion ModelCompletion
			}{events, c})
			for _, secret := range []string{"bound-provider-key", cloudHTTPManagement, "private raw body", upstream.URL, "hello"} {
				if strings.Contains(string(safe), secret) || strings.Contains(w.Body.String(), "private raw body") {
					t.Fatal("unsafe observer or error metadata")
				}
			}
		})
	}
}

func TestCloudHTTPCancelRetryWait(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(503)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var done ModelCompletion
	var waits int
	h := cloudHTTPObservedHandler(t, ModelOptions{Budget: 10 * time.Second, Observe: func(e ModelEvent) {
		if e.Event.Kind == "retry_wait_started" {
			waits++
			cancel()
		}
	}, Complete: func(c ModelCompletion) { done = c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
	w := cloudHTTPCall(t, h, ctx, "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	if waits != 1 || calls.Load() != 1 || done.Result.Attempts != 1 || done.ErrorCode != "request_canceled" {
		t.Fatalf("cancel did not stop wait: waits=%d calls=%d completion=%+v body=%s", waits, calls.Load(), done, w.Body.String())
	}
}

func TestCloudHTTPSharedDeadlineCancelsUpstream(t *testing.T) {
	var calls atomic.Int32
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-time.After(10 * time.Second):
			t.Error("upstream deadline cancellation missing")
		}
	}))
	defer upstream.Close()
	var done ModelCompletion
	h := cloudHTTPObservedHandler(t, ModelOptions{Budget: 150 * time.Millisecond, Complete: func(c ModelCompletion) { done = c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
	w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	if w.Code != 504 || calls.Load() != 1 || done.Result.Attempts != 1 || done.ErrorCode != "cloud_timeout" {
		t.Fatalf("deadline escaped: %d calls=%d completion=%+v %s", w.Code, calls.Load(), done, w.Body.String())
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream context not canceled")
	}
}

func TestCloudHTTPClientDisconnectCancelsCloud(t *testing.T) {
	arrived := make(chan struct{})
	canceled := make(chan struct{})
	complete := make(chan ModelCompletion, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(arrived)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-time.After(10 * time.Second):
			t.Error("upstream client disconnect missing")
		}
	}))
	defer upstream.Close()
	h := cloudHTTPObservedHandler(t, ModelOptions{Budget: 10 * time.Second, Complete: func(c ModelCompletion) { complete <- c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
	product := httptest.NewServer(h)
	defer product.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(cloudHTTPInput("chat", false, false))
	req, _ := http.NewRequestWithContext(ctx, "POST", product.URL+"/app/codex/v1/chat/completions", strings.NewReader(string(body)))
	req.Host = "127.0.0.1:13145"
	req.Header.Set("Content-Type", "application/json")
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := product.Client().Do(req)
		if response != nil {
			response.Body.Close()
		}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("product HTTP never reached upstream")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("client disconnect did not cancel upstream")
	}
	select {
	case done := <-complete:
		if done.ErrorCode != "request_canceled" || done.Result.Attempts != 1 {
			t.Fatalf("disconnect completion: %+v", done)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no canceled completion")
	}
	<-clientDone
}

func TestCloudHTTPStreamFailureAfterTextOrToolDoesNotRetryOrAppendError(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool-%t", source, tool), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					delta := `{"content":"visible text"}`
					if tool {
						delta = `{"tool_calls":[{"index":0,"id":"call_new","type":"function","function":{"name":"run","arguments":"{\"path\":\"next.txt\"}"}}]}`
					}
					packet := `data: {"id":"chunk","choices":[{"index":0,"delta":` + delta + `}]}` + "\n\n"
					if tool {
						frames := strings.Split(cloudHTTPStream(source, true), "\n\n")
						keep := map[string]int{"chat": 1, "responses": 3, "anthropic": 4}[source]
						packet = strings.Join(frames[:keep], "\n\n") + "\n\n"
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Content-Length", fmt.Sprint(len(packet)+1000))
					io.WriteString(w, packet)
					w.(http.Flusher).Flush()
				}))
				defer upstream.Close()
				var done ModelCompletion
				h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
				target := "chat"
				if tool {
					target = source
				}
				cloudHTTPBind(t, h, upstream.URL, target, "codex", "bound-provider-key")
				w := cloudHTTPCall(t, h, context.Background(), "codex", source, cloudHTTPInput(source, true, false), "", "")
				marker := "visible text"
				if tool {
					marker = "call_new"
				}
				if w.Code != 200 || !strings.Contains(w.Body.String(), marker) || calls.Load() != 1 || done.Result.Attempts != 1 || !done.Result.OutputCommitted || done.ErrorCode == "" {
					t.Fatalf("stream boundary: %d calls=%d completion=%+v %s", w.Code, calls.Load(), done, w.Body.String())
				}
				for _, bad := range []string{`[DONE]`, `response.completed`, `message_stop`} {
					if strings.Contains(w.Body.String(), bad) {
						t.Fatalf("partial output falsely completed or appended error %q: %s", bad, w.Body.String())
					}
				}
			})
		}
	}
}
