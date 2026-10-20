package routerservice

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

func TestCloudHTTPInputAndAuxiliaryBoundaries(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("rejected request reached upstream")
	}))
	defer upstream.Close()
	h := cloudHTTPHandler(t)
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-key")
	for _, tc := range []struct {
		method, path, kind, body string
		status                   int
	}{
		{"POST", "responses", "text/plain", `{"input":"hello"}`, 415},
		{"POST", "responses", "application/json", strings.Repeat(" ", 8<<20) + "{}", 413},
		{"POST", "responses", "application/json", `{"model":"profile-pinned","input":"hello","unknown_host_field":true}`, 400},
		{"POST", "responses", "application/json", `{"model":"profile-pinned","model":"profile-pinned","input":"hello"}`, 400},
		{"GET", "responses", "", "", 405},
		{"POST", "models", "application/json", "{}", 405},
		{"GET", "responses/resp_private", "", "", 404},
		{"POST", "messages/count_tokens", "application/json", `{"messages":[{"role":"user","content":"hello"}]}`, 501},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:13145/app/codex/v1/"+tc.path, strings.NewReader(tc.body))
		r.RemoteAddr = "127.0.0.1:2345"
		r.Header.Set("Content-Type", tc.kind)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/app/codex/v1", "/app/codex/v1/models"} {
		w := providerRequest(t, h, "HEAD", path, "", nil)
		if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("HEAD models: %d %s", w.Code, w.Body.String())
		}
	}
	cloudHTTPBind(t, h, upstream.URL, "chat", "no-key", "")
	w := cloudHTTPCall(t, h, context.Background(), "no-key", "chat", cloudHTTPInput("chat", false, false), "Bearer inbound-cannot-replace-missing-provider-key", "")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "credential_missing") {
		t.Fatalf("missing stored credential: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("local validation performed outbound work")
	}
}

func TestCloudHTTPBodySharesTotalDeadline(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	done := make(chan ModelCompletion, 1)
	server.Config.Handler = handlerWithModelOptions(Info{Address: address}, cloudHTTPManagement, store, ModelOptions{Budget: 100 * time.Millisecond, Complete: func(c ModelCompletion) { done <- c }})
	server.Start()
	defer server.Close()
	// Configure through the very same product management HTTP entry.
	h := handler(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store)
	cloudHTTPBind(t, h, "http://127.0.0.1:1", "chat", "codex", "unused-key")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Announce a longer body, then hold it open: the client never finishes sending.
	_, err = fmt.Fprintf(conn, "POST /app/codex/v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", address)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 504 {
		t.Fatalf("slow body status=%d", response.StatusCode)
	}
	select {
	case c := <-done:
		if c.Result.Attempts != 0 || c.ErrorCode != "budget_exhausted" || c.Stage != "failed" {
			t.Fatalf("slow body dispatched: %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("body reader outlived budget")
	}
}

func TestCloudHTTPBlockedClientWriteSharesDeadline(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	completed := make(chan ModelCompletion, 1)
	product := httptest.NewUnstartedServer(nil)
	address := product.Listener.Addr().String()
	product.Config.Handler = handlerWithModelOptions(Info{Address: address}, cloudHTTPManagement, store, ModelOptions{Budget: time.Second, Complete: func(c ModelCompletion) { completed <- c }})
	product.Start()
	defer product.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		packet := `data: {"id":"chunk","choices":[{"index":0,"delta":{"content":"` + strings.Repeat("x", 128<<10) + `"}}]}` + "\n\n"
		for range 64 {
			if _, err := io.WriteString(w, packet); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	h := handler(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store)
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "write-deadline-key")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"model":"profile-pinned","messages":[{"role":"user","content":"hello"}],"stream":true}`
	_, err = fmt.Fprintf(conn, "POST /app/codex/v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", address, len(body), body)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the socket open and deliberately never read its response. A client
	// disconnect is not the trigger; the shared operation deadline must end it.
	select {
	case c := <-completed:
		if c.Result.Attempts != 1 || !c.Result.OutputCommitted || c.ErrorCode != "cloud_timeout" || c.Stage != "failed" {
			t.Fatalf("blocked write completion: %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("downstream socket write outlived total budget")
	}
}
