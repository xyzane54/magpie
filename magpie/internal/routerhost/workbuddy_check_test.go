package routerhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func workBuddyCheckOptions(t *testing.T, origin string) Options {
	t.Helper()
	return Options{Host: "workbuddy", ConfigRoot: filepath.Join(t.TempDir(), "host"), RouterURL: origin, Model: "fixture-model"}
}

func workBuddyCheckReply(model string) string {
	return `{"id":"chatcmpl-fixture","object":"chat.completion","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`
}

func TestWorkBuddyCheckerUsesInstalledChatPathAndOnlyManagementAuth(t *testing.T) {
	const management = "management-fixture-only-for-apps"
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("x-api-key") != "" {
			t.Error("checker sent a model API key")
		}
		if r.URL.Path != "/api/apps" && r.Header.Get("Authorization") != "" {
			t.Error("management token escaped its management endpoint")
		}
		switch r.URL.Path {
		case "/healthz":
			if r.Method != http.MethodGet {
				t.Error("health is not read-only")
			}
			io.WriteString(w, `{"service":"api-router","status":"ok"}`)
		case "/api/apps":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+management {
				t.Error("incorrect management request")
			}
			json.NewEncoder(w).Encode(appProjection("workbuddy", "fixture-model"))
		case "/app/workbuddy/v1/chat/completions":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Error("incorrect business request")
			}
			raw, err := io.ReadAll(r.Body)
			var body struct {
				Model     string
				Stream    *bool
				MaxTokens int `json:"max_tokens"`
				Messages  []struct{ Role, Content, Agent string }
			}
			if err != nil || json.Unmarshal(raw, &body) != nil || body.Model != "fixture-model" || body.Stream == nil || *body.Stream || body.MaxTokens != 128 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || strings.TrimSpace(body.Messages[0].Content) == "" || body.Messages[0].Agent != "connection-check" || strings.Contains(string(raw), management) {
				t.Error("business probe lost its fixed model, bounded text request or secret boundary")
			}
			io.WriteString(w, workBuddyCheckReply("fixture-model"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := ProductionWorkBuddyChecker(management).Check(context.Background(), workBuddyCheckOptions(t, server.URL+"/")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(paths, ",")
	mu.Unlock()
	if got != "/healthz,/api/apps,/app/workbuddy/v1/chat/completions" {
		t.Fatalf("wrong validation sequence: %s", got)
	}
}

func TestWorkBuddyCheckerRejectsUnsafeOptionsBeforeRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, tc := range []struct {
		name, token string
		mutate      func(*Options)
	}{
		{name: "other-host", token: "fixture", mutate: func(o *Options) { o.Host = "codex" }},
		{name: "empty-model", token: "fixture", mutate: func(o *Options) { o.Model = " " }},
		{name: "header-model", token: "fixture", mutate: func(o *Options) { o.Model = "model\nsecret" }},
		{name: "relative-config-root", token: "fixture", mutate: func(o *Options) { o.ConfigRoot = "relative" }},
		{name: "remote-origin", token: "fixture", mutate: func(o *Options) { o.RouterURL = "http://example.invalid:8317" }},
		{name: "hostname-origin", token: "fixture", mutate: func(o *Options) { o.RouterURL = "http://localhost:8317" }},
		{name: "tls-origin", token: "fixture", mutate: func(o *Options) { o.RouterURL = "https://127.0.0.1:8317" }},
		{name: "origin-credentials", token: "fixture", mutate: func(o *Options) { o.RouterURL = "http://private:secret@127.0.0.1:8317" }},
		{name: "origin-path", token: "fixture", mutate: func(o *Options) { o.RouterURL += "/app/workbuddy/v1" }},
		{name: "origin-query", token: "fixture", mutate: func(o *Options) { o.RouterURL += "?secret=private" }},
		{name: "origin-fragment", token: "fixture", mutate: func(o *Options) { o.RouterURL += "#private" }},
		{name: "empty-management"},
		{name: "invalid-management", token: "fixture\r\nprivate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := workBuddyCheckOptions(t, server.URL)
			if tc.mutate != nil {
				tc.mutate(&o)
			}
			if err := ProductionWorkBuddyChecker(tc.token).Check(context.Background(), o); err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe options accepted or exposed: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid options reached the network")
	}
}

func TestWorkBuddyCheckerRejectsIdentityBindingAndBusinessFailures(t *testing.T) {
	const secret = "fixture-sensitive-response-never-echo"
	for _, tc := range []struct {
		name, health, apps, response string
		mutate                       func(map[string]any)
		appStatus, modelStatus       int
		wantModelCalls               int32
	}{
		{name: "wrong-service", health: `{"service":"magpie","status":"ok"}`},
		{name: "unhealthy-router", health: `{"service":"api-router","status":"error"}`},
		{name: "duplicate-health", health: `{"service":"other","service":"api-router","status":"ok"}`},
		{name: "wrong-app-schema", mutate: func(p map[string]any) { p["schemaVersion"] = 2 }},
		{name: "app-api-not-supported", appStatus: http.StatusNotFound},
		{name: "management-rejected", appStatus: http.StatusUnauthorized},
		{name: "duplicate-app-schema", apps: `{"schemaVersion":2,"schemaVersion":1,"apps":[]}`},
		{name: "missing-workbuddy", mutate: func(p map[string]any) { p["apps"] = appProjection("codex", "fixture-model")["apps"] }},
		{name: "duplicate-app-id", mutate: func(p map[string]any) { p["apps"] = append(p["apps"].([]any), p["apps"].([]any)[0]) }},
		{name: "empty-app-id", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["id"] = "" }},
		{name: "wrong-entry-path", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["path"] = "/app/codex/v1" }},
		{name: "unconfigured", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["configured"] = false }},
		{name: "app-revision-zero", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["revision"] = 0 }},
		{name: "wrong-credential-mode", mutate: func(p map[string]any) {
			p["apps"].([]any)[0].(map[string]any)["credentialMode"] = "forward-authorization"
		}},
		{name: "missing-provider", mutate: func(p map[string]any) {
			p["apps"].([]any)[0].(map[string]any)["cloud"].(map[string]any)["providerId"] = ""
		}},
		{name: "different-target-model", mutate: func(p map[string]any) {
			p["apps"].([]any)[0].(map[string]any)["cloud"].(map[string]any)["model"] = "other-model"
		}},
		{name: "model-unauthorized", modelStatus: http.StatusUnauthorized, wantModelCalls: 1},
		{name: "model-incompatible-agent", modelStatus: http.StatusBadRequest, response: `{"error":{"code":"capability_message_agent","message":"` + secret + `"}}`, wantModelCalls: 1},
		{name: "model-unwired", modelStatus: http.StatusNotImplemented, wantModelCalls: 1},
		{name: "health-only-not-business", response: `{"service":"api-router","status":"ok"}`, wantModelCalls: 1},
		{name: "empty-200", response: " ", wantModelCalls: 1},
		{name: "malformed-200", response: `{private`, wantModelCalls: 1},
		{name: "wrong-business-model", response: workBuddyCheckReply("other-model"), wantModelCalls: 1},
		{name: "nonterminal-business", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"stop"`, `"length"`, 1), wantModelCalls: 1},
		{name: "empty-business-text", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"OK"`, `" "`, 1), wantModelCalls: 1},
		{name: "wrong-business-role", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"assistant"`, `"user"`, 1), wantModelCalls: 1},
		{name: "wrong-business-object", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"chat.completion"`, `"response"`, 1), wantModelCalls: 1},
		{name: "business-error-envelope", response: `{"error":{"message":"` + secret + `"}}`, wantModelCalls: 1},
		{name: "business-tool-instead-of-text", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"content":"OK"`, `"content":"OK","tool_calls":[{"id":"call_fixture","type":"function","function":{"name":"run","arguments":"{}"}}]`, 1), wantModelCalls: 1},
		{name: "multiple-business-choices", response: `{"object":"chat.completion","model":"fixture-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"OK"}},{"index":1,"finish_reason":"length","message":{"role":"assistant","content":"unfinished"}}]}`, wantModelCalls: 1},
		{name: "duplicate-business-model", response: strings.Replace(workBuddyCheckReply("fixture-model"), `"model":`, `"model":"other-model","model":`, 1), wantModelCalls: 1},
		{name: "oversized-health", health: strings.Repeat("x", (1<<20)+1)},
		{name: "oversized-apps", apps: strings.Repeat("x", (1<<20)+1)},
		{name: "oversized-business", response: strings.Repeat("x", (1<<20)+1), wantModelCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/healthz":
					health := tc.health
					if health == "" {
						health = `{"service":"api-router","status":"ok"}`
					}
					io.WriteString(w, health)
				case "/api/apps":
					if tc.appStatus != 0 {
						w.WriteHeader(tc.appStatus)
						io.WriteString(w, secret)
						return
					}
					if tc.apps != "" {
						io.WriteString(w, tc.apps)
						return
					}
					p := appProjection("workbuddy", "fixture-model")
					if tc.mutate != nil {
						tc.mutate(p)
					}
					json.NewEncoder(w).Encode(p)
				case "/app/workbuddy/v1/chat/completions":
					modelCalls.Add(1)
					if tc.modelStatus != 0 {
						w.WriteHeader(tc.modelStatus)
						if tc.response != "" {
							io.WriteString(w, tc.response)
						} else {
							io.WriteString(w, secret)
						}
						return
					}
					io.WriteString(w, tc.response)
				default:
					t.Errorf("wrong app/protocol reached %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			err := ProductionWorkBuddyChecker("fixture-management").Check(context.Background(), workBuddyCheckOptions(t, server.URL))
			var safe safeCheckError
			if err == nil || !errors.As(err, &safe) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "fixture-management") || strings.Contains(err.Error(), "private") {
				t.Fatalf("failure accepted or disclosed sensitive details: %v", err)
			}
			if got := modelCalls.Load(); got != tc.wantModelCalls {
				t.Fatalf("business calls=%d, want=%d", got, tc.wantModelCalls)
			}
		})
	}
}

func TestWorkBuddyCheckerNeverFollowsRedirects(t *testing.T) {
	for _, redirectPath := range []string{"/healthz", "/api/apps", "/app/workbuddy/v1/chat/completions"} {
		t.Run(redirectPath, func(t *testing.T) {
			var leaked atomic.Int32
			redirect := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
			defer redirect.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == redirectPath {
					http.Redirect(w, r, redirect.URL+"/private", http.StatusTemporaryRedirect)
					return
				}
				if r.URL.Path == "/healthz" {
					io.WriteString(w, `{"service":"api-router","status":"ok"}`)
				} else if r.URL.Path == "/api/apps" {
					json.NewEncoder(w).Encode(appProjection("workbuddy", "fixture-model"))
				} else {
					io.WriteString(w, workBuddyCheckReply("fixture-model"))
				}
			}))
			defer server.Close()
			if err := ProductionWorkBuddyChecker("fixture-management").Check(context.Background(), workBuddyCheckOptions(t, server.URL)); err == nil {
				t.Fatal("redirect accepted as successful check")
			}
			if leaked.Load() != 0 {
				t.Fatal("redirect destination received a check or credential")
			}
		})
	}
}

func TestWorkBuddyCheckerCancelsBusinessBodyRead(t *testing.T) {
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			io.WriteString(w, `{"service":"api-router","status":"ok"}`)
		case "/api/apps":
			json.NewEncoder(w).Encode(appProjection("workbuddy", "fixture-model"))
		default:
			io.WriteString(w, `{"private":"fixture-sensitive-incomplete`)
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(upstreamCanceled)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := workBuddyCheckOptions(t, server.URL)
	done := make(chan error, 1)
	go func() { done <- ProductionWorkBuddyChecker("fixture-management").Check(ctx, o) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("business probe never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "fixture-sensitive") {
			t.Fatalf("canceled body accepted or leaked: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not stop the response reader")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not close the business connection")
	}
}
