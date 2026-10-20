package routercloud

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
)

// Exercise the real preparation/executor seam without wiring inference into
// routerservice. Product HTTP integration is covered separately by
// routerservice/TestCloudHTTP*; this regression keeps the preparation seam local.
func TestPreparedAppCredentialsAtLoopbackUpstream(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				key := "fake-key-codex"
				if r.URL.Path == "/custom/v1/chat/completions" || r.URL.Path == "/custom/v1/responses" || r.URL.Path == "/custom/v1/messages" {
					key = "fake-key-custom"
				}
				if protocol == "anthropic" {
					if r.Header.Get("x-api-key") != key || r.Header.Get("Authorization") != "" {
						t.Error("upstream did not receive only the bound Anthropic key")
					}
				} else if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("x-api-key") != "" {
					t.Error("upstream did not receive only the bound provider Bearer key")
				}
				raw, _ := io.ReadAll(r.Body)
				var body struct{ Model string }
				if json.Unmarshal(raw, &body) != nil || body.Model != strings.TrimPrefix(key, "fake-key-")+"-model" {
					t.Error("placeholder changed selected model")
				}
				for _, secret := range []string{"placeholder-a", "placeholder-b", "fake-management-secret"} {
					if strings.Contains(string(raw), secret) || strings.Contains(r.Header.Get("Authorization"), secret) || strings.Contains(r.Header.Get("x-api-key"), secret) {
						t.Error("client or management credential reached upstream")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, whole(protocol, true))
			}))
			defer upstream.Close()
			root := t.TempDir()
			store := routerconfig.New(dirs.Layout{Install: root, Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "logs")})
			if _, err := store.Initialize(); err != nil {
				t.Fatal(err)
			}
			for _, app := range []string{"codex", "custom"} {
				key := "fake-key-" + app
				// Saved auth overrides must not defeat the explicitly bound provider key.
				headers := map[string]string{"Authorization": "Bearer ignored-saved-header", "x-api-key": "ignored-saved-header"}
				provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: app, Protocol: protocol, BaseURL: upstream.URL + "/" + app + "/v1", Model: "provider-default", APIKey: &key, Headers: &headers, ProxyMode: "direct"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.SaveApp(routerconfig.AppEdit{ID: app, Name: app, CredentialMode: routerconfig.StoredCredential, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: app + "-model"}}); err != nil {
					t.Fatal(err)
				}
				for _, auth := range []struct{ bearer, key string }{{}, {"Bearer placeholder-a", ""}, {"", "placeholder-b"}, {"Bearer placeholder-a", "placeholder-b"}} {
					req := httptest.NewRequest("POST", "http://localhost/app/"+app+"/v1/chat/completions", nil)
					if auth.bearer != "" {
						req.Header.Set("Authorization", auth.bearer)
					}
					if auth.key != "" {
						req.Header.Set("x-api-key", auth.key)
					}
					prepared, cancel, err := routerrequest.Prepare(req, store, "fake-management-secret", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					state, _ := routerrequest.FromContext(prepared.Context())
					if state.Target.Profile.ID != app || state.Target.Provider.Config.ID != provider.ID {
						t.Fatal("placeholder selected a different app/provider")
					}
					body := []byte(strings.ReplaceAll(string(input(protocol, false)), "caller-cannot-select-another-target", app+"-model"))
					out, err := (Executor{}).Execute(prepared.Context(), httptest.NewRecorder(), Request{Protocol: protocol, Body: body, Target: Target{Provider: state.Target.Provider, Model: state.Target.Profile.Cloud.Model, Authorization: state.Target.Authorization}})
					cancel()
					if err != nil || out.Attempts != 1 {
						t.Fatalf("local execution failed: %v", err)
					}
				}
			}
			if calls.Load() != 8 {
				t.Fatalf("upstream calls = %d", calls.Load())
			}
		})
	}
}

func TestLoomyBusinessCredentialAtLoopbackUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "" {
			t.Error("ordinary or provider key substituted for Loomy credential")
		}
		if r.Header.Get("Authorization") == "Bearer revoked-business-token" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer valid-business-token" {
			t.Error("business Authorization was changed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, whole("chat", true))
	}))
	defer upstream.Close()
	root := t.TempDir()
	store := routerconfig.New(dirs.Layout{Install: root, Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "logs")})
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	key := "fake-provider-must-not-be-used"
	provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "Loomy gateway", Protocol: "chat", BaseURL: upstream.URL + "/v1", Model: "business-model", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveApp(routerconfig.AppEdit{ID: "loomy", Name: "Loomy", CredentialMode: routerconfig.ForwardAuthorization, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "business-model"}}); err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"", "ordinary-placeholder", "Bearer valid-business-token", "Bearer revoked-business-token"} {
		req := httptest.NewRequest("POST", "http://localhost/app/loomy/v1/chat/completions", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("x-api-key", "ordinary-placeholder")
		prepared, cancel, err := routerrequest.Prepare(req, store, "fake-management-secret", time.Minute)
		if auth == "" || auth == "ordinary-placeholder" {
			if err == nil {
				cancel()
				t.Fatal("Loomy accepted missing/invalid business authorization")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		state, _ := routerrequest.FromContext(prepared.Context())
		_, err = (Executor{}).Execute(prepared.Context(), httptest.NewRecorder(), Request{Protocol: "chat", Body: input("chat", false), Target: Target{Provider: state.Target.Provider, Model: state.Target.Profile.Cloud.Model, Authorization: state.Target.Authorization}})
		cancel()
		if auth == "Bearer revoked-business-token" {
			failure, ok := err.(*Error)
			if !ok || failure.Code != "cloud_auth_error" || failure.Status != 401 {
				t.Fatalf("revoked Loomy credential did not retain upstream failure: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("missing/invalid Loomy credential reached upstream: calls=%d", calls.Load())
	}
}
