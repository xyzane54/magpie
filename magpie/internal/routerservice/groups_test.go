package routerservice

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
)

func newRoutingGroupTestHandler(t *testing.T, local *LocalRuntime) (http.Handler, *routerconfig.Store, string) {
	t.Helper()
	root := t.TempDir()
	layout := dirs.Layout{
		Install: filepath.Join(root, "install"),
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Models:  filepath.Join(root, "data", "models"),
		Runtime: filepath.Join(root, "data", "runtime"),
		Cache:   filepath.Join(root, "cache"),
		State:   filepath.Join(root, "state"),
		Logs:    filepath.Join(root, "state", "logs"),
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store := routerconfig.New(layout)
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	info := Info{Service: "api-router", Address: "127.0.0.1:13145", PID: 42}
	token := "test-mgmt-token"
	h := handlerWithModelOptions(info, token, store, ModelOptions{LocalRuntime: local})
	return h, store, token
}

func doGroupRequest(t *testing.T, h http.Handler, token, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:13145"+target, reader)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:13145"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutingGroupsAutoDiscoveryCloudAndLocal(t *testing.T) {
	local := &LocalRuntime{
		Catalog: routermanifest.Catalog{
			Models: []routermanifest.Model{{ID: "qwen2.5-coder-7b"}},
		},
	}
	h, store, token := newRoutingGroupTestHandler(t, local)

	key := "sk-test-key"
	m1 := []string{"claude-sonnet-4-20250514", "gpt-4.1-mini"}
	p1, err := store.SaveCloudProvider(routerconfig.ProviderEdit{
		Name: "Anthropic Direct", Protocol: "chat", BaseURL: "https://api.openai.com/v1",
		APIKey: &key, DiscoveredModels: &m1, ProxyMode: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	m2 := []string{"anthropic/claude-sonnet-4-0", "qwen/qwen2.5-coder-7b"}
	p2, err := store.SaveCloudProvider(routerconfig.ProviderEdit{
		Name: "OpenRouter", Protocol: "chat", BaseURL: "https://openrouter.ai/api/v1",
		APIKey: &key, DiscoveredModels: &m2, ProxyMode: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unauthenticated request must return 401.
	if rec := doGroupRequest(t, h, "", http.MethodGet, "/api/routing-groups", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	rec := doGroupRequest(t, h, token, http.MethodGet, "/api/routing-groups", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/routing-groups status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool        `json:"ok"`
		Groups []groupView `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || len(resp.Groups) != 2 {
		t.Fatalf("expected 2 auto-discovered groups, got %+v", resp)
	}

	byID := make(map[string]groupView, len(resp.Groups))
	for _, g := range resp.Groups {
		byID[g.ID] = g
	}

	sonnet, ok := byID["auto-claude-sonnet-4"]
	if !ok || !sonnet.Auto || !sonnet.Enabled || sonnet.Disabled || len(sonnet.Members) != 2 {
		t.Fatalf("unexpected auto-claude-sonnet-4 group: %+v", sonnet)
	}
	wantM1, wantM2 := p1.ID+"/claude-sonnet-4-20250514", p2.ID+"/anthropic/claude-sonnet-4-0"
	if !((sonnet.Members[0] == wantM1 && sonnet.Members[1] == wantM2) || (sonnet.Members[0] == wantM2 && sonnet.Members[1] == wantM1)) {
		t.Fatalf("sonnet members=%v, want %s and %s", sonnet.Members, wantM1, wantM2)
	}

	qwen, ok := byID["auto-qwen2-5-coder-7b"]
	if !ok || !qwen.Auto || !qwen.Enabled {
		t.Fatalf("unexpected auto-qwen2-5-coder-7b group: %+v", qwen)
	}
	wantQwen := []string{"local/qwen2.5-coder-7b", p2.ID + "/qwen/qwen2.5-coder-7b"}
	if !reflect.DeepEqual(qwen.Members, wantQwen) {
		t.Fatalf("qwen members=%v, want %v", qwen.Members, wantQwen)
	}
}

func TestRoutingGroupsCRUDAndValidation(t *testing.T) {
	h, _, token := newRoutingGroupTestHandler(t, nil)

	// 1. Create groups with 4 strategies and advanced parameters.
	for _, tc := range []struct {
		name    string
		payload map[string]any
		wantID  string
	}{
		{
			name: "smart_hybrid",
			payload: map[string]any{
				"name":       "Smart Hybrid Coding",
				"members":    []string{"local/qwen2.5-coder-7b", "openai/gpt-4.1"},
				"routing":    "smart_hybrid",
				"affinity":   "session",
				"patience":   60,
				"firstToken": 15,
				"effort":     "auto",
				"fast":       []string{"local/qwen2.5-coder-7b"},
				"rules":      []map[string]any{{"pattern": "simple", "use": "local/qwen2.5-coder-7b"}},
			},
			wantID: "smart-hybrid-coding",
		},
		{
			name: "ordered",
			payload: map[string]any{
				"id":      "ordered-pool",
				"name":    "Ordered Pool",
				"members": []string{"p1/m1", "p2/m2"},
				"routing": "ordered",
			},
			wantID: "ordered-pool",
		},
		{
			name: "weighted",
			payload: map[string]any{
				"id":      "weighted-pool",
				"name":    "Weighted Pool",
				"members": []string{"p1/m1", "p2/m2"},
				"routing": "weighted",
				"weights": map[string]int{"p1/m1": 70, "p2/m2": 30},
			},
			wantID: "weighted-pool",
		},
		{
			name: "manual",
			payload: map[string]any{
				"id":      "manual-pool",
				"name":    "Manual Pool",
				"members": []string{"p1/m1"},
				"routing": "manual",
			},
			wantID: "manual-pool",
		},
	} {
		rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", tc.payload)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s create failed: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		var out struct {
			OK    bool      `json:"ok"`
			Group groupView `json:"group"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.OK || out.Group.ID != tc.wantID || !out.Group.Enabled {
			t.Fatalf("%s unexpected saved group: %+v", tc.name, out.Group)
		}
	}

	// 2. Invalid inputs must return 400 Bad Request.
	invalidPayloads := []map[string]any{
		{"id": "no-name", "name": "   ", "members": []string{"p1/m1"}},
		{"name": "Bad Strategy", "members": []string{"p1/m1"}, "routing": "unknown_strategy"},
		{"name": "Bad Weight Zero", "members": []string{"p1/m1"}, "weights": map[string]int{"p1/m1": 0}},
		{"name": "Bad Weight High", "members": []string{"p1/m1"}, "weights": map[string]int{"p1/m1": 150}},
		{"name": "Bad Rule", "members": []string{"p1/m1"}, "rules": []map[string]any{{"pattern": "test"}}},
		{"id": "self-cycle", "name": "Self Cycle", "members": []string{"group/self-cycle"}},
	}
	for i, bad := range invalidPayloads {
		rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", bad)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid case %d expected 400, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestRoutingGroupsSwitchAndDelete(t *testing.T) {
	local := &LocalRuntime{Catalog: routermanifest.Catalog{Models: []routermanifest.Model{{ID: "deepseek-r1"}}}}
	h, store, token := newRoutingGroupTestHandler(t, local)
	key := "sk-test"
	models := []string{"deepseek/deepseek-r1"}
	if _, err := store.SaveCloudProvider(routerconfig.ProviderEdit{
		Name: "OpenRouter", Protocol: "chat", BaseURL: "https://openrouter.ai/api/v1",
		APIKey: &key, DiscoveredModels: &models, ProxyMode: "direct",
	}); err != nil {
		t.Fatal(err)
	}
	if rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", map[string]any{
		"id": "custom-1", "name": "Custom One", "members": []string{"openrouter/deepseek/deepseek-r1"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("create custom-1: %d %s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"auto-deepseek-r1", "custom-1"} {
		rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups/switch", map[string]any{"id": id, "enabled": false})
		if rec.Code != http.StatusOK {
			t.Fatalf("switch off %s: %d %s", id, rec.Code, rec.Body.String())
		}
		var out struct {
			Group groupView `json:"group"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Group.Enabled || !out.Group.Disabled {
			t.Fatalf("expected %s disabled after switch off, got %+v", id, out.Group)
		}
	}
	if rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups/switch", map[string]any{"id": "custom-1", "enabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("switch on custom-1: %d", rec.Code)
	}
	if rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups/switch", map[string]any{"id": "missing-id", "enabled": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 switching missing group, got %d", rec.Code)
	}
	for _, id := range []string{"custom-1", "auto-deepseek-r1"} {
		rec := doGroupRequest(t, h, token, http.MethodDelete, "/api/routing-groups?id="+id, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("delete %s: %d %s", id, rec.Code, rec.Body.String())
		}
		rec2 := doGroupRequest(t, h, token, http.MethodDelete, "/api/routing-groups?id="+id, nil)
		if rec2.Code != http.StatusNotFound {
			t.Fatalf("second delete %s expected 404, got %d", id, rec2.Code)
		}
	}
}

func TestRoutingGroupsEditorFullPayloadRoundTrip(t *testing.T) {
	h, _, token := newRoutingGroupTestHandler(t, nil)

	editorPayload := map[string]any{
		"id":           "editor-full",
		"name":         "Editor Full Options",
		"alias":        "sonnet-unified",
		"members":      []string{"p1/m1:high", "p2/m2"},
		"routing":      "round-robin",
		"cooldownMove": true,
		"hideMembers":  true,
		"context":      -1,
		"effort":       "auto",
		"memberEfforts": map[string]string{
			"p2/m2": "medium",
		},
		"levels":     []string{"low", "medium"},
		"fast":       []string{"p1/m1"},
		"firstToken": 12,
		"patience":   45,
		"affinity":   "session",
		"stickiness": 30,
		"rules": []map[string]any{
			{
				"when":    "tokens",
				"pattern": ">100",
				"use":     "p1/m1",
				"effort":  "high",
				"fast":    true,
			},
			{
				"when":   "vision",
				"target": "p2/m2",
			},
		},
	}

	rec := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", editorPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("create editor-full failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool      `json:"ok"`
		Group groupView `json:"group"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	g := out.Group
	if g.Family != "sonnet-unified" || !g.Sink || !g.Unlists || g.Context != -1 {
		t.Fatalf("unexpected family/sink/unlists/context: %+v", g)
	}
	if g.Stickiness != 30 || g.Affinity != "session" || g.FirstToken != 12 || g.Patience != 45 {
		t.Fatalf("unexpected stickiness/affinity/firstToken/patience: %+v", g)
	}
	if g.MemberEfforts["p1/m1"] != "high" || g.MemberEfforts["p2/m2"] != "medium" {
		t.Fatalf("unexpected memberEfforts: %+v", g.MemberEfforts)
	}
	if len(g.Levels) != 2 || len(g.Rules) != 2 || g.Rules[0].When != "tokens" || !g.Rules[0].Fast || g.Rules[0].Effort != "high" {
		t.Fatalf("unexpected levels/rules: levels=%+v rules=%+v", g.Levels, g.Rules)
	}
	if g.Rules[1].When != "vision" || g.Rules[1].Use != "p2/m2" {
		t.Fatalf("expected vision rule without pattern to round-trip, got %+v", g.Rules[1])
	}

	// Verify all routing strategy names accepted by editor.
	for _, strat := range []string{"rotate", "round-robin", "usage", "least-used", "pace", "drain-reset", "order", "weight"} {
		recStrat := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", map[string]any{
			"id":      "strat-" + strat,
			"name":    "Strat " + strat,
			"members": []string{"p1/m1", "p2/m2"},
			"routing": strat,
		})
		if recStrat.Code != http.StatusOK {
			t.Fatalf("strategy %s rejected: %d %s", strat, recStrat.Code, recStrat.Body.String())
		}
	}

	// Invalid rule 'when' field must return 400.
	recBadWhen := doGroupRequest(t, h, token, http.MethodPost, "/api/routing-groups", map[string]any{
		"name":    "Bad When",
		"members": []string{"p1/m1"},
		"rules":   []map[string]any{{"when": "invalid_dim", "pattern": "x", "use": "p1/m1"}},
	})
	if recBadWhen.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid rule.when, got %d", recBadWhen.Code)
	}
}
