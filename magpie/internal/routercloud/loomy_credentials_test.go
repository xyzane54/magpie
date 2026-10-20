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

func TestLoomyCredentialStoredAtLoopbackUpstream(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !strings.HasPrefix(r.URL.Path, "/loomy/v1/") {
					t.Error("Loomy request escaped its explicitly bound provider")
				}
				if protocol == "anthropic" {
					if r.Header.Get("x-api-key") != "fake-loomy-provider-key" || r.Header.Get("Authorization") != "" {
						t.Error("host credential replaced the stored Anthropic key")
					}
				} else if r.Header.Get("Authorization") != "Bearer fake-loomy-provider-key" || r.Header.Get("x-api-key") != "" {
					t.Error("host credential replaced the stored provider Bearer key")
				}
				var body struct{ Model string }
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "loomy-stored-model" {
					t.Error("host credentials changed the bound model")
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
			for _, app := range []string{"loomy", "other-agent"} {
				key := "fake-loomy-provider-key"
				if app != "loomy" {
					key = "fake-other-provider-key"
				}
				headers := map[string]string{"Authorization": "Bearer fake-saved-override", "x-api-key": "fake-saved-override"}
				p, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: app, Protocol: protocol, BaseURL: upstream.URL + "/" + app + "/v1", APIKey: &key, Headers: &headers, ProxyMode: "direct"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.SaveApp(routerconfig.AppEdit{ID: app, Name: app, CredentialMode: routerconfig.StoredCredential, Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: app + "-stored-model"}}); err != nil {
					t.Fatalf("stored Profile rejected: %v", err)
				}
			}
			for _, headers := range [][2]string{{"", ""}, {"Bearer fake-session", ""}, {"", "fake-host-key"}, {"Bearer fake-other-provider-key", "fake-host-key"}} {
				r := httptest.NewRequest("POST", "http://localhost/app/loomy/v1/chat/completions", nil)
				if headers[0] != "" {
					r.Header.Set("Authorization", headers[0])
				}
				if headers[1] != "" {
					r.Header.Set("x-api-key", headers[1])
				}
				prepared, cancel, err := routerrequest.Prepare(r, store, "fake-management-secret", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				state, _ := routerrequest.FromContext(prepared.Context())
				payload := []byte(strings.ReplaceAll(string(input(protocol, false)), "caller-cannot-select-another-target", "loomy-stored-model"))
				_, err = (Executor{}).Execute(prepared.Context(), httptest.NewRecorder(), Request{Protocol: protocol, Body: payload, Target: Target{Provider: state.Target.Provider, Model: state.Target.Profile.Cloud.Model, Authorization: state.Target.Authorization}})
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 4 {
				t.Fatalf("expected four explicit Loomy requests, got %d", calls.Load())
			}
		})
	}
}
