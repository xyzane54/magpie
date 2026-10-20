package routeracceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

// Fixtures check the cancellation/recovery observer without running a model.
// Only runPrivacyLifecycle supplies the service-owned real Manager.Generate.
func privacyLifecycleFixture(t *testing.T, spec *routerredact.PrivacySpecification, mode string) (privacyRoundtripGenerate, func() routerruntime.Status, *int) {
	t.Helper()
	calls := 0
	pid, active, state := 731, 0, "unknown"
	status := func() routerruntime.Status {
		return routerruntime.Status{PID: pid, Healthy: true, Active: active, Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: state, Fingerprint: spec.RuntimeFingerprint()}}}
	}
	generate := func(ctx context.Context, purpose routerruntime.Purpose, raw []byte, consume func(*http.Response) error) error {
		calls++
		if purpose != routerruntime.Redaction {
			t.Error("lifecycle changed the runtime purpose")
			return errors.New("fixture_wrong_purpose")
		}
		deadline, ok := ctx.Deadline()
		if !ok || deadline.IsZero() {
			t.Error("lifecycle call has no parent budget")
			return errors.New("fixture_unbounded_call")
		}
		var request struct {
			Stream bool           `json:"stream"`
			Kwargs map[string]any `json:"chat_template_kwargs"`
		}
		if json.Unmarshal(raw, &request) != nil || request.Kwargs["enable_thinking"] != false || request.Stream != (calls == 1) {
			t.Error("lifecycle lost stream mode or thinking=false")
			return errors.New("fixture_wrong_request")
		}
		if calls == 3 && mode != "timeout_success" {
			<-ctx.Done()
			return ctx.Err()
		}
		var contentType, responseBody string
		if calls == 1 {
			contentType = "text/event-stream"
			responseBody = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\"},\"finish_reason\":null}]}\n\n"
			if mode == "no_content" {
				responseBody = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
			}
		} else {
			contentType = "application/json"
			content := `{"complete":true,"fields":[{"field":0,"complete":true,"findings":[{"label":"private_person","start":3,"end":5,"text":"李芳","confidence":1}]}]}`
			if mode == "bad_recovery" {
				content = `{"complete":true,"fields":[]}`
			}
			envelope, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}, "usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 10, "total_tokens": 17}})
			responseBody = string(envelope)
		}
		response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(responseBody))}
		defer response.Body.Close()
		err := consume(response)
		if calls == 1 {
			switch mode {
			case "cancel_returns_nil":
				return nil
			case "pid_changes":
				pid++
			case "capability_changes":
				state = "available"
			case "slot_leak":
				active = 1
			}
		}
		return err
	}
	return generate, status, &calls
}

func TestPrivacyLifecycleObservedCancelTimeoutAndImmediateRecoveries(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	generate, status, calls := privacyLifecycleFixture(t, spec, "valid")
	directory := t.TempDir()
	report, err := runPrivacyLifecycleWith(context.Background(), generate, status, spec, directory)
	if err != nil {
		t.Fatalf("lifecycle failed: %s (%v)", report.ErrorCode, err)
	}
	if *calls != 4 || !report.Cancel.ContentObserved || !report.Cancel.CancelIssued || !report.Cancel.ReturnedCanceled || !report.Cancel.SlotReleased || !report.Cancel.SamePID || report.Cancel.HTTPStatus != 200 || !report.Recovery.Parsed || report.Recovery.FinishReason != "stop" || report.Recovery.HTTPStatus != 200 || report.Recovery.ParseCode != "" || report.Recovery.Usage["prompt_tokens"] != 7 || !report.Timeout.DeadlineObserved || report.Timeout.BudgetMS != 100 || !report.Timeout.SlotReleased || !report.Timeout.SamePID || !report.TimeoutRecovery.Parsed || !report.TimeoutRecovery.SlotReleased || !report.TimeoutRecovery.SamePID || !report.RedactionCapabilityUnknown {
		t.Fatalf("incomplete lifecycle: %+v", report)
	}
	safe, err := json.Marshal(report)
	if err != nil || bytes.Contains(safe, []byte("李芳")) || bytes.Contains(safe, []byte("姓名")) {
		t.Fatal("safe lifecycle report contained raw content")
	}
	files, err := filepath.Glob(filepath.Join(directory, "synthetic-only", "lifecycle-*"))
	if err != nil || len(files) != 3 {
		t.Fatal("raw synthetic lifecycle evidence was not isolated")
	}
}

func TestPrivacyLifecycleDoesNotInferCancellationOrRecovery(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	for _, mode := range []string{"no_content", "cancel_returns_nil", "pid_changes", "capability_changes", "slot_leak", "bad_recovery", "timeout_success"} {
		t.Run(mode, func(t *testing.T) {
			generate, status, calls := privacyLifecycleFixture(t, spec, mode)
			report, err := runPrivacyLifecycleWith(context.Background(), generate, status, spec, "")
			if err == nil || report.ErrorCode == "" {
				t.Fatal("lifecycle failure was silently accepted")
			}
			if mode == "no_content" && (report.Cancel.ContentObserved || report.Cancel.CancelIssued || report.Cancel.ReturnedCanceled) {
				t.Fatal("completed empty stream became observed active cancellation")
			}
			if mode == "cancel_returns_nil" && report.Cancel.ReturnedCanceled {
				t.Fatal("nil result became returned cancellation")
			}
			if mode == "bad_recovery" && (report.Recovery.Parsed || report.Recovery.ParseCode != "field_coverage") {
				t.Fatal("incomplete model result became recovered extraction")
			}
			if mode == "timeout_success" && report.Timeout.DeadlineObserved {
				t.Fatal("fast successful response became observed timeout")
			}
			wantCalls := 1
			if mode == "bad_recovery" {
				wantCalls = 2
			}
			if mode == "timeout_success" {
				wantCalls = 3
			}
			if *calls != wantCalls {
				t.Fatal("lifecycle continued after an unverified state")
			}
		})
	}
}

func TestPrivacyLifecycleUnknownCapabilityAndFingerprintAreRequired(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	called := false
	generate := func(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error {
		called = true
		return nil
	}
	status := func() routerruntime.Status {
		return routerruntime.Status{PID: 731, Healthy: true, Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: "different"}}}
	}
	report, err := runPrivacyLifecycleWith(context.Background(), generate, status, spec, "")
	if err == nil || called || report.ErrorCode != "privacy_lifecycle_runtime_state" {
		t.Fatal("wrong fingerprint reached model generation")
	}
}
