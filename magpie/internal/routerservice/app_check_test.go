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

func checkFixture(t *testing.T, upstream, protocol string, options ModelOptions) (http.Handler, *routerconfig.Store, routerconfig.AppEdit, string) {
	t.Helper()
	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	in := providerInputFor(upstream + "/v1")
	in.Protocol = protocol
	key := "exact-bound-key"
	in.APIKey = &key
	p, err := store.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	return handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store, options), store, routerconfig.AppEdit{ID: "codex", Name: "Codex", CredentialMode: "stored", Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "qwen3.8-flash"}}, layout.ConfigFile()
}

func TestAppCheckDraftAndActualProductExecution(t *testing.T) {
	for _, app := range []string{"codex", "claude-code", "custom-agent"} {
		for _, upstreamProtocol := range []string{"chat", "responses", "anthropic"} {
			t.Run(app+"/"+upstreamProtocol, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					b, _ := io.ReadAll(r.Body)
					var raw map[string]json.RawMessage
					if json.Unmarshal(b, &raw) != nil || string(raw["model"]) != `"qwen3.8-flash"` || string(raw["stream"]) != "true" {
						t.Errorf("incorrect pinned text check: %s", b)
					}
					if r.URL.Path != "/v1/"+cloudHTTPPath(upstreamProtocol) {
						t.Error("incorrect protocol endpoint", r.URL.Path)
					}
					if upstreamProtocol == "anthropic" {
						if r.Header.Get("x-api-key") != "exact-bound-key" || r.Header.Get("Authorization") != "" {
							t.Error("wrong auth")
						}
					} else if r.Header.Get("Authorization") != "Bearer exact-bound-key" || r.Header.Get("x-api-key") != "" {
						t.Error("wrong auth")
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply(upstreamProtocol, false))
				}))
				defer upstream.Close()
				var completion ModelCompletion
				var events []ModelEvent
				h, store, draft, path := checkFixture(t, upstream.URL, upstreamProtocol, ModelOptions{Complete: func(c ModelCompletion) { completion = c }, Observe: func(e ModelEvent) { events = append(events, e) }})
				draft.ID, draft.Name = app, app
				before, _ := os.ReadFile(path)
				w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatalf("check: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
				}
				var verified struct {
					Verified         bool
					ProviderRevision int
					ProviderID       string
					Protocol         string
				}
				if json.Unmarshal(w.Body.Bytes(), &verified) != nil || !verified.Verified || verified.ProviderRevision != 1 || verified.ProviderID != draft.Cloud.ProviderID {
					t.Fatal(w.Body.String())
				}
				want := "chat"
				if app == "codex" {
					want = "responses"
				}
				if app == "claude-code" {
					want = "anthropic"
				}
				if verified.Protocol != want {
					t.Fatal("wrong app wire", verified.Protocol)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("read-only check saved configuration")
				}
				for _, forbidden := range []string{"exact-bound-key", cloudHTTPManagement, "hello", "Bearer"} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Fatal("private response leaked", forbidden)
					}
				}
				if completion.Purpose != "connection_check" || completion.Stage != "completed" || completion.Result.Attempts != 1 || completion.Result.Usage != nil {
					t.Fatalf("bad completion: %+v", completion)
				}
				if len(events) == 0 {
					t.Fatal("missing check observation")
				}
				for _, event := range events {
					if event.Purpose != "connection_check" || event.RequestID != completion.RequestID || event.AppID != app {
						t.Fatal("check observation mixed with user inference", event)
					}
				}
				apps, _ := store.Apps()
				for _, p := range apps {
					if p.ID == app && p.Configured {
						t.Fatal("check configured app")
					}
				}
				draft.ExpectedProviderRevision = verified.ProviderRevision
				w = providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, draft)
				if w.Code != 200 {
					t.Fatal("save verified draft", w.Code, w.Body.String())
				}
				body := cloudHTTPInput(want, true, false)
				body["model"] = draft.Cloud.Model
				events = nil
				actual := cloudHTTPCall(t, h, context.Background(), app, want, body, "Bearer client-placeholder", "client-api-key-placeholder")
				if actual.Code != 200 || calls.Load() != 2 {
					t.Fatal("actual product generation failed", actual.Code, actual.Body.String())
				}
				if completion.Purpose != "generation" {
					t.Fatal("check changed following inference purpose", completion.Purpose)
				}
				for _, event := range events {
					if event.Purpose != "generation" {
						t.Fatal("check changed following observation purpose", event.Purpose)
					}
				}
			})
		}
	}
}

func TestAppCheckRejectsListedButUnusableModelWithoutSaving(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `cmaas-qwen3.8-flash`) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"invalid model private upstream body"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	h, _, draft, path := checkFixture(t, upstream.URL, "chat", ModelOptions{})
	before, _ := os.ReadFile(path)
	draft.Cloud.Model = "cmaas-qwen3.8-flash"
	w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
	if w.Code != 400 || calls.Load() != 1 || !strings.Contains(w.Body.String(), "cloud_parameter_error") || strings.Contains(w.Body.String(), "private upstream body") {
		t.Fatal(w.Code, w.Body.String(), calls.Load())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed check wrote config")
	}
	draft.Cloud.Model = "qwen3.8-flash"
	if w = providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
