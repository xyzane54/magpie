package routerservice

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

func TestInstalledHostFieldsHTTPDirectAndRoutedJSONAndSSE(t *testing.T) {
	raw, err := os.ReadFile("../routercloud/testdata/host-fields/installed-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			ID, Host string
			Body     json.RawMessage
		}
	}
	if json.Unmarshal(raw, &fixtures) != nil || len(fixtures.Cases) == 0 {
		t.Fatal("invalid host field contract")
	}
	for _, mode := range []string{"direct", "cloud-judgment", "unavailable-judgment"} {
		t.Run(mode, func(t *testing.T) {
			var expected atomic.Value
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wire, _ := io.ReadAll(r.Body)
				var got map[string]any
				if json.Unmarshal(wire, &got) != nil || !reflect.DeepEqual(got, expected.Load()) {
					t.Error("host field changed through product routing/native forwarding")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, cloudHTTPReply("chat", false))
			}))
			defer upstream.Close()
			options := ModelOptions{}
			if mode != "direct" {
				judge := &scriptedJudge{decision: DecisionCloud}
				if mode == "unavailable-judgment" {
					judge.err = ErrJudgmentFailed
				}
				options.Routing = &RoutingOptions{Judge: judge}
			}
			h := cloudHTTPObservedHandler(t, options)
			cloudHTTPBind(t, h, upstream.URL, "chat", "workbuddy", "host-field-provider-fixture")
			draft := providerInputFor(upstream.URL + "/v1")
			key := "loomy-field-provider-fixture"
			draft.Protocol, draft.APIKey = "chat", &key
			provider := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", cloudHTTPManagement, draft))
			if bound := providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{ID: "loomy", Name: "Loomy", CredentialMode: routerconfig.StoredCredential, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "profile-pinned"}}); bound.Code != 200 {
				t.Fatal("stored Loomy fixture binding failed")
			}
			product := httptest.NewServer(h)
			defer product.Close()
			var count int32
			for _, fixture := range fixtures.Cases {
				apps := []string{"workbuddy"}
				if fixture.Host == "loomy" {
					apps = []string{"loomy"}
				} else if fixture.Host == "both" {
					apps = []string{"workbuddy", "loomy"}
				}
				for _, app := range apps {
					for _, stream := range []bool{false, true} {
						var body map[string]any
						_ = json.Unmarshal(fixture.Body, &body)
						body["model"], body["stream"] = "profile-pinned", stream
						expected.Store(body)
						status, header, reply := workBuddySocketCall(t, product, app, body, "")
						count++
						if status != 200 || header.Get("X-Request-ID") == "" || !bytes.Contains(reply, []byte("hello")) || stream && !bytes.Contains(reply, []byte("[DONE]")) {
							t.Fatalf("%s app=%s stream=%v rejected: status=%d", fixture.ID, app, stream, status)
						}
					}
				}
			}
			if calls.Load() != count {
				t.Fatalf("expected one pinned upstream attempt per host request: calls=%d requests=%d", calls.Load(), count)
			}
		})
	}
}
