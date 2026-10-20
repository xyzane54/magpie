package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudHTTPErrorsExplainSelectedModelWithoutExposingUpstreamData(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"unusable-listed-model", 400, `{"error":{"code":"ModelNotFound","message":"bound-provider-key https://private.invalid/path?token=secret tool arguments user text"}}`, "cloud_model_not_found"},
		{"permission-not-assumed-invalid-key", 403, `{"error":{"code":"Model.AccessDenied","message":"bound-provider-key"}}`, "cloud_permission_error"},
		{"explicit-invalid-key", 403, `{"code":"InvalidApiKey","message":"bound-provider-key"}`, "cloud_auth_error"},
		{"unknown-parameter-failure", 400, `{"error":{"code":"private-arbitrary-code","message":"bound-provider-key"}}`, "cloud_parameter_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			var completion ModelCompletion
			h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { completion = c }})
			cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", cloudHTTPInput("responses", true, false), "", "")
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || completion.ErrorCode != tc.code || completion.Result.Attempts != 1 || calls.Load() != 1 {
				t.Fatalf("status=%d code=%s calls=%d completion=%+v", w.Code, w.Body.String(), calls.Load(), completion)
			}
			encoded, _ := json.Marshal(completion)
			for _, private := range []string{"bound-provider-key", "private.invalid", "tool arguments", "user text", "private-arbitrary-code"} {
				if strings.Contains(w.Body.String(), private) || strings.Contains(string(encoded), private) {
					t.Fatal("private upstream fields entered error or observation")
				}
			}
		})
	}
}

func TestCloudHTTPErrorBodyCannotOutliveSharedBudget(t *testing.T) {
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "10000")
		w.WriteHeader(400)
		io.WriteString(w, `{"error":`)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-time.After(5 * time.Second):
			t.Error("failed upstream body read was not canceled")
		}
	}))
	defer upstream.Close()
	var completion ModelCompletion
	h := cloudHTTPObservedHandler(t, ModelOptions{Budget: 300 * time.Millisecond, Complete: func(c ModelCompletion) { completion = c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
	w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	if w.Code != 504 || completion.ErrorCode != "cloud_timeout" || completion.Result.Attempts != 1 || completion.Result.OutputCommitted {
		t.Fatalf("error-body budget: %d %+v", w.Code, completion)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream error-body read still active")
	}
}
