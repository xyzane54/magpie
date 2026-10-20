package routerservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func providerRequest(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:13145"+path, bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func providerInputFor(base string) routerconfig.ProviderEdit {
	key := "test-cloud-api-key"
	headers := map[string]string{"X-Tenant": "private-tenant"}
	return routerconfig.ProviderEdit{Name: "test provider", Protocol: "chat", BaseURL: base, APIKey: &key, Headers: &headers, ProxyMode: "direct"}
}

func TestProviderHTTPHasNoModelSelection(t *testing.T) {
	layout, _ := fixture(t)
	h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
	in := providerInputFor("https://example.invalid/v1")
	p := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", "management-token", in))
	in.ID, in.Revision, in.Model = p.ID, p.Revision, "ignored-old-client-model"
	in.APIKey, in.Headers = nil, nil
	updated := providerRequest(t, h, "POST", "/api/cloud-providers", "management-token", in)
	providerSaved(t, updated)
	list := providerRequest(t, h, "GET", "/api/cloud-providers", "management-token", nil)
	for _, response := range []*httptest.ResponseRecorder{updated, list} {
		if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte(`"model"`)) || bytes.Contains(response.Body.Bytes(), []byte("ignored-old-client-model")) {
			t.Fatalf("provider model still exposed: %d %s", response.Code, response.Body.String())
		}
	}
}
func providerSaved(t *testing.T, w *httptest.ResponseRecorder) routerconfig.CloudProvider {
	t.Helper()
	var result struct {
		Provider routerconfig.CloudProvider `json:"provider"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Provider.ID == "" {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	return result.Provider
}
func TestCloudProviderManagementAuthenticationAndExplicitModelFetch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-cloud-api-key" || r.Header.Get("X-Tenant") != "private-tenant" {
			t.Errorf("bad upstream request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer upstream.Close()
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	const token = "test-management-only-token"
	h := handler(Info{Address: "127.0.0.1:13145"}, token, store)
	in := providerInputFor(upstream.URL + "/v1")
	for _, path := range []string{"/api/cloud-providers", "/api/cloud-providers/models"} {
		if w := providerRequest(t, h, "POST", path, "", in); w.Code != 401 {
			t.Fatalf("unprotected %s: %d", path, w.Code)
		}
	}
	// Cross-origin writes are rejected before the store or upstream is touched.
	body, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "http://127.0.0.1:13145/api/cloud-providers", bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://foreign.invalid")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(layout.CredentialsFile()); !os.IsNotExist(err) {
		t.Fatal("unauthorized request created credentials")
	}
	saved := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", token, in))
	list := providerRequest(t, h, "GET", "/api/cloud-providers", token, nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), saved.ID) {
		t.Fatal(list.Code, list.Body.String())
	}
	for _, secret := range []string{"test-cloud-api-key", "private-tenant", token} {
		if strings.Contains(list.Body.String(), secret) {
			t.Fatal("management API leaked secret")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("save/list made an unsolicited upstream request")
	}
	before, _ := os.ReadFile(layout.ConfigFile())
	in.ID = saved.ID
	in.Revision = saved.Revision
	in.APIKey = nil
	in.Headers = nil
	models := providerRequest(t, h, "POST", "/api/cloud-providers/models", token, in)
	if models.Code != 200 || !strings.Contains(models.Body.String(), "model-b") || calls.Load() != 1 {
		t.Fatalf("model fetch: %d %s %d", models.Code, models.Body.String(), calls.Load())
	}
	after, _ := os.ReadFile(layout.ConfigFile())
	if !bytes.Equal(before, after) {
		t.Fatal("model fetch saved the draft")
	}
	leaked := providerInputFor(upstream.URL + "/v1")
	leaked.APIKey = ptr(token)
	if w := providerRequest(t, h, "POST", "/api/cloud-providers/models", token, leaked); w.Code != 400 {
		t.Fatal("management token accepted as cloud key")
	}
	if calls.Load() != 1 {
		t.Fatal("management token reached upstream")
	}
	escaped := ""
	for _, r := range token {
		escaped += fmt.Sprintf("\\u%04x", r)
	}
	extra := map[string]json.RawMessage{"private": json.RawMessage(`"` + escaped + `"`)}
	leaked = providerInputFor(upstream.URL + "/v1")
	leaked.ExtraBody = &extra
	if w := providerRequest(t, h, "POST", "/api/cloud-providers/models", token, leaked); w.Code != 400 {
		t.Fatal("escaped management token accepted in extra body")
	}
	if calls.Load() != 1 {
		t.Fatal("escaped management token reached upstream")
	}
	deleted := providerRequest(t, h, "DELETE", "/api/cloud-providers", token, map[string]any{"id": saved.ID, "revision": saved.Revision})
	if deleted.Code != 200 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}
func ptr(s string) *string { return &s }

func TestModelListMissingOpenAIKeyDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "fixture proxy must not be called", 502)
	}))
	defer proxy.Close()
	layout, _ := fixture(t)
	h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
	in := providerInputFor("https://api.openai.com/v1")
	in.APIKey, in.Headers, in.Model = nil, nil, ""
	in.ProxyMode, in.ProxyURL = "custom", ptr(proxy.URL)
	w := providerRequest(t, h, "POST", "/api/cloud-providers/models", "management-token", in)
	if w.Code != 502 || !strings.Contains(w.Body.String(), `"code":"model_key_required"`) || calls.Load() != 0 {
		t.Fatalf("missing key was not rejected locally: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
	if _, err := os.Stat(layout.CredentialsFile()); !os.IsNotExist(err) {
		t.Fatal("missing-key fetch wrote credentials")
	}
}

func TestModelListFailureCodesPreserveManagementAndHideUpstreamSecrets(t *testing.T) {
	var status atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, "private-upstream-content test-cloud-api-key")
	}))
	defer upstream.Close()
	layout, _ := fixture(t)
	h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
	in := providerInputFor(upstream.URL + "/v1")
	for _, tc := range []struct {
		status int
		code   string
	}{{401, "model_list_auth_failed"}, {403, "model_list_forbidden"}, {404, "model_list_unavailable"}, {405, "model_list_unavailable"}, {429, "model_list_rate_limited"}, {503, "model_list_upstream_failed"}, {200, "model_list_invalid_response"}} {
		status.Store(int32(tc.status))
		w := providerRequest(t, h, "POST", "/api/cloud-providers/models", "management-token", in)
		var failure struct{ Code string }
		if json.Unmarshal(w.Body.Bytes(), &failure) != nil || w.Code != 502 || failure.Code != tc.code {
			t.Fatalf("upstream %d: %d %s", tc.status, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "test-cloud-api-key") || strings.Contains(w.Body.String(), "private-upstream-content") {
			t.Fatal("model-list failure exposed upstream content")
		}
		if management := providerRequest(t, h, "GET", "/api/cloud-providers", "management-token", nil); management.Code != 200 {
			t.Fatal("upstream auth failure invalidated local management")
		}
	}
}
