package routerrequest

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
)

func credentialStore(t *testing.T) *routerconfig.Store {
	t.Helper()
	root := t.TempDir()
	store := routerconfig.New(dirs.Layout{Install: root, Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "logs")})
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	key := "fake-bound-provider-secret"
	provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "fixture", Protocol: "chat", BaseURL: "http://127.0.0.1:1/v1", Model: "provider-model", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex", "custom-editor", "loomy"} {
		mode := routerconfig.StoredCredential
		if id == "loomy" {
			mode = routerconfig.ForwardAuthorization
		}
		if _, err := store.SaveApp(routerconfig.AppEdit{ID: id, Name: id, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "fixed-" + id}, CredentialMode: mode}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestStoredRequestsNeedNoClientCredential(t *testing.T) {
	store := credentialStore(t)
	for _, id := range []string{"codex", "custom-editor"} {
		for _, headers := range []struct{ auth, key string }{{}, {"Bearer placeholder-a", ""}, {"", "placeholder-b"}, {"arbitrary-client-value", "different-placeholder"}} {
			req := httptest.NewRequest("POST", "http://localhost/app/"+id+"/v1/chat/completions", nil)
			if headers.auth != "" {
				req.Header.Set("Authorization", headers.auth)
			}
			if headers.key != "" {
				req.Header.Set("x-api-key", headers.key)
			}
			prepared, cancel, err := Prepare(req, store, "fake-management-secret", time.Minute)
			if err != nil {
				t.Fatalf("credential-free preparation failed for %s: %v", id, err)
			}
			state, ok := FromContext(prepared.Context())
			if !ok || state.AppID != id || state.Target.Profile.ID != id || state.Target.Profile.Cloud.Model != "fixed-"+id || state.Target.Provider.Config.Model != "fixed-"+id || state.Target.Provider.APIKey != "fake-bound-provider-secret" || state.Target.Authorization != "" {
				t.Fatal("client credential changed the bound target")
			}
			if prepared.Header.Get("Authorization") != "" || prepared.Header.Get("x-api-key") != "" {
				t.Fatal("client credentials survived the preparation boundary")
			}
			if req.Header.Get("Authorization") != headers.auth || req.Header.Get("x-api-key") != headers.key {
				t.Fatal("preparation mutated caller headers")
			}
			if ClassifyError(state.ValidateModel("another-model")).Code != "target_mismatch" {
				t.Fatal("placeholder changed model selection")
			}
			cancel()
		}
	}
}

func TestCredentialHeaderFormatAndManagementMisuse(t *testing.T) {
	store := credentialStore(t)
	for _, name := range []string{"Authorization", "x-api-key"} {
		req := httptest.NewRequest("POST", "http://localhost/app/codex/v1/responses", nil)
		req.Header.Add(name, "placeholder-a")
		req.Header.Add(name, "placeholder-b")
		_, _, err := Prepare(req, store, "fake-management-secret", time.Minute)
		if err == nil || ClassifyError(err).Code != "invalid_auth_headers" || ClassifyError(err).Status != 400 {
			t.Fatalf("duplicate %s not a format error: %v", name, err)
		}
		req.Header.Set(name, "Bearer fake-management-secret")
		_, _, err = Prepare(req, store, "fake-management-secret", time.Minute)
		if err == nil || ClassifyError(err).Code != "credential_misuse" {
			t.Fatal("management token accepted on model path")
		}
	}
}

func TestLoomyStillRequiresBusinessAuthorization(t *testing.T) {
	store := credentialStore(t)
	for _, auth := range []string{"", "placeholder", "Basic placeholder", "Bearer ", "Bearer invalid value", "Bearer fake-management-secret", "Bearer business-token"} {
		req := httptest.NewRequest("POST", "http://localhost/app/loomy/v1/messages", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("x-api-key", "ordinary-placeholder")
		prepared, cancel, err := Prepare(req, store, "fake-management-secret", time.Minute)
		if auth != "Bearer business-token" {
			if err == nil {
				cancel()
				t.Fatalf("invalid Loomy credential accepted: %q", auth)
			}
			want := "credential_missing"
			if auth == "Bearer fake-management-secret" {
				want = "credential_misuse"
			}
			if ClassifyError(err).Code != want {
				t.Fatalf("Loomy error = %v, want %s", err, want)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		state, _ := FromContext(prepared.Context())
		if state.Target.Authorization != auth || state.Target.Provider.APIKey != "" {
			t.Fatal("Loomy business authorization substituted")
		}
		cancel()
	}
}
