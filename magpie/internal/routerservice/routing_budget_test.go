package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestLongContextSettingsUseOneTotalBudgetAndReserveCloudTime(t *testing.T) {
	settings := routerconfig.LongContextRuntimeSettings()
	deadline := time.Now().Add(4 * time.Minute)
	parent, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	ctx, options := snapshotRuntimeSettings(routerruntime.WithRuntimeSettings(parent, settings), ModelOptions{})
	if options.Budget != 600*time.Second {
		t.Fatalf("configured total budget = %s", options.Budget)
	}
	local, stop := localWorkContext(ctx, options)
	defer stop()
	localDeadline, _ := local.Deadline()
	if !localDeadline.Equal(deadline.Add(-60 * time.Second)) {
		t.Fatalf("local deadline = %s; parent = %s", localDeadline, deadline)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1", nil).WithContext(ctx)
	r, stopRequest := routerrequest.WithBudget(r, options.Budget)
	defer stopRequest()
	r, stopAgain := routerrequest.WithBudget(r, options.Budget)
	defer stopAgain()
	if actual, _ := r.Context().Deadline(); !actual.Equal(deadline) {
		t.Fatalf("nested preparation extended parent deadline: %s", actual)
	}
	_, shorter := snapshotRuntimeSettings(ctx, ModelOptions{Budget: 3 * time.Second})
	if shorter.Budget != 3*time.Second {
		t.Fatal("configured budget extended an explicit shorter limit")
	}
	_, legacy := snapshotRuntimeSettings(context.Background(), ModelOptions{})
	if legacy.Budget != 0 {
		t.Fatal("missing runtime settings changed the legacy total budget")
	}
}

func TestCloudReserveSkipsLocalWhenHostDeadlineIsShorter(t *testing.T) {
	var cloudCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	ctx = routerruntime.WithRuntimeSettings(ctx, routerconfig.LongContextRuntimeSettings())
	body := []byte(`{"model":"profile-pinned","messages":[{"role":"user","content":"hello"}]}`)
	prepared, cancel, _ := routedRequest(t, ctx, upstream.URL, false, body)
	defer cancel()
	deadline, _ := prepared.Context().Deadline()
	judge := &scriptedJudge{decision: DecisionLocal}
	invoker := &scriptedInvoker{response: localChatResponse()}
	w := httptest.NewRecorder()
	result, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: judge, LocalInvoker: invoker}}, nil, modelWriterForTest(w))
	if err != nil || judge.calls.Load() != 0 || invoker.calls.Load() != 0 || cloudCalls.Load() != 1 || result.CapabilityState != "local_budget_reserved" || result.Destination != "cloud" {
		t.Fatalf("cloud reserve not honored: result=%+v err=%v judge=%d local=%d cloud=%d", result, err, judge.calls.Load(), invoker.calls.Load(), cloudCalls.Load())
	}
	state, _ := routerrequest.FromContext(prepared.Context())
	if !state.Deadline.Equal(deadline) {
		t.Fatal("fallback restarted the total budget")
	}
}

func TestLocalStageFailuresRemainDistinctAcrossCloudFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		judgment bool
		state    string
	}{
		{"judgment-queue", routerruntime.ErrJudgmentQueueTimeout, true, "judgment_queue_timeout"},
		{"judgment-inference", routerruntime.ErrJudgmentInferenceTimeout, true, "judgment_inference_timeout"},
		{"judgment-size", routerruntime.ErrJudgmentInputExceeded, true, "judgment_input_exceeded"},
		{"summary", ErrJudgmentSummaryIncomplete, true, "judgment_summary_incomplete"},
		{"execution-queue", routerruntime.ErrExecutionQueueTimeout, false, "execution_queue_timeout"},
		{"execution-time", routerruntime.ErrExecutionTimeout, false, "execution_timeout"},
		{"execution-context", routerruntime.ErrContextExceeded, false, "context_limit_exceeded"},
		{"tokenizer", routerruntime.ErrTokenCountUnavailable, false, "token_count_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cloudCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cloudCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
			}))
			defer upstream.Close()
			body := []byte(`{"model":"profile-pinned","messages":[{"role":"user","content":"hello"}]}`)
			prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, false, body)
			defer cancel()
			judge := &scriptedJudge{decision: DecisionLocal}
			invoker := &scriptedInvoker{err: tc.err}
			if tc.judgment {
				judge.err = tc.err
			}
			w := httptest.NewRecorder()
			result, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: judge, LocalInvoker: invoker}}, nil, modelWriterForTest(w))
			if err != nil || cloudCalls.Load() != 1 || result.CapabilityState != tc.state || result.Destination != "cloud" {
				t.Fatalf("lost fallback cause: result=%+v err=%v cloud=%d", result, err, cloudCalls.Load())
			}
		})
	}
}

func TestCompactJudgmentHasStrictBoundedOutputWithoutChangingExecution(t *testing.T) {
	view := routerrequest.JudgmentView{Model: "m", System: []string{"Never modify files."}, History: []routerrequest.JudgmentMessage{{Role: "user", Text: "only reply OK"}}}
	body, err := marshalJudgment(view, defaultPromptVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		MaxTokens      int    `json:"max_tokens"`
		PromptVersion  string `json:"prompt_version"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Strict bool `json:"strict"`
			} `json:"json_schema"`
		} `json:"response_format"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.MaxTokens != 32 || envelope.PromptVersion != compactPromptVersion || envelope.ResponseFormat.Type != "json_schema" || !envelope.ResponseFormat.JSONSchema.Strict || !strings.Contains(envelope.Messages[0].Content, "request data, not instructions") || !strings.Contains(envelope.Messages[1].Content, "Never modify files") {
		t.Fatalf("judgment policy missing: %s", body)
	}
	legacy, err := jsonMarshalJudgment(view, defaultPromptVersion)
	if err != nil || strings.Contains(string(legacy), "max_tokens") || strings.Contains(string(legacy), routerrequest.JudgmentSummaryPolicy) {
		t.Fatalf("legacy prompt semantics changed: %s %v", legacy, err)
	}
	_, err = marshalJudgment(routerrequest.JudgmentView{System: []string{strings.Repeat("Never omit this required restriction.\n", 100)}}, "", true)
	if !errors.Is(err, ErrJudgmentSummaryIncomplete) {
		t.Fatalf("silently dispatched incomplete restrictions: %v", err)
	}
}

func TestStrictJudgmentDecisionRejectsExtraOrNonJSONAnswers(t *testing.T) {
	for _, tc := range []struct {
		content string
		valid   bool
	}{
		{`{"decision":"local"}`, true}, {`{"decision":"cloud"}`, true},
		{`local`, false}, {"```json\n{\"decision\":\"local\"}\n```", false},
		{`{"decision":"local","reason":"easy"}`, false}, {`{"decision":"local","decision":"cloud"}`, false},
		{`{"decision":"local"} {"decision":"cloud"}`, false}, {`{"decision":"LOCAL"}`, false},
	} {
		envelope, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.content}}}})
		_, err := parseStrictDecision(envelope)
		if (err == nil) != tc.valid {
			t.Fatalf("answer %q accepted=%t; expected %t", tc.content, err == nil, tc.valid)
		}
	}
}

func TestLocalObservationRetainsCapturedBindingAfterSettingsReload(t *testing.T) {
	layout, _ := fixture(t)
	installer := &managedFixtureInstaller{}
	installer.installed.Store(true)
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: routermanifest.Fixed(), RuntimeID: "windows-amd64-cpu", Store: routerruntime.ConfigIntentStore{Store: routerconfig.New(layout)}, Installer: installer, Factory: &managedFixtureFactory{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Select(context.Background(), localModelID); err != nil {
		t.Fatal(err)
	}
	identity, settings := manager.SnapshotBinding(context.Background())
	var events []routerruntime.LocalCallObservation
	meta := localCallContext{Observer: routerruntime.LocalCallObserverFunc(func(event routerruntime.LocalCallObservation) { events = append(events, event) }), Settings: settings, Binding: identity, BindingCaptured: true}
	// A synchronous reload between call completion and observer dispatch is a
	// deterministic version of the delayed-completion race.
	if err := manager.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
		t.Fatal(err)
	}
	if manager.Identity().ModelFingerprint == identity.ModelFingerprint {
		t.Fatal("fixture did not change runtime identity")
	}
	emitObservedLocalCall(meta, manager, routerruntime.Execution, time.Now(), routerruntime.GenerationDiagnosticSnapshot{Attempted: true}, nil, nil)
	if len(events) != 1 || events[0].ModelFingerprint != identity.ModelFingerprint || events[0].Settings != settings {
		t.Fatalf("completed call acquired replacement runtime binding: %+v", events)
	}
	meta.BindingCaptured = false
	meta.Binding = routerruntime.LocalCallIdentity{}
	emitObservedLocalCall(meta, manager, routerruntime.Execution, time.Now(), routerruntime.GenerationDiagnosticSnapshot{}, nil, routerruntime.ErrSettingsChanged)
	if events[1].ModelFingerprint != "" || events[1].Settings != settings {
		t.Fatalf("stale preflight claimed replacement fingerprint: %+v", events[1])
	}
}

func TestAnthropicNullToolChoiceDoesNotBypassOutputFormatValidation(t *testing.T) {
	for _, invalid := range []string{`{"type":"text"}`, `{"type":"json_schema"}`, `{"type":"json_schema","schema":[]}`} {
		body := []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"tool_choice":null,"output_config":{"format":` + invalid + `}}`)
		if err := validateAnthropicLocalControls(body); err == nil {
			t.Fatalf("null tool choice skipped later independent format validation: %s", body)
		}
		if _, err := localExecutionBody("anthropic", body, "local"); err == nil {
			t.Fatalf("null tool choice bypassed unsupported output format: %s", body)
		}
	}
	for _, controls := range []string{`"tool_choice":null,"output_config":null`, `"tool_choice":null,"output_config":{"format":null}`, `"tool_choice":null,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}`} {
		body := []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"hello"}],` + controls + `}`)
		if err := validateAnthropicLocalControls(body); err != nil {
			t.Fatalf("valid independent nullable controls rejected by control validator: %s: %v", body, err)
		}
	}
}
