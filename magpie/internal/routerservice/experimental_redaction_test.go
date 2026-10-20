package routerservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestExperimentalProductionAdmissionPreservesUnknownAndBlocksInvalidResults(t *testing.T) {
	layout, address := fixture(t)
	store := routerconfig.New(layout)
	var invalid atomic.Bool
	var generated, upstream atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generated.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"enable_thinking":false`) {
			t.Error("trial enabled thinking")
		}
		end := 25
		if invalid.Load() {
			end++
		}
		content := fmt.Sprintf(`{"complete":true,"fields":[{"field":0,"complete":true,"findings":[{"label":"private_email","start":3,"end":%d,"text":"synthetic@example.test","confidence":1}]}]}`, end)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": content}}}})
	}))
	defer local.Close()
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1) // Count before checking, including any failed request.
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "synthetic@example.test") || !strings.Contains(string(body), "<EMAIL:1>") {
			t.Error("trial sent an unredacted synthetic value")
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<EMAIL:1>\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"<EMAIL:1>"},"finish_reason":"stop"}]}`)
	}))
	defer cloud.Close()
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH), Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: local.URL}, ExperimentalRedaction: true})
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
	localModels := &LocalModels{Manager: manager, Layout: layout, Catalog: catalog}
	redactor := newRuntimeRedactor(localModels, true)
	defer redactor.close()
	options := ModelOptions{ExperimentalRedaction: true, LocalRuntime: localModels, Routing: &RoutingOptions{Judge: RuntimeJudge{Manager: manager, PromptVersion: defaultPromptVersion}, LocalInvoker: manager, Redactor: redactor, PromptVersion: defaultPromptVersion}}
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "management-token", store, options)
	call := func(method, path, body string, management bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:2345"
		req.Header.Set("Content-Type", "application/json")
		if management {
			req.Header.Set("Authorization", "Bearer management-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	status := call("GET", "/api/redaction", "", true)
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"capability":"experimental"`) || !strings.Contains(status.Body.String(), `"recognitionReady":false`) || !strings.Contains(status.Body.String(), `"budgetMs":30000`) {
		t.Fatal("trial projection unavailable", status.Code, status.Body.String())
	}
	if w := call("POST", "/api/redaction", `{"edits":[{"id":"workbuddy","revision":0,"enabled":true}]}`, true); w.Code != 200 {
		t.Fatal("trial intent could not be enabled", w.Code)
	}
	key := "fixture-key"
	provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "trial fixture", Protocol: "chat", BaseURL: cloud.URL + "/v1", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := store.Apps()
	var revision int
	for _, app := range apps {
		if app.ID == "workbuddy" {
			revision = app.Revision
		}
	}
	edit, _ := json.Marshal(routerconfig.AppEdit{ID: "workbuddy", Revision: revision, Name: "WorkBuddy", Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "trial-model"}, CredentialMode: routerconfig.StoredCredential, RedactionEnabled: true})
	if w := call("POST", "/api/apps", string(edit), true); w.Code != 200 {
		t.Fatal("trial profile could not be saved", w.Code, w.Body.String())
	}
	for _, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":"trial-model","stream":%t,"messages":[{"role":"user","content":"邮箱：synthetic@example.test"}]}`, stream)
		w := call("POST", "/app/workbuddy/v1/chat/completions", body, false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic@example.test") {
			t.Fatal("trial did not restore response", stream, w.Code, w.Body.String())
		}
	}
	if upstream.Load() != 2 || generated.Load() != 2 {
		t.Fatal("trial transport escaped the unique manager", upstream.Load(), generated.Load())
	}
	invalid.Store(true)
	w := call("POST", "/app/workbuddy/v1/chat/completions", `{"model":"trial-model","messages":[{"role":"user","content":"邮箱：synthetic@example.test"}]}`, false)
	if w.Code == 200 || upstream.Load() != 2 {
		t.Fatal("invalid trial result reached upstream", w.Code, upstream.Load())
	}
	if err := manager.Require(routerruntime.Redaction); err == nil || manager.Status().Capabilities[routerruntime.Redaction].State != "unknown" {
		t.Fatal("trial forged formal acceptance")
	}
	defaultRedactor := newRuntimeRedactor(localModels)
	defer defaultRedactor.close()
	if _, _, err := defaultRedactor.current(); err != nil {
		t.Fatal(err)
	}
	if defaultRedactor.state().CloudActive {
		t.Fatal("startup opt-in was inherited by ordinary redactor")
	}
}
