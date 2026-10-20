package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestRuntimeRedactorContextReloadRebindsCacheAndPreservesPreparedSnapshot(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions/input_tokens" {
			_, _ = io.WriteString(w, `{"input_tokens":123}`)
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MaxTokens != routerredact.PrivacyMaxOutputTokens {
			t.Error("redaction output cap changed with serving context")
		}
		var fields struct {
			Fields []struct {
				Field int `json:"field"`
			} `json:"fields"`
		}
		if len(body.Messages) < 2 {
			t.Error("privacy material missing")
			w.WriteHeader(500)
			return
		}
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &fields); err != nil {
			t.Error(err)
		}
		complete := make([]any, 0, len(fields.Fields))
		for _, field := range fields.Fields {
			complete = append(complete, map[string]any{"field": field.Field, "complete": true, "findings": []any{}})
		}
		content, _ := json.Marshal(map[string]any{"complete": true, "fields": complete})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	catalog := routermanifest.Fixed()
	runtimeID := routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH)
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: runtimeID, Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: server.URL}, ExperimentalRedaction: true})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err := manager.Select(context.Background(), "spark-x2.5-1.7B"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	redactor := newRuntimeRedactor(&LocalModels{Manager: manager, Layout: layout, Catalog: catalog}, true)
	defer redactor.close()
	oldPipeline, oldSpec, err := redactor.current()
	if err != nil {
		t.Fatal(err)
	}
	oldHandle, err := manager.NewExperimentalRedactionInvoker(oldSpec.RuntimeFingerprint(), oldSpec.CapabilitySuite())
	if err != nil {
		t.Fatal(err)
	}
	input := routerredact.Input{RequestID: "context-fixture", AppID: "context-app", Protocol: routerredact.Chat, Operation: "generation", SessionID: "verified", SessionVerified: true, Versions: oldSpec.Versions(), Body: []byte(`{"model":"fixture-model","messages":[{"role":"user","content":"fixture"}]}`)}
	prepared, err := oldPipeline.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := manager.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
		t.Fatal(err)
	}
	newPipeline, newSpec, err := redactor.current()
	if err != nil {
		t.Fatal(err)
	}
	if newPipeline == oldPipeline || oldSpec.RuntimeFingerprint() == newSpec.RuntimeFingerprint() || len(redactor.retired) != 1 {
		t.Fatal("context reload reused the old redaction cache epoch")
	}
	if newSpec.RuntimeFingerprint() != manager.Identity().ModelFingerprint || newSpec.Budget() != 30*time.Second {
		t.Fatal("new privacy binding or trial budget is incorrect")
	}
	if !errors.Is(oldHandle.VerifyBinding(oldSpec.RuntimeFingerprint(), oldSpec.CapabilitySuite()), routerruntime.ErrCapability) {
		t.Fatal("old experimental handle survived context reload")
	}
	if _, err := manager.NewExperimentalRedactionInvoker(newSpec.RuntimeFingerprint(), newSpec.CapabilitySuite()); err != nil {
		t.Fatal("explicit trial could not bind new context", err)
	}
	if _, err := prepared.Snapshot.RestoreResponse(routerredact.Chat, []byte(`{"choices":[{"message":{"role":"assistant","content":"fixture"}}]}`)); err != nil {
		t.Fatal("prepared snapshot was invalidated by cache epoch change", err)
	}
	input.Versions = newSpec.Versions()
	input.RequestID = "new-context-fixture"
	next, err := newPipeline.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal("new context could not prepare with unchanged privacy material", err)
	}
	next.Close()
}
