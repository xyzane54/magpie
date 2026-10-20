package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

type integrationInstaller struct{}

func (integrationInstaller) Verify(context.Context, routermanifest.Model, routermanifest.Runtime) (routerprocess.Spec, error) {
	return routerprocess.Spec{}, nil
}
func (integrationInstaller) Install(context.Context, routermanifest.Model, routermanifest.Runtime, func(routerruntime.Progress)) (routerprocess.Spec, error) {
	return routerprocess.Spec{}, nil
}

type integrationProcess struct {
	endpoint string
	done     chan struct{}
	once     sync.Once
}

func (p *integrationProcess) Endpoint() string             { return p.endpoint }
func (p *integrationProcess) PID() int                     { return 9117 }
func (p *integrationProcess) Done() <-chan struct{}        { return p.done }
func (p *integrationProcess) Err() error                   { return nil }
func (p *integrationProcess) Health(context.Context) error { return nil }
func (p *integrationProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type integrationFactory struct{ endpoint string }

func (f integrationFactory) Start(context.Context, routerprocess.Spec) (routerprocess.Process, error) {
	return &integrationProcess{endpoint: f.endpoint, done: make(chan struct{})}, nil
}

func TestProductionHTTPW4W5UsesTargetWireAndRestoresClientResponse(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		for _, experimental := range []bool{false, true} {
			t.Run(modelID+map[bool]string{false: "/formal", true: "/experimental"}[experimental], func(t *testing.T) {
				layout, address := fixture(t)
				store := routerconfig.New(layout)

				var localBodies [][]byte
				local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					localBodies = append(localBodies, append([]byte(nil), body...))
					var request struct {
						Model    string `json:"model"`
						Messages []struct {
							Content string `json:"content"`
						} `json:"messages"`
					}
					_ = json.Unmarshal(body, &request)
					content := `{"decision":"cloud"}`
					if len(request.Messages) > 1 {
						var classify struct {
							Fields []struct {
								Field int    `json:"field"`
								Text  string `json:"text"`
							} `json:"fields"`
						}
						if json.Unmarshal([]byte(request.Messages[1].Content), &classify) == nil && len(classify.Fields) > 0 {
							if request.Model != modelID {
								t.Error("redaction request used a different model", request.Model)
							}
							fields := make([]any, 0, len(classify.Fields))
							for _, field := range classify.Fields {
								findings := []any{}
								if field.Text == "Alice" {
									findings = append(findings, map[string]any{"label": "private_person", "start": 0, "end": 5, "text": "Alice", "confidence": 0.99})
								}
								fields = append(fields, map[string]any{"field": field.Field, "complete": true, "findings": findings})
							}
							b, _ := json.Marshal(map[string]any{"complete": true, "fields": fields})
							content = string(b)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(content) + `},"finish_reason":"stop"}]}`))
				}))
				defer local.Close()

				var cloudBody []byte
				cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					cloudBody, _ = io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<PERSON:1>"},"finish_reason":"stop"}]}`))
				}))
				defer cloud.Close()

				catalog := routermanifest.Fixed()
				runtimeID := routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH)
				manager, err := routerruntime.New(context.Background(), routerruntime.Options{
					Catalog: catalog, RuntimeID: runtimeID, ExperimentalRedaction: experimental,
					Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: local.URL},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer manager.Close(context.Background())
				if err := manager.Select(context.Background(), modelID); err != nil {
					t.Fatal(err)
				}
				if err := manager.Enable(context.Background()); err != nil {
					t.Fatal(err)
				}
				spec, err := routerredact.NewPrivacySpecification(routerredact.ModelBinding{ModelID: modelID, RuntimeID: runtimeID})
				if experimental {
					spec, err = routerredact.NewExperimentalPrivacySpecification(routerredact.ModelBinding{ModelID: modelID, RuntimeID: runtimeID})
				}
				if err != nil {
					t.Fatal(err)
				}
				checks := []routerruntime.Check{
					{Purpose: routerruntime.Judgment, Suite: "judgment-engineering-v1", Run: func(context.Context, string) (string, error) { return "judgment-fixture", nil }},
					{Purpose: routerruntime.Execution, Suite: "execution-engineering-v1", Run: func(context.Context, string) (string, error) { return "execution-fixture", nil }},
				}
				if !experimental {
					checks = append(checks, routerruntime.Check{Purpose: routerruntime.Redaction, Suite: spec.CapabilitySuite(), Run: func(context.Context, string) (string, error) { return "redaction-fixture", nil }})
				}
				for _, check := range checks {
					if err := manager.CheckCapability(context.Background(), check); err != nil {
						t.Fatal(err)
					}
				}

				key := "test-key"
				provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "loopback", Protocol: "chat", BaseURL: cloud.URL + "/v1", APIKey: &key, ProxyMode: "direct"})
				if err != nil {
					t.Fatal(err)
				}
				app, err := store.SaveAppWithRedactionCapabilityContext(context.Background(), routerconfig.AppEdit{
					ID: "codex", Name: "Codex", Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "profile-model"}, CredentialMode: routerconfig.StoredCredential, RedactionEnabled: true,
				}, routerconfig.RedactionAvailable)
				if err != nil {
					t.Fatal(err)
				}
				if !app.RedactionEnabled {
					t.Fatal("redaction intent was not persisted")
				}

				redactor := newRuntimeRedactor(&LocalModels{Manager: manager, Layout: layout, Catalog: catalog}, experimental)
				defer redactor.close()
				h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "management-token", store, ModelOptions{
					ExperimentalRedaction: experimental,
					LocalRuntime:          &LocalModels{Manager: manager, Layout: layout, Catalog: catalog},
					Routing:               &RoutingOptions{Judge: RuntimeJudge{Manager: manager, PromptVersion: defaultPromptVersion}, LocalInvoker: manager, Redactor: redactor, PromptVersion: defaultPromptVersion},
				})
				req := httptest.NewRequest(http.MethodPost, "http://"+address+"/app/codex/v1/chat/completions", strings.NewReader(`{"model":"profile-model","messages":[{"role":"user","content":"Alice"}]}`))
				req.RemoteAddr = "127.0.0.1:2345"
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Alice") {
					t.Fatalf("integrated request was not restored: %d %s", w.Code, w.Body.String())
				}
				if strings.Contains(string(cloudBody), "Alice") || !strings.Contains(string(cloudBody), "<PERSON:1>") {
					t.Fatalf("raw entity reached cloud: %s", cloudBody)
				}
				if len(localBodies) < 2 {
					t.Fatalf("expected judgment and redaction local calls, got %d", len(localBodies))
				}
				for _, body := range localBodies {
					if !strings.Contains(string(body), `"enable_thinking":false`) {
						t.Fatalf("local call enabled thinking: %s", body)
					}
				}
				if redactor.Stats().Processed == 0 {
					t.Fatal("production pipeline did not report a processed request")
				}
				if experimental {
					capability := manager.Status().Capabilities[routerruntime.Redaction]
					if manager.Require(routerruntime.Redaction) == nil || capability.State != "unknown" || capability.Ready {
						t.Fatal("experimental redaction accepted a formal capability", capability)
					}
				}
			})
		}
	}
}
