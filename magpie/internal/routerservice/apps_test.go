package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
)

func TestApplicationManagementAndModelBoundary(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	token := "management-token-for-apps"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, auth := range []string{"", "placeholder-a", "placeholder-b"} {
			if w := providerRequest(t, h, method, "/api/apps", auth, nil); w.Code != 401 {
				t.Fatalf("unprotected %s: %d", method, w.Code)
			}
		}
	}
	list := providerRequest(t, h, "GET", "/api/apps", token, nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"connectionTest":true`) {
		t.Fatal(list.Body.String())
	}
	var data struct{ Apps []routerconfig.AppProfile }
	if json.Unmarshal(list.Body.Bytes(), &data) != nil || len(data.Apps) != 6 {
		t.Fatal("missing presets")
	}
	p, err := store.SaveCloudProvider(providerInputFor("http://127.0.0.1:1/v1"))
	if err != nil {
		t.Fatal(err)
	}
	entry := "client-placeholder"
	in := routerconfig.AppEdit{ID: "codex", Name: "Codex", Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "profile-model"}, CredentialMode: "stored"}
	w := providerRequest(t, h, "POST", "/api/apps", token, in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved struct{ App routerconfig.AppProfile }
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	for _, secret := range []string{entry, "test-cloud-api-key", token, "entryCredentialRef", "sha256"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("management leaked a secret")
		}
	}
	in.Revision = 0
	if w := providerRequest(t, h, "POST", "/api/apps", token, in); w.Code != 409 {
		t.Fatal("stale save", w.Code)
	}
	in.Revision = saved.App.Revision
	in.Name = token
	if w := providerRequest(t, h, "POST", "/api/apps", token, in); w.Code != 403 || strings.Contains(w.Body.String(), token) {
		t.Fatal("management credential accepted/leaked")
	}
	bad := map[string]any{"id": "custom", "name": "custom", "credentialMode": "stored", "unknown": true}
	if w := providerRequest(t, h, "POST", "/api/apps", token, bad); w.Code != 400 {
		t.Fatal("unknown input accepted")
	}
	for _, field := range []string{"entryKey", "clearEntryKey", "hasEntryKey", "entryCredentialRef"} {
		bad := map[string]any{"id": "custom", "name": "custom", "credentialMode": "stored", field: true}
		if w := providerRequest(t, h, "POST", "/api/apps", token, bad); w.Code != 400 {
			t.Fatalf("obsolete input %s accepted", field)
		}
	}
	raw := httptest.NewRequest("POST", "http://127.0.0.1:13145/api/apps", strings.NewReader(strings.Repeat("x", 129<<10)))
	raw.RemoteAddr = "127.0.0.1:2345"
	raw.Header.Set("Authorization", "Bearer "+token)
	raw.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, raw)
	if rw.Code != 400 {
		t.Fatal("oversized input accepted")
	}
	for _, tc := range []struct {
		path, auth string
		status     int
		code       string
	}{
		{"/app/codex/v1/responses", entry, 400, "unsupported_input"},
		{"/app/codex/v1/models", entry, 200, "profile-model"},
		{"/app/codex/v1/responses", "other-placeholder", 400, "unsupported_input"},
		{"/app/codex/v1/responses", "", 400, "unsupported_input"},
		{"/app/codex/v1/responses", token, 403, "credential_misuse"},
		{"/app/claude-code/v1/messages", "", 409, "select an application cloud provider and model"},
		{"/app/unregistered/v1/responses", "", 404, "app_not_registered"},
		{"/app/%63odex/v1/responses", entry, 400, "invalid_app_path"},
		{"/app/codex%2f..%2floomy/v1/responses", entry, 400, "invalid_app_path"},
		{"/app/codex/v1//responses", entry, 400, "invalid_app_path"},
		{"/app/codex/v1/../models", entry, 400, "invalid_app_path"},
		{"/app/codex/v1/v1/messages", entry, 404, "application endpoint is not supported"},
	} {
		method := "POST"
		if strings.HasSuffix(tc.path, "/models") {
			method = "GET"
		}
		w := providerRequest(t, h, method, tc.path, tc.auth, nil)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Request-ID") == "" {
			t.Fatal("error lacks request correlation")
		}
		if strings.Contains(w.Body.String(), entry) || strings.Contains(w.Body.String(), "test-cloud-api-key") {
			t.Fatal("business error leaked credential")
		}
	}
	for _, origin := range []string{"null", "https://foreign.invalid", "http://127.0.0.1:2222"} {
		for _, path := range []string{"/api/apps", "/app/codex/v1/responses"} {
			r := httptest.NewRequest("GET", "http://127.0.0.1:13145"+path, nil)
			r.RemoteAddr = "127.0.0.1:2345"
			r.Header.Set("Origin", origin)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("foreign origin accepted")
			}
		}
	}
	if w := providerRequest(t, h, "DELETE", "/api/cloud-providers", token, map[string]any{"id": p.ID, "revision": p.Revision}); w.Code != 400 {
		t.Fatal("deleted referenced provider")
	}
	if w := providerRequest(t, h, "DELETE", "/api/apps", token, map[string]any{"id": "codex", "revision": saved.App.Revision}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	list = providerRequest(t, h, "GET", "/api/apps", token, nil)
	if list.Code != 200 {
		t.Fatal(list.Body.String())
	}
	_ = json.Unmarshal(list.Body.Bytes(), &data)
	for _, app := range data.Apps {
		if app.ID == "codex" && app.Configured {
			t.Fatal("preset reset retained private target")
		}
	}
	// Invalid persisted Profile must fail closed, never become an empty app list.
	before, _ := os.ReadFile(layout.ConfigFile())
	if err := os.WriteFile(layout.ConfigFile(), []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if w := providerRequest(t, h, "GET", "/api/apps", token, nil); w.Code != 503 {
		t.Fatal("corruption reported success")
	}
	after, _ := os.ReadFile(layout.ConfigFile())
	if bytes.Equal(before, after) {
		t.Fatal("test failed to corrupt fixture")
	}
}
func TestPreparedRequestPinsTargetAndPropagatesCancellation(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	draft := providerInputFor("http://127.0.0.1:1/v1")
	p, err := store.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	entry := "client-placeholder"
	_, err = store.SaveApp(routerconfig.AppEdit{ID: "codex", Name: "Codex", Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "fixed-model"}, CredentialMode: "stored"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	req := httptest.NewRequest("POST", "http://127.0.0.1:13145/app/codex/v1/responses", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+entry)
	prepared, cancel, err := routerrequest.Prepare(req, store, "management", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	state, ok := routerrequest.FromContext(prepared.Context())
	if !ok || state.ID == "" || state.AppID != "codex" || state.Target.Provider.Config.Model != "fixed-model" || state.Attempts != 0 || state.OutputCommitted || state.Remaining() <= 0 {
		t.Fatal("missing request state")
	}
	if err := state.ValidateModel("arbitrary-model"); err == nil {
		t.Fatal("host overrode Profile target")
	}
	if err := state.ValidateModel("fixed-model"); err != nil {
		t.Fatal(err)
	}
	draft.ID = p.ID
	draft.Revision = p.Revision
	draft.Model = "updated-provider-model"
	key := "new-cloud-api-key"
	draft.APIKey = &key
	if _, err := store.SaveCloudProvider(draft); err != nil {
		t.Fatal(err)
	}
	if state.Target.Provider.APIKey != "test-cloud-api-key" || state.Target.Provider.Config.Model != "fixed-model" {
		t.Fatal("in-flight target changed")
	}
	if err := state.BeginCloudAttempt(); err != nil {
		t.Fatal(err)
	}
	state.CommitOutput()
	if err := state.BeginCloudAttempt(); err == nil {
		t.Fatal("retry after output")
	}
	state.OutputCommitted = false
	if err := state.BeginCloudAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCloudAttempt(); err == nil {
		t.Fatal("third attempt allowed")
	}
	cancelParent()
	if !state.Canceled() || !errors.Is(state.Context.Err(), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if err := state.BeginCloudAttempt(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if e := routerrequest.ClassifyError(context.DeadlineExceeded); e.Code != "budget_exhausted" {
		t.Fatal(e)
	}
}

func TestPrepareEveryPresetAndCustomApplication(t *testing.T) {
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	provider, err := store.SaveCloudProvider(providerInputFor("http://127.0.0.1:1/v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex", "claude-code", "loomy", "astudio", "workbuddy", "dsh", "custom-editor"} {
		key := "placeholder-for-" + id
		mode := routerconfig.StoredCredential
		in := routerconfig.AppEdit{ID: id, Name: id, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "fixed-" + id}, CredentialMode: mode}
		if id == "loomy" {
			in.CredentialMode = routerconfig.ForwardAuthorization
			key = "dynamic-loomy-authorization"
		}
		if _, err := store.SaveApp(in); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		req := httptest.NewRequest("POST", "http://localhost/app/"+id+"/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		prepared, cancel, err := routerrequest.Prepare(req, store, "management-token", time.Minute)
		if err != nil {
			t.Fatalf("prepare %s: %v", id, err)
		}
		state, ok := routerrequest.FromContext(prepared.Context())
		if !ok || state.AppID != id || state.Target.Profile.ID != id || state.Target.Provider.Config.Model != "fixed-"+id {
			t.Fatalf("identity/target mismatch for %s", id)
		}
		if id == "loomy" {
			if state.Target.Authorization != "Bearer "+key || state.Target.Provider.APIKey != "" {
				t.Fatal("Loomy credential mode lost")
			}
		} else if state.Target.Authorization != "" || state.Target.Provider.APIKey != "test-cloud-api-key" {
			t.Fatal("stored credential mode lost")
		}
		cancel()
	}
}
