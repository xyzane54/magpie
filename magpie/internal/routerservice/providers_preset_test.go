package routerservice

import (
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerruntime"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderModelDiscoveryFailureGracefulPresetFallback(t *testing.T) {
	var statusCode atomic.Int32
	statusCode.Store(404)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(statusCode.Load()))
		_, _ = io.WriteString(w, `{"error":"models endpoint unsupported"}`)
	}))
	defer upstream.Close()

	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "management-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)

	in := providerInputFor(upstream.URL + "/v1")
	in.Name = "Qwen Coding Plan"
	in.Preset = "qwen-cn"
	saved := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, in))

	for _, code := range []int32{404, 500} {
		statusCode.Store(code)
		resp := providerRequest(t, h, "POST", "/api/cloud-providers/models?persist=1", token, map[string]any{
			"id":       saved.ID,
			"persist":  true,
			"fallback": true,
		})
		if resp.Code != 200 {
			t.Fatalf("upstream %d with persist/fallback should return 200, got %d: %s", code, resp.Code, resp.Body.String())
		}
		var result providerModelListResult
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatalf("unmarshal fallback response: %v", err)
		}
		if !result.Fallback || result.Source != "preset" || len(result.Models) == 0 {
			t.Fatalf("expected preset fallback on HTTP %d, got %+v", code, result)
		}
	}

	// Also test connection failure when upstream is closed
	upstream.Close()
	connFailResp := providerRequest(t, h, "POST", "/api/cloud-providers/models", token, map[string]any{
		"id":       saved.ID,
		"persist":  true,
		"fallback": true,
	})
	if connFailResp.Code != 200 {
		t.Fatalf("connection failure with fallback should return 200, got %d: %s", connFailResp.Code, connFailResp.Body.String())
	}
	var connResult providerModelListResult
	if err := json.Unmarshal(connFailResp.Body.Bytes(), &connResult); err != nil || !connResult.Fallback || connResult.Source != "preset" || len(connResult.Models) == 0 {
		t.Fatalf("expected preset fallback on connection failure, got %+v (err=%v)", connResult, err)
	}
}

func TestProviderCandidateModelsAggregateLocalModels(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "management-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)

	// Mark spark-x2.5-1.7b as selected & installed in runtime config
	if err := store.Update(func(d routerconfig.Document) error {
		raw, _ := json.Marshal(map[string]any{
			"enabled":      true,
			"selected":     "spark-x2.5-1.7B",
			"installation": "installed",
			"status":       "ready",
		})
		d["runtime"] = raw
		return nil
	}); err != nil {
		t.Fatalf("update runtime config: %v", err)
	}

	list := providerRequest(t, h, "GET", "/api/cloud-providers", token, nil)
	if list.Code != 200 {
		t.Fatalf("GET /api/cloud-providers: %d %s", list.Code, list.Body.String())
	}
	var payload struct {
		LocalModels     []localModelCandidate `json:"localModels"`
		CandidateModels []candidateModelEntry `json:"candidateModels"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal localModels payload: %v", err)
	}
	if len(payload.LocalModels) < 2 {
		t.Fatalf("expected at least 2 local models, got %+v", payload.LocalModels)
	}

	bySelector := map[string]localModelCandidate{}
	for _, lm := range payload.LocalModels {
		bySelector[lm.Selector] = lm
	}
	small, okSmall := bySelector["local/spark-x2.5-1.7b"]
	large, okLarge := bySelector["local/spark-x2.5-4b"]
	if !okSmall || !okLarge {
		t.Fatalf("expected local/spark-x2.5-1.7b and local/spark-x2.5-4b in localModels, got %+v", payload.LocalModels)
	}
	if !small.Installed || small.Status != "ready" {
		t.Fatalf("expected local/spark-x2.5-1.7b installed=true status=ready, got %+v", small)
	}
	if large.Installed || large.Status != "not_installed" {
		t.Fatalf("expected local/spark-x2.5-4b installed=false status=not_installed, got %+v", large)
	}

	// Verify Options.LocalRuntime wired through NewHandler / http.go line 174
	layout2, _ := fixture(t)
	store2 := routerconfig.New(layout2)
	installer := &managedFixtureInstaller{}
	installer.installed.Store(true)
	factory := &managedFixtureFactory{}
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{
		Catalog:   catalog,
		RuntimeID: "windows-amd64-cpu",
		Store:     routerruntime.ConfigIntentStore{Store: store2},
		Installer: installer,
		Factory:   factory,
	})
	if err != nil {
		t.Fatalf("routerruntime.New: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Select(context.Background(), "spark-x2.5-4B"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := manager.EnableSelected(context.Background(), "spark-x2.5-4B"); err != nil {
		t.Fatalf("EnableSelected: %v", err)
	}
	hWithRuntime := handlerWithModelOptions(Info{Service: "api-router", Address: "127.0.0.1:13145"}, token, store2, ModelOptions{
		LocalRuntime: &LocalModels{Manager: manager, Layout: layout2, Catalog: catalog},
	})
	list2 := providerRequest(t, hWithRuntime, "GET", "/api/cloud-providers", token, nil)
	if list2.Code != 200 {
		t.Fatalf("GET /api/cloud-providers with LocalRuntime: %d %s", list2.Code, list2.Body.String())
	}
	var payload2 struct {
		LocalModels []localModelCandidate `json:"localModels"`
	}
	if err := json.Unmarshal(list2.Body.Bytes(), &payload2); err != nil {
		t.Fatalf("unmarshal payload2: %v", err)
	}
	bySelector2 := map[string]localModelCandidate{}
	for _, lm := range payload2.LocalModels {
		bySelector2[lm.Selector] = lm
	}
	large2 := bySelector2["local/spark-x2.5-4b"]
	if !large2.Installed || !large2.Selected || !large2.Enabled {
		t.Fatalf("expected local/spark-x2.5-4b installed=true selected=true enabled=true via Options.LocalRuntime, got %+v", large2)
	}
}

func TestProviderModelFallbackPriorityCachedPresetAndNone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":"upstream down"}`)
	}))
	defer upstream.Close()

	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "management-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)

	// 1. Provider with cached DiscoveredModels should fall back to cached discovered models first (not overwritten by preset)
	inCached := providerInputFor(upstream.URL + "/v1")
	inCached.Name = "Cached DeepSeek"
	inCached.Preset = "deepseek"
	savedCached := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, inCached))
	if _, err := store.SaveProviderDiscoveredModels(savedCached.ID, []string{"cached-deepseek-v3", "cached-deepseek-r1"}); err != nil {
		t.Fatalf("seed discovered models: %v", err)
	}
	respCached := providerRequest(t, h, "POST", "/api/cloud-providers/models?persist=1&fallback=1", token, map[string]any{
		"id":       savedCached.ID,
		"persist":  true,
		"fallback": true,
	})
	if respCached.Code != 200 {
		t.Fatalf("expected 200 for cached fallback, got %d: %s", respCached.Code, respCached.Body.String())
	}
	var resCached providerModelListResult
	if err := json.Unmarshal(respCached.Body.Bytes(), &resCached); err != nil {
		t.Fatalf("unmarshal cached fallback: %v", err)
	}
	if !resCached.Fallback || resCached.Source != "discovered" || len(resCached.Models) != 2 || resCached.Models[0] != "cached-deepseek-v3" {
		t.Fatalf("expected fallback to cached discoveredModels with source=discovered, got %+v", resCached)
	}

	// 2. Custom provider with no cached DiscoveredModels and no matching preset should return source="none" and models=[]
	inCustom := providerInputFor(upstream.URL + "/v1")
	inCustom.Name = "Custom Private Endpoint"
	inCustom.Preset = "custom"
	savedCustom := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, inCustom))
	respCustom := providerRequest(t, h, "POST", "/api/cloud-providers/models?persist=1&fallback=1", token, map[string]any{
		"id":       savedCustom.ID,
		"persist":  true,
		"fallback": true,
	})
	if respCustom.Code != 200 {
		t.Fatalf("expected 200 for custom provider fallback, got %d: %s", respCustom.Code, respCustom.Body.String())
	}
	var resCustom providerModelListResult
	if err := json.Unmarshal(respCustom.Body.Bytes(), &resCustom); err != nil {
		t.Fatalf("unmarshal custom fallback: %v", err)
	}
	if !resCustom.Fallback || resCustom.Source != "none" || resCustom.Models == nil || len(resCustom.Models) != 0 {
		t.Fatalf("expected fallback with source=none and empty models slice for custom provider, got %+v", resCustom)
	}
}
