package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelListDraftsErrorsAndRedirectsDoNotLeakOrSave(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer target.Close()
	var mode atomic.Value
	mode.Store("success")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "success":
			_, _ = io.WriteString(w, `{"data":[{"id":"manual-model"}]}`)
		case "error":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "test-cloud-api-key reflected by upstream")
		case "redirect":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "invalid":
			_, _ = io.WriteString(w, `{"unknown":[]}`)
		}
	}))
	defer upstream.Close()
	l, _ := fixture(t)
	h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(l))
	in := providerInputFor(upstream.URL + "/v1")
	in.Model = ""
	before, _ := os.ReadFile(l.ConfigFile())
	for _, state := range []string{"success", "error", "redirect", "invalid"} {
		mode.Store(state)
		w := providerRequest(t, h, "POST", "/api/cloud-providers/models", "management-token", in)
		want := 502
		if state == "success" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("%s: %d %s", state, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "test-cloud-api-key") {
			t.Fatal("upstream reflected key leaked")
		}
	}
	if targetCalls.Load() != 0 {
		t.Fatal("credentialed redirect was followed")
	}
	after, _ := os.ReadFile(l.ConfigFile())
	if !bytes.Equal(before, after) {
		t.Fatal("draft fetching wrote config")
	}
	if _, err := os.Stat(l.CredentialsFile()); !os.IsNotExist(err) {
		t.Fatal("draft fetching wrote credentials")
	}
}

func TestAnthropicModelListAndProxy(t *testing.T) {
	var pages atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "test-cloud-api-key" || r.Header.Get("Authorization") != "" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("invalid anthropic model request")
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a"}],"has_more":true,"last_id":"model-a"}`)
		} else {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-b"}],"has_more":false}`)
		}
	}))
	defer upstream.Close()
	resolved := routerconfig.ResolvedProvider{Config: routerconfig.CloudProvider{Protocol: "anthropic", BaseURL: upstream.URL, ProxyMode: "direct"}, APIKey: "test-cloud-api-key"}
	models, err := providerModels(context.Background(), resolved)
	if err != nil || len(models) != 2 || pages.Load() != 2 {
		t.Fatalf("pagination: %v %v", models, err)
	}
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.URL.Host != "configured.invalid" {
			t.Error("proxy did not receive configured target")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"proxy-model"}]}`)
	}))
	defer proxy.Close()
	resolved.Config.Protocol = "chat"
	resolved.Config.BaseURL = "http://configured.invalid/v1"
	resolved.Config.ProxyMode = "custom"
	resolved.ProxyURL = proxy.URL
	models, err = providerModels(context.Background(), resolved)
	if err != nil || len(models) != 1 || proxyCalls.Load() != 1 {
		t.Fatalf("proxy: %v %v", models, err)
	}
}

func TestProviderPresetModelsFallbackWhenDiscoveredModelsEmpty(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "management-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)

	cases := []struct {
		name      string
		preset    string
		baseURL   string
		wantModel string
	}{
		{name: "Spark Provider", preset: "spark", baseURL: "https://spark-api-open.xf-yun.com/v1", wantModel: "4.0Ultra"},
		{name: "OpenAI Provider", preset: "openai", baseURL: "https://api.openai.com/v1", wantModel: "gpt-5.4"},
		{name: "DeepSeek Provider", preset: "deepseek", baseURL: "https://api.deepseek.com/v1", wantModel: "deepseek-chat"},
		{name: "Qwen Provider", preset: "qwen-cn", baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", wantModel: "qwen3-max"},
	}

	savedByPreset := map[string]string{}
	for _, tc := range cases {
		in := providerInputFor(tc.baseURL)
		in.Name = tc.name
		in.Preset = tc.preset
		p := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, in))
		savedByPreset[tc.preset] = p.ID
	}

	list := providerRequest(t, h, "GET", "/api/cloud-providers", token, nil)
	if list.Code != 200 {
		t.Fatalf("GET /api/cloud-providers status=%d body=%s", list.Code, list.Body.String())
	}
	var payload struct {
		Providers       []redactedCloudProvider `json:"providers"`
		CandidateModels []candidateModelEntry   `json:"candidateModels"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	for _, tc := range cases {
		provID := savedByPreset[tc.preset]
		var found *redactedCloudProvider
		for i := range payload.Providers {
			if payload.Providers[i].ID == provID {
				found = &payload.Providers[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("provider %s (%s) not found in list", tc.preset, provID)
		}
		if len(found.DiscoveredModels) != 0 {
			t.Fatalf("expected empty discoveredModels for %s, got %v", tc.preset, found.DiscoveredModels)
		}
		hasModel := false
		for _, m := range found.EffectiveModels {
			if m == tc.wantModel {
				hasModel = true
				break
			}
		}
		if !hasModel || len(found.Models) == 0 {
			t.Fatalf("expected preset model %q in effectiveModels for %s, got models=%v effectiveModels=%v", tc.wantModel, tc.preset, found.Models, found.EffectiveModels)
		}
		wantSelector := provID + "/" + tc.wantModel
		hasCandidate := false
		for _, c := range payload.CandidateModels {
			if c.Selector == wantSelector && c.SourceType == "cloud" {
				hasCandidate = true
				break
			}
		}
		if !hasCandidate {
			t.Fatalf("expected candidate selector %q in candidateModels", wantSelector)
		}
	}
}

func TestProviderModelDiscoveryPersistAndOverridePreset(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-custom-v3"},{"id":"deepseek-r2"}]}`)
	}))
	defer upstream.Close()

	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "management-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)

	in := providerInputFor(upstream.URL + "/v1")
	in.Name = "DeepSeek Custom"
	in.Preset = "deepseek"
	saved := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, in))

	// Persist discovered models via POST /api/cloud-providers/models with persist: true
	fetchResp := providerRequest(t, h, "POST", "/api/cloud-providers/models", token, map[string]any{
		"id":       saved.ID,
		"persist":  true,
		"fallback": true,
	})
	if fetchResp.Code != 200 {
		t.Fatalf("expected 200 from model discovery with persist, got %d: %s", fetchResp.Code, fetchResp.Body.String())
	}
	var fetchData providerModelListResult
	if err := json.Unmarshal(fetchResp.Body.Bytes(), &fetchData); err != nil {
		t.Fatalf("unmarshal model list result: %v", err)
	}
	if fetchData.Fallback || fetchData.Source != "discovered" || len(fetchData.Models) != 2 || fetchData.Models[0] != "deepseek-custom-v3" {
		t.Fatalf("unexpected discovery result: %+v", fetchData)
	}

	// Verify config.json persisted discoveredModels
	persistedProviders, err := store.CloudProviders()
	if err != nil || len(persistedProviders) != 1 {
		t.Fatalf("load persisted providers: %v %v", persistedProviders, err)
	}
	if len(persistedProviders[0].DiscoveredModels) != 2 || persistedProviders[0].DiscoveredModels[0] != "deepseek-custom-v3" {
		t.Fatalf("expected discoveredModels persisted in config.json, got %v", persistedProviders[0].DiscoveredModels)
	}

	// Verify GET /api/cloud-providers prioritizes discoveredModels over preset models
	list := providerRequest(t, h, "GET", "/api/cloud-providers", token, nil)
	if list.Code != 200 {
		t.Fatalf("GET /api/cloud-providers: %d %s", list.Code, list.Body.String())
	}
	var listPayload struct {
		Providers []redactedCloudProvider `json:"providers"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listPayload); err != nil || len(listPayload.Providers) != 1 {
		t.Fatalf("unmarshal providers: %v %+v", err, listPayload)
	}
	got := listPayload.Providers[0]
	if got.ModelSource != "discovered" || len(got.EffectiveModels) != 2 || got.EffectiveModels[0] != "deepseek-custom-v3" || got.EffectiveModels[1] != "deepseek-r2" {
		t.Fatalf("expected discovered models to override preset models, got %+v", got)
	}
}
