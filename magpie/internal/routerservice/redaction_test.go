package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestRedactionManagementRequiresTokenAndCannotClaimAvailability(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "redaction-management-test-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)
	for _, method := range []string{"GET", "HEAD", "POST"} {
		if w := providerRequest(t, h, method, "/api/redaction", "", nil); w.Code != 401 {
			t.Fatalf("unprotected %s: %d", method, w.Code)
		}
	}
	w := providerRequest(t, h, "GET", "/api/redaction", token, nil)
	var status struct {
		Capability             string
		Reason                 string
		ProductionIntegrated   bool
		LocalExecutionRedacted bool
		StatsAvailable         bool
		Statistics             any
		Intents                []redactionIntent
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Intents) != 6 || status.Capability != "unavailable" || status.Reason != "recognition_acceptance_pending" || status.ProductionIntegrated || status.LocalExecutionRedacted || status.StatsAvailable || status.Statistics != nil {
		t.Fatal("false capability or fake statistics", w.Code, w.Body.String())
	}
	for _, intent := range status.Intents {
		if intent.Enabled || intent.Active {
			t.Fatal("default intent must be off and inactive")
		}
	}
	if w := providerRequest(t, h, "POST", "/api/redaction", token, map[string]any{"edits": []any{map[string]any{"id": "codex", "revision": 0, "enabled": true}}}); w.Code != 409 || !strings.Contains(w.Body.String(), "redaction_unavailable") {
		t.Fatal("new enable bypassed unavailable capability", w.Code, w.Body.String())
	}
	for _, input := range []any{
		map[string]any{"edits": []any{map[string]any{"id": "codex", "revision": 0}}},
		map[string]any{"edits": []any{}, "capability": "available"},
		map[string]any{"edits": []any{}, "statsAvailable": true},
	} {
		if w := providerRequest(t, h, "POST", "/api/redaction", token, input); w.Code != 400 {
			t.Fatal("invalid client authority/input accepted", w.Code)
		}
	}
	for path, code := range map[string]int{"/api/%72edaction": 404, "/api/redaction?token=" + token: 400} {
		if w := providerRequest(t, h, "GET", path, token, nil); w.Code != code || strings.Contains(w.Body.String(), token) {
			t.Fatal("alias/query was accepted or secret echoed", path, w.Code)
		}
	}
	if w := providerRequest(t, h, "DELETE", "/api/redaction", token, nil); w.Code != 405 {
		t.Fatal("unsupported redaction method accepted", w.Code)
	}
	if w := providerRequest(t, h, "HEAD", "/api/redaction", token, nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD returned content", w.Code)
	}
	for _, origin := range []string{"null", "https://foreign.invalid"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:13145/api/redaction", nil)
		r.RemoteAddr = "127.0.0.1:2345"
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("foreign origin accepted", w.Code)
		}
	}
}

func TestRedactionManagementRetainsAndDisablesExistingIntent(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	saved, err := store.SetRedactionIntentsContext(context.Background(), []routerconfig.RedactionIntentEdit{{ID: "codex", Enabled: true}, {ID: "dsh", Enabled: true}}, routerconfig.RedactionAvailable)
	if err != nil {
		t.Fatal(err)
	}
	h := handler(Info{Address: "127.0.0.1:13145"}, "test-token", store)
	w := providerRequest(t, h, "GET", "/api/redaction", "test-token", nil)
	var result struct{ Intents []redactionIntent }
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	on := 0
	for _, intent := range result.Intents {
		if intent.Enabled {
			on++
		}
		if intent.Active {
			t.Fatal("saved intent claimed active protection")
		}
	}
	if on != 2 {
		t.Fatal("read erased saved intent")
	}
	edits := []routerconfig.RedactionIntentEdit{{ID: "codex", Revision: saved[0].Revision, Enabled: false}, {ID: "dsh", Enabled: false}}
	if w := providerRequest(t, h, "POST", "/api/redaction", "test-token", map[string]any{"edits": edits}); w.Code != 409 {
		t.Fatal("stale batch accepted", w.Code)
	}
	edits[1].Revision = saved[1].Revision
	if w := providerRequest(t, h, "POST", "/api/redaction", "test-token", map[string]any{"edits": edits}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	apps, _ := store.Apps()
	for _, app := range apps {
		if app.RedactionEnabled {
			t.Fatal("batch disable did not persist")
		}
	}
	w = providerRequest(t, h, "GET", "/api/apps", "test-token", nil)
	if !strings.Contains(w.Body.String(), `"redaction":false`) {
		t.Fatal("engineering module prematurely unlocked production capability")
	}
}

func TestRedactionManagementProjectsOnlySafeAggregateStats(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const secret = "private-source-and-mapping-must-not-be-returned"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/redaction", nil)
	redactionAPI(w, r, store, func() routerredact.Stats {
		return routerredact.Stats{Processed: 9, Entities: 4, CacheHits: 2, Blocked: 1, DurationMS: 12, Failures: map[string]uint64{secret: 1}}
	})
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"processed":9`) || !strings.Contains(w.Body.String(), `"statsAvailable":true`) || !strings.Contains(w.Body.String(), `"redaction_failed":1`) {
		t.Fatal("unsafe/unavailable aggregate projection", w.Code, w.Body.String())
	}
}

func TestRedactionMultiTurnSessionCacheHitAndIncrementalExtraction(t *testing.T) {
	layout, address := fixture(t)
	store := routerconfig.New(layout)
	const modelID = "spark-x2.5-1.7B"

	var extractedBatches [][]string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &request)
		content := `{"decision":"cloud"}`
		if len(request.Messages) > 1 {
			var classify struct {
				Fields []struct {
					Field int    `json:"field"`
					Text  string `json:"text"`
				} `json:"fields"`
			}
			if json.Unmarshal([]byte(request.Messages[1].Content), &classify) == nil && len(classify.Fields) > 0 {
				var batch []string
				fields := make([]any, 0, len(classify.Fields))
				for _, field := range classify.Fields {
					batch = append(batch, field.Text)
					findings := []any{}
					if idx := strings.Index(field.Text, "Alice"); idx >= 0 {
						findings = append(findings, map[string]any{"label": "private_person", "start": idx, "end": idx + len("Alice"), "text": "Alice", "confidence": 0.99})
					}
					if idx := strings.Index(field.Text, "Bob"); idx >= 0 {
						findings = append(findings, map[string]any{"label": "private_person", "start": idx, "end": idx + len("Bob"), "text": "Bob", "confidence": 0.99})
					}
					fields = append(fields, map[string]any{"field": field.Field, "complete": true, "findings": findings})
				}
				extractedBatches = append(extractedBatches, batch)
				b, _ := json.Marshal(map[string]any{"complete": true, "fields": fields})
				content = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(content) + `},"finish_reason":"stop"}]}`))
	}))
	defer local.Close()

	var cloudBodies []string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cloudBodies = append(cloudBodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Hello <PERSON:1>"},"finish_reason":"stop"}]}`))
	}))
	defer cloud.Close()

	catalog := routermanifest.Fixed()
	runtimeID := routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH)
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{
		Catalog: catalog, RuntimeID: runtimeID,
		Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: local.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err := manager.Select(context.Background(), modelID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec, err := routerredact.NewPrivacySpecification(routerredact.ModelBinding{ModelID: modelID, RuntimeID: runtimeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []routerruntime.Check{
		{Purpose: routerruntime.Judgment, Suite: "judgment-engineering-v1", Run: func(context.Context, string) (string, error) { return "judgment-fixture", nil }},
		{Purpose: routerruntime.Execution, Suite: "execution-engineering-v1", Run: func(context.Context, string) (string, error) { return "execution-fixture", nil }},
		{Purpose: routerruntime.Redaction, Suite: spec.CapabilitySuite(), Run: func(context.Context, string) (string, error) { return "redaction-fixture", nil }},
	} {
		if err := manager.CheckCapability(context.Background(), check); err != nil {
			t.Fatal(err)
		}
	}

	key := "test-key"
	provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "loopback", Protocol: "chat", BaseURL: cloud.URL + "/v1", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAppWithRedactionCapabilityContext(context.Background(), routerconfig.AppEdit{
		ID: "codex", Name: "Codex", Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "profile-model"}, CredentialMode: routerconfig.StoredCredential, RedactionEnabled: true,
	}, routerconfig.RedactionAvailable); err != nil {
		t.Fatal(err)
	}

	redactor := newRuntimeRedactor(&LocalModels{Manager: manager, Layout: layout, Catalog: catalog}, false)
	defer redactor.close()
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "management-token", store, ModelOptions{
		LocalRuntime: &LocalModels{Manager: manager, Layout: layout, Catalog: catalog},
		Routing:      &RoutingOptions{Judge: RuntimeJudge{Manager: manager, PromptVersion: defaultPromptVersion}, LocalInvoker: manager, Redactor: redactor, PromptVersion: defaultPromptVersion},
	})

	// Turn 1: initial user message
	req1 := httptest.NewRequest(http.MethodPost, "http://"+address+"/app/codex/v1/chat/completions", strings.NewReader(`{"model":"profile-model","messages":[{"role":"user","content":"Contact Alice"}]}`))
	req1.RemoteAddr = "127.0.0.1:2345"
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK || !strings.Contains(w1.Body.String(), "Hello Alice") {
		t.Fatalf("turn 1 failed: status=%d body=%s", w1.Code, w1.Body.String())
	}
	if len(extractedBatches) != 1 || len(extractedBatches[0]) != 1 || extractedBatches[0][0] != "Contact Alice" {
		t.Fatalf("unexpected turn 1 extraction batches: %+v", extractedBatches)
	}

	// Turn 2: history + new user message
	req2 := httptest.NewRequest(http.MethodPost, "http://"+address+"/app/codex/v1/chat/completions", strings.NewReader(`{"model":"profile-model","messages":[{"role":"user","content":"Contact Alice"},{"role":"assistant","content":"Hello Alice"},{"role":"user","content":"Also invite Bob"}]}`))
	req2.RemoteAddr = "127.0.0.1:2345"
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("turn 2 failed: status=%d body=%s", w2.Code, w2.Body.String())
	}
	stats := redactor.Stats()
	if stats.CacheHits < 1 {
		t.Fatalf("expected CacheHits >= 1 on second turn, got %+v", stats)
	}
	if len(extractedBatches) != 2 {
		t.Fatalf("expected 2 extraction batches across 2 turns, got %d: %+v", len(extractedBatches), extractedBatches)
	}
	for _, text := range extractedBatches[1] {
		if text == "Contact Alice" {
			t.Fatalf("expected turn 2 to only extract incremental messages, but re-extracted turn 1 message: %+v", extractedBatches[1])
		}
	}
	if len(cloudBodies) != 2 || strings.Contains(cloudBodies[1], "Alice") || strings.Contains(cloudBodies[1], "Bob") {
		t.Fatalf("expected both historical and incremental entities redacted in turn 2 cloud payload: %+v", cloudBodies)
	}

	// Cross-protocol multi-turn (client uses responses protocol, upstream provider uses chat protocol):
	// Turn 1 (responses -> chat): single input message seeds the session cache
	req3 := httptest.NewRequest(http.MethodPost, "http://"+address+"/app/codex/v1/responses", strings.NewReader(`{"model":"profile-model","input":[{"role":"user","content":"Email Alice"}]}`))
	req3.RemoteAddr = "127.0.0.1:2345"
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK || !strings.Contains(w3.Body.String(), "Hello Alice") {
		t.Fatalf("cross-protocol turn 1 failed: status=%d body=%s", w3.Code, w3.Body.String())
	}

	// Turn 2 (responses -> chat): history + new user message must hit the seeded cache
	req4 := httptest.NewRequest(http.MethodPost, "http://"+address+"/app/codex/v1/responses", strings.NewReader(`{"model":"profile-model","input":[{"role":"user","content":"Email Alice"},{"role":"assistant","content":"Hello Alice"},{"role":"user","content":"CC Bob"}]}`))
	req4.RemoteAddr = "127.0.0.1:2345"
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	h.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("cross-protocol turn 2 failed: status=%d body=%s", w4.Code, w4.Body.String())
	}
	statsAfterCrossProtocol := redactor.Stats()
	if statsAfterCrossProtocol.CacheHits < 2 {
		t.Fatalf("expected CacheHits >= 2 after cross-protocol second turn, got %+v", statsAfterCrossProtocol)
	}
	if len(extractedBatches) != 4 {
		t.Fatalf("expected 4 extraction batches across 4 turns, got %d: %+v", len(extractedBatches), extractedBatches)
	}
	for _, text := range extractedBatches[3] {
		if text == "Email Alice" {
			t.Fatalf("expected cross-protocol turn 2 to only extract incremental messages, but re-extracted turn 1 message: %+v", extractedBatches[3])
		}
	}
	if len(cloudBodies) != 4 || strings.Contains(cloudBodies[3], "Alice") || strings.Contains(cloudBodies[3], "Bob") {
		t.Fatalf("expected both historical and incremental entities redacted in cross-protocol turn 2 cloud payload: %+v", cloudBodies)
	}
}
