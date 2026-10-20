package routerservice

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppCheckBodySharesDeadline(t *testing.T) {
	for _, prefix := range []string{"{", `{"id":"codex"}`} {
		t.Run(prefix, func(t *testing.T) {
			// Exercise both the initial decode and the trailing-data check on a
			// real socket. The client announces more bytes and never sends them.
			layout, _ := fixture(t)
			store := routerconfig.New(layout)
			server := httptest.NewUnstartedServer(nil)
			address := server.Listener.Addr().String()
			var observations atomic.Int32
			server.Config.Handler = handlerWithModelOptions(Info{Address: address}, cloudHTTPManagement, store, ModelOptions{Budget: 100 * time.Millisecond, Observe: func(ModelEvent) { observations.Add(1) }})
			server.Start()
			defer server.Close()
			conn, err := net.Dial("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			_, err = fmt.Fprintf(conn, "POST /api/apps/check HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n%s", address, cloudHTTPManagement, prefix)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal("check body outlived budget", err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 504 || !response.Close || !bytes.Contains(body, []byte("budget_exhausted")) {
				t.Fatalf("stalled check body: status=%d close=%v body=%s error=%v", response.StatusCode, response.Close, body, err)
			}
			if observations.Load() != 0 {
				t.Fatal("incomplete draft reached executor")
			}
		})
	}
}

func TestClaudeCodeAnthropicBetaHeadersAcceptedAndStripped(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Anthropic-Beta"); got != "" {
			t.Errorf("expected anthropic-beta to be stripped before upstream, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			io.WriteString(w, `{"input_tokens":4}`)
		} else {
			io.WriteString(w, cloudHTTPReply("anthropic", false))
		}
	}))
	defer upstream.Close()

	var done ModelCompletion
	h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
	cloudHTTPBind(t, h, upstream.URL, "anthropic", "claude-code", "bound-key")

	input := cloudHTTPInput("anthropic", false, false)
	body, _ := json.Marshal(input)

	for _, sub := range []string{"messages", "messages/count_tokens"} {
		reqBody := body
		if sub == "messages/count_tokens" {
			countIn := cloudHTTPInput("anthropic", false, false)
			delete(countIn, "max_tokens")
			delete(countIn, "stream")
			reqBody, _ = json.Marshal(countIn)
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1:13145/app/claude-code/v1/"+sub, bytes.NewReader(reqBody))
		r.RemoteAddr = "127.0.0.1:2345"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Api-Key", "client-placeholder")
		r.Header.Set("Anthropic-Version", "2023-06-01")
		r.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05,effort-2025-11-24")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("claude-code %s rejected known beta headers: %d %s", sub, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", calls.Load())
	}

	// Unknown beta flags must still be rejected with 400 unsupported_input.
	r := httptest.NewRequest("POST", "http://127.0.0.1:13145/app/claude-code/v1/messages", bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:2345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Api-Key", "client-placeholder")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("Anthropic-Beta", "claude-code-20250219,unknown-beta-2025-01-01")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || done.ErrorCode != "unsupported_input" || calls.Load() != 2 {
		t.Fatalf("expected unknown beta token to fail with 400 unsupported_input, got %d %s done=%+v (calls=%d)", w.Code, w.Body.String(), done, calls.Load())
	}
}

func TestAppCheckDetectsHostCharacteristicCrossProtocolIncompatibility(t *testing.T) {
	for _, tc := range []struct {
		appID    string
		wantCode string
	}{
		{appID: "workbuddy", wantCode: "capability_message_agent"},
		{appID: "loomy", wantCode: "capability_request_comment"},
	} {
		for _, upstreamProtocol := range []string{"chat", "responses", "anthropic"} {
			t.Run(tc.appID+"/"+upstreamProtocol, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply(upstreamProtocol, false))
				}))
				defer upstream.Close()

				h, _, draft, _ := checkFixture(t, upstream.URL, upstreamProtocol, ModelOptions{})
				draft.ID = tc.appID
				draft.Name = tc.appID
				w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
				if upstreamProtocol == "chat" {
					if w.Code != 200 || calls.Load() != 1 {
						t.Fatalf("expected native chat check to succeed for %s, got %d %s calls=%d", tc.appID, w.Code, w.Body.String(), calls.Load())
					}
				} else {
					if w.Code != 400 || calls.Load() != 0 || !strings.Contains(w.Body.String(), tc.wantCode) {
						t.Fatalf("expected cross-protocol check for %s->%s to fail with 400 %s, got %d %s calls=%d", tc.appID, upstreamProtocol, tc.wantCode, w.Code, w.Body.String(), calls.Load())
					}
				}
			})
		}
	}
}
