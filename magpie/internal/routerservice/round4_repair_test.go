package routerservice

import (
	"context"
	"encoding/json"
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

func TestRound4TrialCheckRecordsEachRequestAndNeedsExplicitAuthorization(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		t.Run(modelID, func(t *testing.T) {
			testLocalModelTrialChecks(t, modelID)
		})
	}
}

func testLocalModelTrialChecks(t *testing.T, modelID string) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil || request.Model != modelID {
			t.Errorf("check used model %q instead of selected %q: %v", request.Model, modelID, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		text := string(body)
		content := `{"decision":"local"}`
		if strings.Contains(text, "labeled cloud") {
			content = `{"decision":"cloud"}`
		}
		if strings.Contains(text, localTextAnswer) {
			content = localTextAnswer
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(text, `"tools"`) {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"fixture-call","type":"function","function":{"name":"router_check","arguments":"{\"path\":\"README.md\"}"}}]}}],"usage":{"prompt_tokens":13,"completion_tokens":2,"total_tokens":15}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}}, "usage": map[string]int{"prompt_tokens": 13, "completion_tokens": 2, "total_tokens": 15}})
	}))
	defer server.Close()

	layout, _ := fixture(t)
	store := routerconfig.New(layout)
	local, err := newLocalModels(context.Background(), layout, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH), Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: server.URL}})
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
	var events []routerruntime.LocalCallObservation
	local.Manager = manager
	local.Observer = routerruntime.LocalCallObserverFunc(func(v routerruntime.LocalCallObservation) { events = append(events, v) })
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/local-model/check", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	localModelAPI(response, req, local)
	if response.Code != http.StatusOK || requests.Load() != 5 {
		t.Fatalf("checks did not run their actual requests: code=%d requests=%d body=%s", response.Code, requests.Load(), response.Body.String())
	}
	if len(events) != int(requests.Load()) {
		t.Fatalf("expected one event per model request: requests=%d events=%d", requests.Load(), len(events))
	}
	seenIDs := map[string]bool{}
	for _, event := range events {
		if !event.Check || event.RequestID != "" || event.Mode != routerruntime.LocalCallDiagnostic || !event.Attempted || event.CallID == "" || seenIDs[event.CallID] || len(event.MaterialVersion) > 128 || !strings.Contains(event.Suite, manager.Identity().ModelFingerprint) || event.ModelID != modelID {
			t.Fatalf("unsafe or incomplete check observation: %+v", event)
		}
		seenIDs[event.CallID] = true
		if event.Usage == nil || event.Usage.InputTokens == nil || *event.Usage.InputTokens != 13 || event.Usage.OutputTokens == nil || *event.Usage.OutputTokens != 2 {
			t.Fatalf("check usage was not retained: %+v", event.Usage)
		}
	}
	encoded, _ := json.Marshal(events)
	t.Logf("ROUND4_REPAIR_EVENTS=%s", encoded)
	for _, purpose := range []routerruntime.Purpose{routerruntime.Judgment, routerruntime.Execution} {
		capability := manager.Status().Capabilities[purpose]
		if capability.State == "available" || capability.TrialReady {
			t.Fatalf("engineering check unexpectedly became formal/ready before authorization: %+v", capability)
		}
	}
	if err := manager.EnableTrial(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if !status.Intent.Trial.Judgment || !status.Intent.Trial.Execution || !status.Capabilities[routerruntime.Judgment].TrialReady || !status.Capabilities[routerruntime.Execution].TrialReady {
		t.Fatalf("explicit trial authorization was not active: %+v", status)
	}
	payload, _ := json.Marshal(map[string]any{"model": modelID, "messages": []map[string]string{{"role": "user", "content": localTextAnswer}}})
	if err := manager.Invoke(context.Background(), routerruntime.Execution, payload, func(response *http.Response) error {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		reply, err := parseLocalReply(body)
		if err != nil {
			return err
		}
		if reply.Text != localTextAnswer {
			t.Errorf("unexpected local answer: %+v", reply)
		}
		return nil
	}); err != nil {
		t.Fatalf("selected model could not execute after trial authorization: %v", err)
	}
	if err := manager.DisableTrial(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(); status.Intent.Trial.Judgment || status.Intent.Trial.Execution || status.Capabilities[routerruntime.Judgment].TrialReady || status.Capabilities[routerruntime.Execution].TrialReady {
		t.Fatalf("trial disable was not persisted: %+v", status)
	}
	// Switch after the API captures its check identity, before either check
	// acquires the slot. Neither old probe may reach the newly selected model.
	otherModelID := "spark-x2.5-4B"
	if modelID == otherModelID {
		otherModelID = localModelID
	}
	local.checkFactory = func(identity routerruntime.LocalCallIdentity, prompt string) []routerruntime.Check {
		checks := localCapabilityChecks(identity, prompt)
		if err := manager.Disable(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := manager.Select(context.Background(), otherModelID); err != nil {
			t.Fatal(err)
		}
		if err := manager.Enable(context.Background()); err != nil {
			t.Fatal(err)
		}
		return checks
	}
	beforeSwitch := requests.Load()
	response = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/local-model/check", strings.NewReader(`{}`))
	localModelAPI(response, req, local)
	if response.Code != http.StatusConflict || requests.Load() != beforeSwitch {
		t.Fatalf("stale checks reached a different model: code=%d requests=%d before=%d", response.Code, requests.Load(), beforeSwitch)
	}
	if err := manager.EnableTrial(context.Background()); err == nil {
		t.Fatal("newly selected model inherited previous trial evidence")
	}
}
