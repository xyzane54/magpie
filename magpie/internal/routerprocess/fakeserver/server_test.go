package fakeserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestWireSuccessStreamErrorAndBadJSON(t *testing.T) {
	server := httptest.NewServer(New(Config{}))
	defer server.Close()
	client := server.Client()
	for _, tc := range []struct {
		name, body, contains string
		status               int
	}{
		{"normal", `{"model":"fixture","messages":[{"role":"user","content":"hello"}],"fake":{"text":"fixture answer"}}`, `"content":"fixture answer"`, 200},
		{"sse", `{"messages":[{}],"stream":true,"fake":{"chunks":["one"," two"]}}`, "data: [DONE]", 200},
		{"error", `{"messages":[{}],"fake":{"mode":"error","status":429}}`, `"code":429`, 429},
		{"bad json", `{`, "Invalid JSON", 400},
		{"missing messages", `{}`, "messages", 400},
		{"extra body", `{"messages":[{}]} {}`, "messages", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != tc.status || !strings.Contains(string(body), tc.contains) {
				t.Fatalf("status=%d body=%s err=%v", response.StatusCode, body, err)
			}
			if tc.name == "sse" && response.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatal("missing SSE content type")
			}
		})
	}
}

func TestHangCancellationAndDelayTimeout(t *testing.T) {
	for _, mode := range []string{"hang", "delay"} {
		t.Run(mode, func(t *testing.T) {
			accepted, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
			server := httptest.NewServer(New(Config{OnRequest: func() { accepted <- struct{}{} }, OnCancel: func() { canceled <- struct{}{} }}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions",
				strings.NewReader(`{"messages":[{}],"fake":{"mode":"`+mode+`","delay_ms":120000}}`))
			done := make(chan struct{})
			go func() {
				defer close(done)
				response, err := server.Client().Do(request)
				if err == nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			}()
			select {
			case <-accepted:
			case <-time.After(30 * time.Second):
				t.Fatal("request not accepted")
			}
			cancel()
			select {
			case <-canceled:
			case <-time.After(30 * time.Second):
				t.Fatal("fake did not observe cancellation")
			}
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("client did not return")
			}
		})
	}
}

func TestHealthAndCrashAreIndependentControls(t *testing.T) {
	crashed := false
	handler := New(Config{Ready: func() bool { return false }, Crash: func() { crashed = true }})
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
	if health.Code != 503 || !strings.Contains(health.Body.String(), "Loading model") {
		t.Fatal(health.Code, health.Body.String())
	}
	request := httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{}],"fake":{"mode":"crash"}}`)))
	if !crashed {
		t.Fatal("crash control not observed")
	}
	if code := Run([]string{"--fake-start-fail"}, io.Discard, func(int) {}); code != 22 {
		t.Fatalf("startup failure code=%d", code)
	}
	if code := Run([]string{"--host", "0.0.0.0"}, io.Discard, func(int) {}); code != 2 {
		t.Fatalf("nonloopback accepted: %d", code)
	}
}

func TestCommandAcceptsVerifiedTemplateFlag(t *testing.T) {
	// Startup failure ends before binding; 22 means argument parsing accepted
	// the same template option FileInstaller includes in the real Spec.
	if code := Run([]string{"--chat-template-file", "verified-template.jinja", "--fake-start-fail"}, io.Discard, func(int) {}); code != 22 {
		t.Fatalf("template flag rejected: exit %d, want controlled startup exit 22", code)
	}
}
