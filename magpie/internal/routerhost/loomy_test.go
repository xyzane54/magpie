package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

func loomySetup(t *testing.T) (Engine, Options) {
	t.Helper()
	root := t.TempDir()
	return Engine{StateDir: filepath.Join(root, "recovery"), Adapter: LoomyAdapter{},
			Checker: checkerFunc(func(context.Context, Options) error { return nil })}, Options{
			Host: "loomy", ConfigRoot: filepath.Join(root, "opencode"), RouterURL: "http://127.0.0.1:14387", Model: "vendor/model.v1"}
}

func TestLoomyInstalledContractAndPreviewIsolation(t *testing.T) {
	e, o := loomySetup(t)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(p.Summary())
	if err != nil {
		t.Fatal(err)
	}
	var summary struct{ RouterURL, RequestURL, CredentialSource string }
	if json.Unmarshal(public, &summary) != nil || summary.RouterURL != "http://127.0.0.1:14387/app/loomy/v1" || summary.RequestURL != summary.RouterURL+"/chat/completions" || !strings.Contains(summary.CredentialSource, "useSessionAuth:false") {
		t.Fatal("Loomy preview does not describe its actual endpoint and credential mode")
	}
	if _, err := os.Stat(o.ConfigRoot); !os.IsNotExist(err) {
		t.Fatal("preview created host directory", err)
	}
	if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
		t.Fatal("preview created recovery directory", err)
	}
	doc, err := readJSON(p.files[0].After)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "small_model"} {
		if doc[key] != "api_router/vendor/model.v1" {
			t.Fatalf("wrong installed model selector %s: %v", key, doc[key])
		}
	}
	for _, want := range []struct {
		path  []string
		value any
	}{
		{[]string{"provider", "api_router", "npm"}, "@ai-sdk/openai-compatible"},
		{[]string{"provider", "api_router", "name"}, "API Router"},
		{[]string{"provider", "api_router", "options", "baseURL"}, "http://127.0.0.1:14387/app/loomy/v1"},
		{[]string{"provider", "api_router", "options", "apiKey"}, "api-router-local-placeholder"},
		{[]string{"provider", "api_router", "options", "useSessionAuth"}, false},
		{[]string{"provider", "api_router", "models", "vendor/model.v1", "name"}, "vendor/model.v1"},
		{[]string{"provider", "api_router", "models", "vendor/model.v1", "tool_call"}, true},
	} {
		if got, exists := lookup(doc, want.path); !exists || got != want.value {
			t.Fatalf("installed provider field %v = %v, want %v", want.path, got, want.value)
		}
	}
	if strings.Contains(string(p.files[0].After), "imodel") || strings.Contains(string(p.files[0].After), "session-secret") {
		t.Fatal("adapter touched a template provider or imported a session")
	}
	if err := e.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(get(t, filepath.Join(o.ConfigRoot, "opencode.json")), p.files[0].After) {
		t.Fatal("applied bytes differ from preview")
	}
}

func TestLoomyFirstBackupIdempotenceAndOfflineRestore(t *testing.T) {
	for _, existed := range []bool{false, true} {
		t.Run(fmt.Sprint(existed), func(t *testing.T) {
			e, o := loomySetup(t)
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			original := []byte("{\r\n  // keep my local config\r\n  \"model\":\"imodel/original\",\r\n  \"provider\":{\"imodel\":{\"options\":{\"apiKey\":\"private-original-secret\"}}},\r\n  \"theme\":\"system\"\r\n}\r\n")
			if existed {
				put(t, path, original)
			}
			mustApply(t, e, o)
			record := get(t, e.recordPath(o.Host, o.ConfigRoot))
			applied := get(t, path)
			mustApply(t, e, o)
			if !bytes.Equal(record, get(t, e.recordPath(o.Host, o.ConfigRoot))) || !bytes.Equal(applied, get(t, path)) {
				t.Fatal("repeat apply overwrote first backup or changed host bytes")
			}
			current, err := edit.PatchJSON(applied, []edit.KV{{Path: "theme", Value: "dark"},
				{Path: "provider.api_router.options.timeout", Value: 1200000},
				{Path: "provider.api_router.options.chunkTimeout", Value: 1200000},
				{Path: "provider.api_router.models.vendor/model\\.v1.limit.context", Value: 8192},
				{Path: "provider.api_router.localNote", Value: "keep"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			// A later comment inside a tool-created provider survives its cleanup.
			current = bytes.Replace(current, []byte(`"localNote"`), []byte("// later provider note\n\"localNote\""), 1)
			put(t, path, current)
			mustApply(t, e, o)
			if !bytes.Equal(record, get(t, e.recordPath(o.Host, o.ConfigRoot))) || !bytes.Equal(current, get(t, path)) {
				t.Fatal("repeat apply overwrote backup or unrelated changes")
			}
			e.Checker = nil // independent recovery has no HTTP or running Router.
			mustRestore(t, e, o)
			restored := get(t, path)
			doc, err := readJSON(restored)
			if err != nil {
				t.Fatal(err)
			}
			if doc["theme"] != "dark" || !bytes.Contains(restored, []byte("later provider note")) {
				t.Fatal("unrelated edits or comments were lost", string(restored))
			}
			for _, p := range [][]string{{"provider", loomyProvider, "localNote"}, {"provider", loomyProvider, "models", o.Model, "limit", "context"}} {
				if _, exists := lookup(doc, p); !exists {
					t.Fatal("later provider/model field was removed", p)
				}
			}
			for _, key := range []string{"baseURL", "apiKey", "useSessionAuth"} {
				if _, exists := lookup(doc, []string{"provider", loomyProvider, "options", key}); exists {
					t.Fatal("tool-created auth option remains after restore", key)
				}
			}
			for _, key := range []string{"timeout", "chunkTimeout"} {
				if got, exists := lookup(doc, []string{"provider", loomyProvider, "options", key}); !exists || got != json.Number("1200000") {
					t.Fatal("host-added timeout was not preserved", key)
				}
			}
			if existed {
				if doc["model"] != "imodel/original" || !bytes.Contains(restored, []byte("private-original-secret")) || !bytes.Contains(restored, []byte("keep my local config")) {
					t.Fatal("original selections/provider data did not survive")
				}
			} else if _, exists := doc["model"]; exists {
				t.Fatal("tool-created model selector remains")
			}
			mustRestore(t, e, o)
			if !bytes.Equal(restored, get(t, path)) {
				t.Fatal("repeat recovery modified user's restored config")
			}
		})
	}
}

func TestLoomySameFieldConflictAndStalePlan(t *testing.T) {
	for _, field := range []string{"model", "small_model", "provider.api_router.options.baseURL", "provider.api_router.options.apiKey", "provider.api_router.options.useSessionAuth", "provider.api_router.models.vendor/model\\.v1.tool_call", "provider.api_router.options"} {
		t.Run(field, func(t *testing.T) {
			e, o := loomySetup(t)
			mustApply(t, e, o)
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			value := any("user-conflict-secret")
			if strings.HasSuffix(field, "useSessionAuth") {
				value = true
			} else if strings.HasSuffix(field, "tool_call") {
				value = false
			}
			changed, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: field, Value: value}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			put(t, path, changed)
			if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil || strings.Contains(err.Error(), "user-conflict-secret") {
				t.Fatal("same-field conflict accepted or value disclosed", err)
			}
			if !bytes.Equal(changed, get(t, path)) {
				t.Fatal("conflict wrote host file")
			}
		})
	}
	e, o := loomySetup(t)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.ConfigRoot, "opencode.json")
	put(t, path, []byte(`{"theme":"changed-after-preview"}`))
	if err := e.Apply(context.Background(), p); err == nil {
		t.Fatal("stale preview accepted")
	}
	if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
		t.Fatal("stale plan created backup", err)
	}
}

func TestLoomyMalformedAndReservedProviderConflicts(t *testing.T) {
	for _, raw := range []string{"", `{"provider":false}`, `{"model":false}`, `{"small_model":{}}`, `{"model":"one","model":"two"}`, `{"provider":{"api_router":{"npm":"other-sdk","options":{"apiKey":"secret"}}}}`} {
		t.Run(fmt.Sprint(len(raw)), func(t *testing.T) {
			e, o := loomySetup(t)
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			put(t, path, []byte(raw))
			if _, err := e.Preview(o); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid/conflicting config accepted or disclosed", err)
			}
			if got := get(t, path); string(got) != raw {
				t.Fatal("invalid preview wrote source")
			}
		})
	}
	for _, field := range []string{"provider.api_router.options.headers.Authorization", "provider.api_router.options.useSessionAuth", "provider.api_router.options.baseURL", "provider.api_router.api", "provider.api_router.models.vendor/model\\.v1.options.headers.Authorization", "provider.api_router.models.vendor/model\\.v1.api.url"} {
		t.Run(field, func(t *testing.T) {
			e, o := loomySetup(t)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			v := any("private-conflicting-override")
			if strings.HasSuffix(field, "useSessionAuth") {
				v = true
			}
			raw, err := edit.PatchJSON(p.files[0].After, []edit.KV{{Path: field, Value: v}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(o.ConfigRoot, "opencode.json"), raw)
			if _, err := e.Preview(o); err == nil || strings.Contains(err.Error(), "private-conflicting-override") {
				t.Fatal("reserved provider override accepted or disclosed", err)
			}
		})
	}
}

const loomyTextReply = `{"object":"chat.completion","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}]}`

func TestLoomyModelWireTargetOverridesAreRejected(t *testing.T) {
	for _, override := range []struct {
		name  string
		value any
	}{
		{"id", "other-wire-model"},
		{"id", "vendor/model.v1"},
		{"provider", map[string]any{"npm": "@ai-sdk/anthropic", "api": "https://different-target.invalid"}},
	} {
		t.Run(override.name+fmt.Sprint(override.value), func(t *testing.T) {
			e, o := loomySetup(t)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := edit.PatchJSON(p.files[0].After, []edit.KV{{
				Path: "provider.api_router.models.vendor/model\\.v1." + override.name, Value: override.value,
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			put(t, path, raw)
			if _, err := e.Preview(o); err == nil {
				t.Fatal("model-level wire target override accepted")
			}
			if !bytes.Equal(raw, get(t, path)) {
				t.Fatal("rejected override changed original configuration")
			}
			if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
				t.Fatal("rejected override created recovery data", err)
			}
		})
	}
}

func TestLoomyInstalledProviderAndModelExclusionsArePreserved(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		model     string
		allowed   bool
	}{
		{"disabled-selected", `{"disabled_providers":["api_router"]}`, "vendor/model.v1", false},
		{"disabled-other", `{"disabled_providers":["imodel"]}`, "vendor/model.v1", true},
		{"enabled-empty", `{"enabled_providers":[]}`, "vendor/model.v1", false},
		{"enabled-other", `{"enabled_providers":["imodel"]}`, "vendor/model.v1", false},
		{"enabled-selected", `{"enabled_providers":["api_router","imodel"]}`, "vendor/model.v1", true},
		{"disabled-wins", `{"enabled_providers":["api_router"],"disabled_providers":["api_router"]}`, "vendor/model.v1", false},
		{"invalid-enabled", `{"enabled_providers":true}`, "vendor/model.v1", false},
		{"invalid-disabled", `{"disabled_providers":[false]}`, "vendor/model.v1", false},
		{"removed-model", `{}`, "gpt-5-chat-latest", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, o := loomySetup(t)
			o.Model = tc.model
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			put(t, path, []byte(tc.raw))
			p, err := e.Preview(o)
			if (err == nil) != tc.allowed {
				t.Fatalf("provider/model exclusion allowed=%v: %v", tc.allowed, err)
			}
			if !bytes.Equal([]byte(tc.raw), get(t, path)) {
				t.Fatal("preview modified provider filters")
			}
			if tc.allowed {
				before, _ := readJSON([]byte(tc.raw))
				after, _ := readJSON(p.files[0].After)
				for _, name := range []string{"disabled_providers", "enabled_providers"} {
					b, bok := before[name]
					a, aok := after[name]
					if !equalValue(b, bok, a, aok) {
						t.Fatal("provider filter was silently changed")
					}
				}
			}
		})
	}
	for _, status := range []string{"alpha", "deprecated"} {
		t.Run(status, func(t *testing.T) {
			e, o := loomySetup(t)
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := edit.PatchJSON(p.files[0].After, []edit.KV{{Path: "provider.api_router.models.vendor/model\\.v1.status", Value: status}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(o.ConfigRoot, "opencode.json")
			put(t, path, raw)
			if _, err := e.Preview(o); err == nil {
				t.Fatal("excluded model status accepted")
			}
			if !bytes.Equal(raw, get(t, path)) {
				t.Fatal("model exclusion was silently cleared")
			}
		})
	}
}

func TestLoomyCheckerPathsCredentialsAndStoredBoundary(t *testing.T) {
	for _, mode := range []string{"stored", "forward-authorization", "session"} {
		t.Run(mode, func(t *testing.T) {
			var paths []string
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.Header.Get("x-api-key") != "" || r.URL.Query().Encode() != "" {
					t.Error("unexpected business credential or query")
				}
				switch r.URL.Path {
				case "/healthz":
					if r.Header.Get("Authorization") != "" {
						t.Error("management credential sent to health")
					}
					fmt.Fprint(w, `{"service":"api-router","status":"ok"}`)
				case "/api/apps":
					if r.Header.Get("Authorization") != "Bearer private-management-only" {
						t.Error("missing explicit management credential")
					}
					fmt.Fprintf(w, `{"schemaVersion":1,"apps":[{"schemaVersion":1,"revision":2,"id":"loomy","path":"/app/loomy/v1","configured":true,"credentialMode":%q,"cloud":{"providerId":"provider-a","model":"vendor/model.v1"}}]}`, mode)
				case "/app/loomy/v1/chat/completions":
					if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
						t.Error("business probe credential leak or wrong content type")
					}
					var body struct {
						Model     string `json:"model"`
						Stream    bool   `json:"stream"`
						MaxTokens int    `json:"max_tokens"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "vendor/model.v1" || body.Stream || body.MaxTokens != 128 {
						t.Error("incorrect installed protocol probe")
					}
					fmt.Fprint(w, loomyTextReply)
				default:
					t.Error("unexpected path", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			_, o := loomySetup(t)
			o.RouterURL = server.URL
			err := LoomyChecker("private-management-only").Check(context.Background(), o)
			mu.Lock()
			defer mu.Unlock()
			if mode == "stored" {
				if err != nil || strings.Join(paths, ",") != "/healthz,/api/apps,/app/loomy/v1/chat/completions" {
					t.Fatal("stored probe failed", paths, err)
				}
			} else if err == nil || len(paths) != 2 {
				t.Fatal("session mode reached automatic business probe", paths, err)
			}
		})
	}
}

func TestLoomyCheckerFailureLeavesHostAndBackupUntouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"service":"other","status":"ok","private":"never-echo-secret"}`)
	}))
	defer server.Close()
	e, o := loomySetup(t)
	o.RouterURL = server.URL
	e.Checker = LoomyChecker("management")
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), p); err == nil || strings.Contains(err.Error(), "never-echo-secret") {
		t.Fatal("failed check accepted or disclosed response", err)
	}
	for _, path := range []string{o.ConfigRoot, e.StateDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("failed check wrote host or backup", path, err)
		}
	}
}

func TestLoomyProbeRequiresCompletedText(t *testing.T) {
	if !validLoomyProbe([]byte(loomyTextReply)) {
		t.Fatal("completed reply rejected")
	}
	for _, raw := range []string{`{}`, `{"object":"chat.completion","choices":[]}`, strings.Replace(loomyTextReply, `"OK"`, `" "`, 1), strings.Replace(loomyTextReply, `"stop"`, `"length"`, 1), strings.Replace(loomyTextReply, `"assistant"`, `"user"`, 1), strings.Replace(loomyTextReply, `"object"`, `"error":{"message":"private-secret"},"object"`, 1)} {
		if validLoomyProbe([]byte(raw)) {
			t.Fatal("empty/incomplete/error reply accepted", raw)
		}
	}
}
