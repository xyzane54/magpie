package routerservice

import (
	"bytes"
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

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
)

func newGroupRoutingEnv(t *testing.T, opts ModelOptions) (http.Handler, *routerconfig.Store) {
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
	return handlerWithModelOptions(info, "mgmt-secret-token", store, opts), store
}

func saveTestCloudProvider(t *testing.T, store *routerconfig.Store, name, baseURL string, models ...string) routerconfig.CloudProvider {
	t.Helper()
	key := "sk-provider-" + name
	p, err := store.SaveCloudProvider(routerconfig.ProviderEdit{
		Name: name, Protocol: "chat", BaseURL: baseURL,
		APIKey: &key, DiscoveredModels: &models, ProxyMode: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func saveGroupBoundApp(t *testing.T, store *routerconfig.Store, models []string) {
	t.Helper()
	if _, err := store.SaveApp(routerconfig.AppEdit{
		ID:             "codex",
		Name:           "Codex",
		Models:         &models,
		CredentialMode: routerconfig.StoredCredential,
	}); err != nil {
		t.Fatal(err)
	}
}

func postAppChat(t *testing.T, h http.Handler, model string) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	if model != "" {
		payload["model"] = model
	}
	return doGroupRequest(t, h, "", http.MethodPost, "/app/codex/v1/chat/completions", payload)
}

func TestGroupRoutingSmartSplit(t *testing.T) {
	var cloudCalls atomic.Int32
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cloud-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-cloud"},"finish_reason":"stop"}]}`))
	}))
	defer cloudSrv.Close()

	judge := &scriptedJudge{decision: DecisionLocal}
	invoker := &scriptedInvoker{response: localChatResponse()}
	h, store := newGroupRoutingEnv(t, ModelOptions{
		Routing: &RoutingOptions{Judge: judge, LocalInvoker: invoker},
	})

	p := saveTestCloudProvider(t, store, "CloudOne", cloudSrv.URL+"/v1", "gpt-4.1")
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "hybrid-group", Name: "Hybrid Group", Routing: "smart_split",
		Members: []string{"local/qwen2.5-coder-7b", p.ID + "/gpt-4.1"},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/hybrid-group"})

	// 1. Judged local -> local runtime responds directly without touching cloud.
	rec1 := postAppChat(t, h, "group/hybrid-group")
	if rec1.Code != http.StatusOK || invoker.calls.Load() != 1 || cloudCalls.Load() != 0 {
		t.Fatalf("smart_split local case failed: code=%d local=%d cloud=%d body=%s", rec1.Code, invoker.calls.Load(), cloudCalls.Load(), rec1.Body.String())
	}

	// 2. Judged local, but local fails before committing output -> seamless failover to cloud.
	invoker.response = nil
	invoker.err = errors.New("local inference failed")
	rec2 := postAppChat(t, h, "hybrid-group")
	if rec2.Code != http.StatusOK || invoker.calls.Load() != 2 || cloudCalls.Load() != 1 {
		t.Fatalf("smart_split failover case failed: code=%d local=%d cloud=%d body=%s", rec2.Code, invoker.calls.Load(), cloudCalls.Load(), rec2.Body.String())
	}

	// 3. Judged cloud -> routes directly to cloud member without calling local invoker.
	judge.decision = DecisionCloud
	rec3 := postAppChat(t, h, "")
	if rec3.Code != http.StatusOK || invoker.calls.Load() != 2 || cloudCalls.Load() != 2 {
		t.Fatalf("smart_split cloud case failed: code=%d local=%d cloud=%d body=%s", rec3.Code, invoker.calls.Load(), cloudCalls.Load(), rec3.Body.String())
	}
}

func TestGroupRoutingOrderedFailover(t *testing.T) {
	var calls1, calls3 atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls1.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream 500"}}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv2.Close() // closed immediately to simulate connection failure

	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls3.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c3","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok-from-3"},"finish_reason":"stop"}]}`))
	}))
	defer srv3.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	p1 := saveTestCloudProvider(t, store, "P1", srv1.URL+"/v1", "m1")
	p2 := saveTestCloudProvider(t, store, "P2", srv2.URL+"/v1", "m2")
	p3 := saveTestCloudProvider(t, store, "P3", srv3.URL+"/v1", "m3")

	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "ordered-group", Name: "Ordered Group", Routing: "ordered",
		Members: []string{p1.ID + "/m1", p2.ID + "/m2", p3.ID + "/m3"},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/ordered-group"})

	rec := postAppChat(t, h, "group/ordered-group")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from 3rd member, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls1.Load() == 0 || calls3.Load() != 1 {
		t.Fatalf("unexpected member call counts: calls1=%d calls3=%d", calls1.Load(), calls3.Load())
	}
}

func TestGroupRoutingWeightedAndFailover(t *testing.T) {
	var failFirst atomic.Bool
	var callsA, callsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		if failFirst.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-a"},"finish_reason":"stop"}]}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`))
	}))
	defer srvB.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	pA := saveTestCloudProvider(t, store, "PA", srvA.URL+"/v1", "ma")
	pB := saveTestCloudProvider(t, store, "PB", srvB.URL+"/v1", "mb")
	mA, mB := pA.ID+"/ma", pB.ID+"/mb"

	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "weighted-group", Name: "Weighted Group", Routing: "weighted",
		Members: []string{mA, mB}, Weights: map[string]int{mA: 80, mB: 20},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/weighted-group"})

	groupWeightSeq.Store(0)
	for i := 0; i < 5; i++ {
		if rec := postAppChat(t, h, "group/weighted-group"); rec.Code != http.StatusOK {
			t.Fatalf("req %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	if callsA.Load() != 4 || callsB.Load() != 1 {
		t.Fatalf("expected 80:20 (4:1) distribution over 5 requests, got A=%d B=%d", callsA.Load(), callsB.Load())
	}

	// When A fails on its turn, weighted routing must failover to B.
	groupWeightSeq.Store(0)
	failFirst.Store(true)
	callsA.Store(0)
	callsB.Store(0)
	rec := postAppChat(t, h, "group/weighted-group")
	if rec.Code != http.StatusOK || callsA.Load() == 0 || callsB.Load() != 1 {
		t.Fatalf("expected weighted failover from A to B: status=%d A=%d B=%d", rec.Code, callsA.Load(), callsB.Load())
	}
}

func TestGroupRoutingManualAndDisabled(t *testing.T) {
	var calls1, calls2 atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"m1"},"finish_reason":"stop"}]}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls2.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	p1 := saveTestCloudProvider(t, store, "M1", srv1.URL+"/v1", "m1")
	p2 := saveTestCloudProvider(t, store, "M2", srv2.URL+"/v1", "m2")

	// 1. Manual strategy only invokes Pick member and does not failover when Pick fails.
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "manual-group", Name: "Manual Group", Routing: "manual",
		Members: []string{p1.ID + "/m1", p2.ID + "/m2"}, Pick: p2.ID + "/m2",
	}); err != nil {
		t.Fatal(err)
	}
	// 2. Disabled group returns 409 group_disabled.
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "off-group", Name: "Off Group", Routing: "ordered", Disabled: true,
		Members: []string{p1.ID + "/m1"},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/manual-group", "group/off-group"})

	recManual := postAppChat(t, h, "group/manual-group")
	if recManual.Code == http.StatusOK || calls2.Load() == 0 || calls1.Load() != 0 {
		t.Fatalf("manual group should only call pick and not failover: status=%d calls1=%d calls2=%d", recManual.Code, calls1.Load(), calls2.Load())
	}

	recOff := postAppChat(t, h, "group/off-group")
	if recOff.Code != http.StatusConflict {
		t.Fatalf("disabled group expected 409, got %d: %s", recOff.Code, recOff.Body.String())
	}
	var errEnv struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recOff.Body.Bytes(), &errEnv)
	if errEnv.Error.Code != "group_disabled" {
		t.Fatalf("expected error.code=group_disabled, got %q (%s)", errEnv.Error.Code, recOff.Body.String())
	}
}

func postAppChatWithPayload(t *testing.T, h http.Handler, payload map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:13145/app/codex/v1/chat/completions", bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:13145"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGroupRoutingStrategiesRotateUsageAndSink(t *testing.T) {
	var rateLimitA atomic.Bool
	var callsA, callsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		if rateLimitA.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-a"},"finish_reason":"stop"}]}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`))
	}))
	defer srvB.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	pA := saveTestCloudProvider(t, store, "PA", srvA.URL+"/v1", "ma")
	pB := saveTestCloudProvider(t, store, "PB", srvB.URL+"/v1", "mb")
	mA, mB := pA.ID+"/ma", pB.ID+"/mb"

	// 1. Round-robin (rotate) alternates across requests when affinity is off.
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "rr-group", Name: "RR Group", Routing: "round-robin", Affinity: "off",
		Members: []string{mA, mB},
	}); err != nil {
		t.Fatal(err)
	}
	// 2. Least-used (usage) prefers member with lower recorded token usage.
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "usage-group", Name: "Usage Group", Routing: "least-used", Affinity: "off",
		Members: []string{mA, mB},
	}); err != nil {
		t.Fatal(err)
	}
	// 3. Sink (cooldownMove) moves 429-failing member to the end for subsequent requests.
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "sink-group", Name: "Sink Group", Routing: "ordered", Affinity: "off", Sink: true,
		Members: []string{mA, mB},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/rr-group", "group/usage-group", "group/sink-group"})

	for i := 0; i < 4; i++ {
		if rec := postAppChat(t, h, "group/rr-group"); rec.Code != http.StatusOK {
			t.Fatalf("rr req %d failed: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if callsA.Load() != 2 || callsB.Load() != 2 {
		t.Fatalf("expected round-robin 2:2 calls, got A=%d B=%d", callsA.Load(), callsB.Load())
	}

	// Record heavy token usage on mA so least-used picks mB.
	now := time.Now()
	recordMemberSuccess(mA, 50000, now)
	recordMemberSuccess(mB, 10, now)
	callsA.Store(0)
	callsB.Store(0)
	if rec := postAppChat(t, h, "group/usage-group"); rec.Code != http.StatusOK || callsB.Load() != 1 || callsA.Load() != 0 {
		t.Fatalf("expected least-used to pick mB, got A=%d B=%d", callsA.Load(), callsB.Load())
	}

	// Sink test: mA returns 429 on request 1 -> fails over to mB and sinks mA.
	// On request 2 (even with rateLimitA reset), mB is tried first without hitting mA.
	callsA.Store(0)
	callsB.Store(0)
	rateLimitA.Store(true)
	if rec := postAppChat(t, h, "group/sink-group"); rec.Code != http.StatusOK {
		t.Fatalf("sink req 1 failed: %d %s", rec.Code, rec.Body.String())
	}
	if callsA.Load() == 0 || callsB.Load() != 1 {
		t.Fatalf("sink req 1 expected A tried then B=1, got A=%d B=%d", callsA.Load(), callsB.Load())
	}
	rateLimitA.Store(false)
	callsA.Store(0)
	callsB.Store(0)
	if rec := postAppChat(t, h, "group/sink-group"); rec.Code != http.StatusOK {
		t.Fatalf("sink req 2 failed: %d %s", rec.Code, rec.Body.String())
	}
	if callsA.Load() != 0 || callsB.Load() != 1 {
		t.Fatalf("after sink, expected mB tried first (A=0, B=1), got A=%d B=%d", callsA.Load(), callsB.Load())
	}
}

func TestGroupRoutingModelsUnlistsContextAndFamily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"by-alias"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	p1 := saveTestCloudProvider(t, store, "P1", srv.URL+"/v1", "claude-sonnet-4-20250514", "gpt-4o-mini")
	mSonnet := p1.ID + "/claude-sonnet-4-20250514"
	mMini := p1.ID + "/gpt-4o-mini"

	if _, err := store.SaveGroup(routerconfig.Group{
		ID:      "coding-pool",
		Name:    "Coding Pool",
		Family:  "sonnet-unified",
		Routing: "ordered",
		Unlists: true,
		Context: -1, // min of 200k and 128k -> 128000
		Members: []string{mSonnet, mMini},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/coding-pool", mSonnet, mMini})

	// 1. GET /app/codex/v1/models should unlist mSonnet & mMini and report context_window=128000 on group/coding-pool.
	recModels := doGroupRequest(t, h, "", http.MethodGet, "/app/codex/v1/models", nil)
	if recModels.Code != http.StatusOK {
		t.Fatalf("GET /app/codex/v1/models status=%d body=%s", recModels.Code, recModels.Body.String())
	}
	var listOut struct {
		Data []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recModels.Body.Bytes(), &listOut); err != nil {
		t.Fatal(err)
	}
	if len(listOut.Data) != 1 || listOut.Data[0].ID != "group/coding-pool" || listOut.Data[0].ContextWindow != 128000 {
		t.Fatalf("expected only group/coding-pool with context_window=128000, got %+v", listOut.Data)
	}

	// 2. Request using Family alias "sonnet-unified" routes to the group.
	recChat := postAppChat(t, h, "sonnet-unified")
	if recChat.Code != http.StatusOK || !strings.Contains(recChat.Body.String(), "by-alias") {
		t.Fatalf("expected chat via family alias sonnet-unified to succeed, got %d: %s", recChat.Code, recChat.Body.String())
	}
}

func TestGroupRoutingEffortLevelsAndFast(t *testing.T) {
	var mu sync.Mutex
	var gotPayloads []map[string]any
	var failFirst atomic.Bool

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failFirst.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		gotPayloads = append(gotPayloads, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok1"},"finish_reason":"stop"}]}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		gotPayloads = append(gotPayloads, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok2"},"finish_reason":"stop"}]}`))
	}))
	defer srv2.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	p1 := saveTestCloudProvider(t, store, "P1", srv1.URL+"/v1", "o3-mini")
	p2 := saveTestCloudProvider(t, store, "P2", srv2.URL+"/v1", "o3-mini-2")
	m1, m2 := p1.ID+"/o3-mini", p2.ID+"/o3-mini-2"

	if _, err := store.SaveGroup(routerconfig.Group{
		ID:       "effort-group",
		Name:     "Effort Group",
		Routing:  "ordered",
		Affinity: "off",
		Effort:   "low",
		Members:  []string{m1, m2 + ":xhigh"},
		Levels:   []string{"low", "medium"},
		Fast:     []string{m1},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/effort-group"})

	// Request 1 hits m1 -> gets group Effort="low" and Fast service_tier="priority".
	if rec := postAppChat(t, h, "group/effort-group"); rec.Code != http.StatusOK {
		t.Fatalf("req 1 failed: %d %s", rec.Code, rec.Body.String())
	}
	// Request 2 fails m1 -> fails over to m2 which has memberEffort="xhigh" clamped by Levels to "medium" and no Fast.
	failFirst.Store(true)
	if rec := postAppChat(t, h, "group/effort-group"); rec.Code != http.StatusOK {
		t.Fatalf("req 2 failed: %d %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotPayloads) != 2 {
		t.Fatalf("expected 2 captured upstream payloads, got %d", len(gotPayloads))
	}
	if gotPayloads[0]["reasoning_effort"] != "low" || gotPayloads[0]["service_tier"] != "priority" {
		t.Fatalf("m1 expected reasoning_effort=low & service_tier=priority, got %+v", gotPayloads[0])
	}
	if gotPayloads[1]["reasoning_effort"] != "medium" || gotPayloads[1]["service_tier"] != nil {
		t.Fatalf("m2 expected reasoning_effort=medium (clamped from xhigh) & no service_tier, got %+v", gotPayloads[1])
	}
}

func TestGroupRoutingFirstTokenTimeoutFailover(t *testing.T) {
	srvSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1500 * time.Millisecond):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"slow","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"slow"},"finish_reason":"stop"}]}`))
		}
	}))
	defer srvSlow.Close()

	srvFast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fast","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"fast-fallback"},"finish_reason":"stop"}]}`))
	}))
	defer srvFast.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	pSlow := saveTestCloudProvider(t, store, "PSlow", srvSlow.URL+"/v1", "mslow")
	pFast := saveTestCloudProvider(t, store, "PFast", srvFast.URL+"/v1", "mfast")

	if _, err := store.SaveGroup(routerconfig.Group{
		ID:         "ft-group",
		Name:       "FirstToken Group",
		Routing:    "ordered",
		Affinity:   "off",
		FirstToken: 1, // 1 second first-token timeout
		Members:    []string{pSlow.ID + "/mslow", pFast.ID + "/mfast"},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/ft-group"})

	rec := postAppChat(t, h, "group/ft-group")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fast-fallback") {
		t.Fatalf("expected firstToken timeout to failover to fast member, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGroupRoutingPatienceRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 3 {
			// Initial round (up to 3 cloud retry attempts) returns 429; next patience round succeeds.
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"recovered","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"patience-ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	p := saveTestCloudProvider(t, store, "PPatience", srv.URL+"/v1", "mp")

	if _, err := store.SaveGroup(routerconfig.Group{
		ID:       "patience-group",
		Name:     "Patience Group",
		Routing:  "ordered",
		Affinity: "off",
		Patience: 5,
		Members:  []string{p.ID + "/mp"},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/patience-group"})

	rec := postAppChat(t, h, "group/patience-group")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "patience-ok") {
		t.Fatalf("expected patience retry to recover and return 200, got %d (attempts=%d): %s", rec.Code, attempts.Load(), rec.Body.String())
	}
}

func TestGroupRoutingAffinityModes(t *testing.T) {
	var failA atomic.Bool
	var callsA, callsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		if failA.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-a"},"finish_reason":"stop"}]}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`))
	}))
	defer srvB.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	pA := saveTestCloudProvider(t, store, "PA", srvA.URL+"/v1", "ma")
	pB := saveTestCloudProvider(t, store, "PB", srvB.URL+"/v1", "mb")
	mA, mB := pA.ID+"/ma", pB.ID+"/mb"

	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "sess-group", Name: "Session Group", Routing: "round-robin",
		Affinity: "session", Stickiness: 10,
		Members: []string{mA, mB},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroup(routerconfig.Group{
		ID: "pin-group", Name: "Pin Group", Routing: "ordered",
		Affinity: "pin",
		Members:  []string{mA, mB},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/sess-group", "group/pin-group"})

	// 1. Session affinity sticks to the first chosen member across multiple requests with same X-Session-Id,
	// even though strategy is round-robin.
	payload := map[string]any{
		"model":    "group/sess-group",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	for i := 0; i < 3; i++ {
		rec := postAppChatWithPayload(t, h, payload, map[string]string{"X-Session-Id": "sess-123"})
		if rec.Code != http.StatusOK {
			t.Fatalf("sess req %d failed: %d", i, rec.Code)
		}
	}
	if (callsA.Load() == 3 && callsB.Load() == 0) || (callsA.Load() == 0 && callsB.Load() == 3) {
		// expected: all 3 calls went to the same sticky member
	} else {
		t.Fatalf("expected session affinity to stick to one member across 3 calls, got A=%d B=%d", callsA.Load(), callsB.Load())
	}

	// 2. Pin affinity: once pinned to mA on turn 1, if mA fails on turn 2, it does NOT failover to mB.
	callsA.Store(0)
	callsB.Store(0)
	pinPayload := map[string]any{
		"model":    "group/pin-group",
		"messages": []map[string]string{{"role": "user", "content": "pin me"}},
	}
	rec1 := postAppChatWithPayload(t, h, pinPayload, map[string]string{"X-Session-Id": "pin-sess"})
	if rec1.Code != http.StatusOK || callsA.Load() != 1 {
		t.Fatalf("pin turn 1 failed: code=%d A=%d", rec1.Code, callsA.Load())
	}
	failA.Store(true)
	callsA.Store(0)
	callsB.Store(0)
	rec2 := postAppChatWithPayload(t, h, pinPayload, map[string]string{"X-Session-Id": "pin-sess"})
	if rec2.Code == http.StatusOK || callsB.Load() != 0 {
		t.Fatalf("pin turn 2 expected failure without failover to mB, got code=%d A=%d B=%d", rec2.Code, callsA.Load(), callsB.Load())
	}
}

func TestGroupRoutingRulesWhenDimensions(t *testing.T) {
	var callsA, callsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-a"},"finish_reason":"stop"}]}`))
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`))
	}))
	defer srvB.Close()

	h, store := newGroupRoutingEnv(t, ModelOptions{})
	pA := saveTestCloudProvider(t, store, "PA", srvA.URL+"/v1", "ma")
	pB := saveTestCloudProvider(t, store, "PB", srvB.URL+"/v1", "mb")
	mA, mB := pA.ID+"/ma", pB.ID+"/mb"

	if _, err := store.SaveGroup(routerconfig.Group{
		ID:       "rules-group",
		Name:     "Rules Group",
		Family:   "custom-alias-pro",
		Routing:  "ordered",
		Affinity: "off",
		Members:  []string{mA, mB},
		Rules: []routerconfig.Rule{
			{When: "vision", Use: mB},
			{When: "tools", Pattern: "web_search", Use: mB},
			{When: "system", Pattern: "architect", Use: mB},
			{When: "prompt", Pattern: "/deploy\\s+prod/i", Use: mB},
			{When: "tokens", Pattern: ">50", Use: mB},
			{When: "model", Pattern: "alias-pro", Use: mB},
		},
	}); err != nil {
		t.Fatal(err)
	}
	saveGroupBoundApp(t, store, []string{"group/rules-group"})

	// Default short prompt without matching rule -> mA (first in ordered).
	recDef := postAppChat(t, h, "group/rules-group")
	if recDef.Code != http.StatusOK || callsA.Load() != 1 || callsB.Load() != 0 {
		t.Fatalf("default expected mA, got A=%d B=%d", callsA.Load(), callsB.Load())
	}

	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "tools",
			payload: map[string]any{
				"model":    "group/rules-group",
				"messages": []map[string]string{{"role": "user", "content": "hi"}},
				"tools": []map[string]any{
					{"type": "function", "function": map[string]any{"name": "web_search"}},
				},
			},
		},
		{
			name: "system",
			payload: map[string]any{
				"model": "group/rules-group",
				"messages": []map[string]string{
					{"role": "system", "content": "You are a principal cloud architect."},
					{"role": "user", "content": "hi"},
				},
			},
		},
		{
			name: "prompt_regex",
			payload: map[string]any{
				"model":    "group/rules-group",
				"messages": []map[string]string{{"role": "user", "content": "Please DEPLOY PROD now"}},
			},
		},
		{
			name: "tokens",
			payload: map[string]any{
				"model":    "group/rules-group",
				"messages": []map[string]string{{"role": "user", "content": strings.Repeat("long token text ", 30)}},
			},
		},
		{
			name: "model_when",
			payload: map[string]any{
				"model":    "custom-alias-pro",
				"messages": []map[string]string{{"role": "user", "content": "hi"}},
			},
		},
	} {
		callsA.Store(0)
		callsB.Store(0)
		rec := postAppChatWithPayload(t, h, tc.payload, nil)
		if rec.Code != http.StatusOK || callsB.Load() != 1 || callsA.Load() != 0 {
			t.Fatalf("rule case %s expected mB (A=0, B=1), got status=%d A=%d B=%d body=%s", tc.name, rec.Code, callsA.Load(), callsB.Load(), rec.Body.String())
		}
	}

	// Verify vision rule evaluation across OpenAI image_url, Responses input_image, and Anthropic image blocks.
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	groups := effectiveGroups(cfg, nil)
	var rulesGroup routerconfig.Group
	for _, g := range groups {
		if g.ID == "rules-group" {
			rulesGroup = g
			break
		}
	}
	for _, visionBody := range []string{
		`{"model":"group/rules-group","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
		`{"model":"group/rules-group","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
		`{"model":"group/rules-group","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
	} {
		promoted, matchedRule := evaluateGroupRules(rulesGroup, groups, "chat", []byte(visionBody), rulesGroup.Members)
		if matchedRule == nil || len(promoted) == 0 || promoted[0] != mB {
			t.Fatalf("expected vision rule to match %s for body %s, got rule=%v promoted=%v", mB, visionBody, matchedRule, promoted)
		}
	}
}
