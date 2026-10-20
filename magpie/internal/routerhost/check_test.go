package routerhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func appProjection(host, model string) map[string]any {
	return map[string]any{"schemaVersion": 1, "apps": []any{map[string]any{
		"schemaVersion": 1, "revision": 1, "id": host, "path": "/app/" + host + "/v1",
		"configured": true, "credentialMode": "stored",
		"cloud": map[string]any{"providerId": "p_fixture", "model": model},
	}}}
}
func probeResponse(host string) string {
	if host == "claude-code" {
		return `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}]}`
	}
	return `{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}]}`
}

func TestProductionCheckerChecksAppsThenInferenceWithoutApplicationCredential(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			_, o := setup(t, host)
			const management = "management-fixture-never-for-models"
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case "/healthz":
					if r.Header.Get("Authorization") != "" {
						t.Error("health received token")
					}
					io.WriteString(w, `{"service":"api-router","status":"ok"}`)
				case "/api/apps":
					if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+management {
						t.Error("wrong management check")
					}
					json.NewEncoder(w).Encode(appProjection(host, o.Model))
				default:
					want := "/app/" + host + "/v1/responses"
					if host == "claude-code" {
						want = "/app/" + host + "/v1/messages"
					}
					if r.Method != "POST" || r.URL.Path != want || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
						t.Error("wrong model check or credential")
					}
					if r.Header.Get("Content-Type") != "application/json" || (host == "claude-code" && r.Header.Get("anthropic-version") != "2023-06-01") {
						t.Error("unauthenticated probe lost protocol headers")
					}
					raw, _ := io.ReadAll(r.Body)
					var body map[string]any
					if json.Unmarshal(raw, &body) != nil || body["model"] != o.Model || strings.Contains(string(raw), management) {
						t.Error("invalid model probe body")
					}
					io.WriteString(w, probeResponse(host))
				}
			}))
			defer server.Close()
			o.RouterURL = server.URL
			if err := ProductionChecker(management).Check(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got := strings.Join(paths, ",")
			mu.Unlock()
			suffix := "/responses"
			if host == "claude-code" {
				suffix = "/messages"
			}
			if got != "/healthz,/api/apps,/app/"+host+"/v1"+suffix {
				t.Fatalf("check sequence: %s", got)
			}
		})
	}
}

func TestProductionCheckerRejectsApplicationAndBusinessFailures(t *testing.T) {
	tests := []struct {
		name                   string
		appStatus, modelStatus int
		mutate                 func(map[string]any)
		response               string
		modelCalls             int
	}{
		{name: "old-service", appStatus: 404},
		{name: "bad-management", appStatus: 401},
		{name: "unsupported-envelope", mutate: func(p map[string]any) { p["schemaVersion"] = 2 }},
		{name: "unregistered", mutate: func(p map[string]any) { p["apps"] = []any{} }},
		{name: "duplicate", mutate: func(p map[string]any) { p["apps"] = append(p["apps"].([]any), p["apps"].([]any)[0]) }},
		{name: "unconfigured", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["configured"] = false }},
		{name: "wrong-mode", mutate: func(p map[string]any) {
			p["apps"].([]any)[0].(map[string]any)["credentialMode"] = "forward-authorization"
		}},
		{name: "wrong-model", mutate: func(p map[string]any) {
			p["apps"].([]any)[0].(map[string]any)["cloud"].(map[string]any)["model"] = "another-model"
		}},
		{name: "wrong-path", mutate: func(p map[string]any) { p["apps"].([]any)[0].(map[string]any)["path"] = "/app/other/v1" }},
		{name: "model-unauthorized", modelStatus: 401, modelCalls: 1},
		{name: "unwired-model", modelStatus: 501, modelCalls: 1},
		{name: "empty-model-success", response: `{"object":"response","status":"completed","output":[null]}`, modelCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, o := setup(t, "codex")
			const management = "management-fixture-secret"
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					io.WriteString(w, `{"service":"api-router","status":"ok"}`)
					return
				}
				if r.URL.Path == "/api/apps" {
					if tc.appStatus != 0 {
						w.WriteHeader(tc.appStatus)
						io.WriteString(w, management+PlaceholderAPIKey)
						return
					}
					p := appProjection(o.Host, o.Model)
					if tc.mutate != nil {
						tc.mutate(p)
					}
					json.NewEncoder(w).Encode(p)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				if tc.modelStatus != 0 {
					w.WriteHeader(tc.modelStatus)
					io.WriteString(w, management+PlaceholderAPIKey)
					return
				}
				io.WriteString(w, tc.response)
			}))
			defer server.Close()
			o.RouterURL = server.URL
			e.Checker = ProductionChecker(management)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			err = e.Apply(context.Background(), p)
			if err == nil || strings.Contains(err.Error(), management) || strings.Contains(err.Error(), PlaceholderAPIKey) {
				t.Fatal("check succeeded or exposed secrets", err)
			}
			mu.Lock()
			got := calls
			mu.Unlock()
			if got != tc.modelCalls {
				t.Fatalf("model calls=%d want=%d", got, tc.modelCalls)
			}
			for _, dir := range []string{o.ConfigRoot, e.StateDir} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("failed checks created host/recovery directories")
				}
			}
		})
	}
}

func TestProductionCheckerLeavesRoomForReasoningBeforeVisibleText(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			_, o := setup(t, host)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/healthz":
					io.WriteString(w, `{"service":"api-router","status":"ok"}`)
				case "/api/apps":
					json.NewEncoder(w).Encode(appProjection(host, o.Model))
				default:
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid model probe")
					}
					key := "max_output_tokens"
					if host == "claude-code" {
						key = "max_tokens"
					}
					budget, _ := body[key].(float64)
					if budget < 128 || budget > 256 {
						// Matches the observed failure: the probe completed no text
						// within its allowance, although ordinary inference worked.
						io.WriteString(w, `{"object":"response","status":"incomplete","output":[]}`)
						return
					}
					io.WriteString(w, probeResponse(host))
				}
			}))
			defer server.Close()
			o.RouterURL = server.URL
			if err := ProductionChecker("fixture-management").Check(context.Background(), o); err != nil {
				t.Fatalf("bounded probe ran out before visible text: %v", err)
			}
		})
	}
}
