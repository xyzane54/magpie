package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func newFastHealthServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var unexpectedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"service":"api-router","status":"ok"}`)
			return
		}
		unexpectedCalls.Add(1)
		http.Error(w, "unexpected probe call", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &unexpectedCalls
}

func TestLoomyMultiModelConnectRetireAndRestore(t *testing.T) {
	srv, unexpected := newFastHealthServer(t)
	root := filepath.Join(t.TempDir(), "loomy")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"model\": \"imodel/default\",\n  \"small_model\": \"imodel/small\",\n  \"provider\": {\n    \"other\": {\n      \"name\": \"Custom Other\"\n    }\n  }\n}\n")
	cfgPath := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	e := Engine{
		StateDir: state,
		Adapter:  LoomyAdapter{},
		Checker:  FastChecker("mgmt-token"),
	}
	firstModels := []string{"group/coding-hybrid", "openai/gpt-4o", "local/qwen2.5-coder-7b"}
	if _, err := e.Connect(context.Background(), Options{
		Host:       "loomy",
		ConfigRoot: root,
		RouterURL:  srv.URL,
		Models:     firstModels,
	}); err != nil {
		t.Fatalf("Connect first multi-model failed: %v", err)
	}
	if unexpected.Load() != 0 {
		t.Fatalf("FastChecker made %d unexpected non-healthz calls", unexpected.Load())
	}

	afterFirst, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(afterFirst, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "api_router/group/coding-hybrid" || doc["small_model"] != "api_router/group/coding-hybrid" {
		t.Fatalf("unexpected primary model in opencode.json: model=%v small_model=%v", doc["model"], doc["small_model"])
	}
	modelsMap := doc["provider"].(map[string]any)["api_router"].(map[string]any)["models"].(map[string]any)
	for _, m := range firstModels {
		if _, ok := modelsMap[m]; !ok {
			t.Fatalf("expected model %q in provider.api_router.models, got %v", m, modelsMap)
		}
	}

	secondModels := []string{"openai/gpt-4o", "local/qwen2.5-coder-7b"}
	if _, err := e.Connect(context.Background(), Options{
		Host:       "loomy",
		ConfigRoot: root,
		RouterURL:  srv.URL,
		Models:     secondModels,
	}); err != nil {
		t.Fatalf("Connect second multi-model subset failed: %v", err)
	}
	afterSecond, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	doc = nil
	if err := json.Unmarshal(afterSecond, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "api_router/openai/gpt-4o" {
		t.Fatalf("expected updated primary model api_router/openai/gpt-4o, got %v", doc["model"])
	}
	modelsMap = doc["provider"].(map[string]any)["api_router"].(map[string]any)["models"].(map[string]any)
	if _, exists := modelsMap["group/coding-hybrid"]; exists {
		t.Fatalf("expected retired model group/coding-hybrid to be removed, got %v", modelsMap)
	}
	for _, m := range secondModels {
		if _, ok := modelsMap[m]; !ok {
			t.Fatalf("expected model %q to remain, got %v", m, modelsMap)
		}
	}

	rp, err := e.PreviewRestore("loomy", root)
	if err != nil {
		t.Fatalf("PreviewRestore failed: %v", err)
	}
	if err := e.Restore(rp); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	restored, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored opencode.json does not match original:\ngot:\n%s\nwant:\n%s", restored, original)
	}
}

func TestWorkBuddyMultiModelConnectRetireAndRestore(t *testing.T) {
	srv, unexpected := newFastHealthServer(t)
	root := filepath.Join(t.TempDir(), "workbuddy")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	origModels := []byte("{\n  \"models\": [\n    {\"id\": \"user-custom\", \"name\": \"User Custom\", \"url\": \"https://api.example.com/v1\"}\n  ]\n}\n")
	origSettings := []byte("{\n  \"model\": \"builtin:default\",\n  \"theme\": \"dark\"\n}\n")
	modelsPath := filepath.Join(root, "models.json")
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(modelsPath, origModels, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, origSettings, 0o600); err != nil {
		t.Fatal(err)
	}

	e := Engine{
		StateDir: state,
		Adapter:  WorkBuddyAdapter{},
		Checker:  FastChecker("mgmt-token"),
	}
	firstModels := []string{"group/coding-hybrid", "openai/gpt-4o", "local/qwen2.5-coder-7b"}
	if _, err := e.Connect(context.Background(), Options{
		Host:       "workbuddy",
		ConfigRoot: root,
		RouterURL:  srv.URL,
		Models:     firstModels,
	}); err != nil {
		t.Fatalf("WorkBuddy Connect first multi-model failed: %v", err)
	}
	if unexpected.Load() != 0 {
		t.Fatalf("FastChecker made %d unexpected non-healthz calls", unexpected.Load())
	}

	checkWorkBuddyFiles := func(wantPrimary string, wantIDs []string, retiredIDs []string) {
		t.Helper()
		rawModels, err := os.ReadFile(modelsPath)
		if err != nil {
			t.Fatal(err)
		}
		var mdoc struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(rawModels, &mdoc); err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, item := range mdoc.Models {
			if id, ok := item["id"].(string); ok {
				found[id] = true
			}
		}
		for _, id := range wantIDs {
			if !found[id] {
				t.Fatalf("expected WorkBuddy models.json to contain %q, got %v", id, found)
			}
		}
		for _, id := range retiredIDs {
			if found[id] {
				t.Fatalf("expected WorkBuddy models.json not to contain retired %q, got %v", id, found)
			}
		}
		rawSettings, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		var sdoc map[string]any
		if err := json.Unmarshal(rawSettings, &sdoc); err != nil {
			t.Fatal(err)
		}
		if sdoc["model"] != "custom-local:"+wantPrimary {
			t.Fatalf("expected settings.json model=%q, got %v", "custom-local:"+wantPrimary, sdoc["model"])
		}
	}

	checkWorkBuddyFiles("group/coding-hybrid", append(firstModels, "user-custom"), nil)

	secondModels := []string{"local/qwen2.5-coder-7b"}
	if _, err := e.Connect(context.Background(), Options{
		Host:       "workbuddy",
		ConfigRoot: root,
		RouterURL:  srv.URL,
		Models:     secondModels,
	}); err != nil {
		t.Fatalf("WorkBuddy Connect second multi-model failed: %v", err)
	}
	checkWorkBuddyFiles("local/qwen2.5-coder-7b", []string{"user-custom", "local/qwen2.5-coder-7b"}, []string{"group/coding-hybrid", "openai/gpt-4o"})

	rp, err := e.PreviewRestore("workbuddy", root)
	if err != nil {
		t.Fatalf("WorkBuddy PreviewRestore failed: %v", err)
	}
	if err := e.Restore(rp); err != nil {
		t.Fatalf("WorkBuddy Restore failed: %v", err)
	}
	restoredModels, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredModels, origModels) {
		t.Fatalf("restored models.json mismatch:\ngot:\n%s\nwant:\n%s", restoredModels, origModels)
	}
	if !bytes.Equal(restoredSettings, origSettings) {
		t.Fatalf("restored settings.json mismatch:\ngot:\n%s\nwant:\n%s", restoredSettings, origSettings)
	}
}
