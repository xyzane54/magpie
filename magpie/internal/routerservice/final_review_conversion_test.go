package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerrequest"
)

func requireFinalReviewInput(t *testing.T, source []byte) *routerrequest.Prepared {
	t.Helper()
	if err := routercloud.ValidateInput("anthropic", source); err != nil {
		t.Fatalf("fixture rejected at actual input boundary: %v", err)
	}
	prepared, err := routerrequest.Normalize(routerrequest.Input{RequestID: "review", AppID: "loomy", Protocol: "anthropic", Operation: "generate", OriginalBody: source, Context: context.Background()})
	if err != nil {
		t.Fatalf("fixture rejected at normalization boundary: %v", err)
	}
	return prepared
}

func TestFinalReviewToolReplyPreservesTruncation(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"sample.txt\"}"}}]},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
	w := httptest.NewRecorder()
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := writeLocalResponse(w, response, "anthropic", []byte(`{"stream":false}`), modelWriterForTest(w)); err != nil {
		// Explicit rejection before output is also safe: the caller can use the
		// existing pre-output fallback rather than misreport a complete tool turn.
		if w.Body.Len() != 0 {
			t.Fatalf("rejection committed output: %s", w.Body.String())
		}
		return
	}
	var result struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "max_tokens" {
		t.Fatalf("truncated tool reply became %q instead of max_tokens: %s", result.StopReason, w.Body.String())
	}
}

func TestFinalReviewParallelToolRestrictionIsPreservedOrRejected(t *testing.T) {
	source := []byte(`{"model":"client","max_tokens":32,"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"read the file"}],"tools":[{"name":"read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`)
	requireFinalReviewInput(t, source)
	converted, err := localExecutionBody("anthropic", source, "local-model")
	if err != nil {
		return
	} // Existing capability-mismatch fallback is allowed.
	var request map[string]json.RawMessage
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	if string(request["parallel_tool_calls"]) != "false" {
		t.Fatalf("accepted request lost disable_parallel_tool_use=true: %s", converted)
	}
}

func TestFinalReviewToolFailureHistoryIsNotSilentlyConverted(t *testing.T) {
	source := []byte(`{"model":"client","max_tokens":32,"messages":[{"role":"user","content":"read the file"},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"path":"sample.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","is_error":true,"content":"permission denied"}]}],"tools":[{"name":"read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`)
	requireFinalReviewInput(t, source)
	converted, err := localExecutionBody("anthropic", source, "local-model")
	if err == nil {
		// The shared CloudCodec explicitly rejects an IsError tool result when
		// the target protocol cannot express it. The local seam must preserve
		// that rejection instead of turning a failed result into an ordinary one.
		t.Fatalf("accepted failed tool result as ordinary Chat history: %s", converted)
	}
}

func TestFinalReviewMultipleToolResultsAndFollowupSurvive(t *testing.T) {
	source := []byte(`{"model":"client","max_tokens":32,"messages":[{"role":"user","content":"read both files"},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"path":"one.txt"}},{"type":"tool_use","id":"call-2","name":"read","input":{"path":"two.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"first-result"},{"type":"tool_result","tool_use_id":"call-2","content":"second-result"},{"type":"text","text":"compare both results"}]}],"tools":[{"name":"read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`)
	prepared := requireFinalReviewInput(t, source)
	view, err := json.Marshal(prepared.Judgment())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first-result", "second-result", "compare both results"} {
		if !bytes.Contains(view, []byte(value)) {
			t.Errorf("judgment history lost %q: %s", value, view)
		}
	}
	converted, err := localExecutionBody("anthropic", source, "local-model")
	if err != nil {
		return
	}
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	followup := false
	for _, message := range request.Messages {
		if message.Role == "tool" {
			results[message.ToolCallID] = message.Content
		}
		if message.Role == "user" && message.Content == "compare both results" {
			followup = true
		}
	}
	if results["call-1"] != "first-result" || results["call-2"] != "second-result" || !followup {
		t.Fatalf("accepted conversion lost tool results or user followup: %s", converted)
	}
}

func TestFinalReviewStructuredOutputIsPreservedOrRejected(t *testing.T) {
	source := []byte(`{"model":"client","max_tokens":32,"messages":[{"role":"user","content":"answer with a status"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}}}}`)
	requireFinalReviewInput(t, source)
	converted, err := localExecutionBody("anthropic", source, "local-model")
	if err != nil {
		return
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	if len(request["response_format"]) == 0 {
		t.Fatalf("accepted structured output request lost its response format: %s", converted)
	}
}

func TestFinalReviewResponsesControlsUseSharedCodec(t *testing.T) {
	source := []byte(`{"model":"client","input":[{"role":"user","content":"answer with a status"}],"parallel_tool_calls":false,"text":{"format":{"type":"json_schema","name":"status","schema":{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}}},"tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	if err := routercloud.ValidateInput("responses", source); err != nil {
		t.Fatal(err)
	}
	converted, err := localExecutionBody("responses", source, "local-model")
	if err != nil {
		return
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	if string(request["parallel_tool_calls"]) != "false" || len(request["response_format"]) == 0 {
		t.Fatalf("Responses controls were lost: %s", converted)
	}
}

func TestNonChatStreamFastFailsBeforeLocalInference(t *testing.T) {
	cases := []struct {
		protocol string
		body     []byte
	}{
		{
			protocol: "anthropic",
			body:     []byte(`{"model":"profile-pinned","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			protocol: "responses",
			body:     []byte(`{"model":"profile-pinned","stream":true,"input":[{"role":"user","content":"hello"}]}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			if _, err := localExecutionBody(tc.protocol, tc.body, "local-model"); err == nil || err.Error() != "local_protocol_stream_conversion_unavailable" {
				t.Fatalf("expected local_protocol_stream_conversion_unavailable from localExecutionBody, got %v", err)
			}
			cloudCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cloudCalls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
			}))
			defer upstream.Close()
			prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, false, tc.body)
			defer cancel()
			invoker := &scriptedInvoker{response: localChatResponse()}
			w := httptest.NewRecorder()
			result, err := executeRoutedModel(w, prepared, tc.protocol, tc.body, ModelOptions{
				Routing: &RoutingOptions{Judge: &scriptedJudge{decision: DecisionLocal}, LocalInvoker: invoker},
			}, nil, modelWriterForTest(w))
			if err != nil {
				t.Fatalf("expected fallback to cloud to succeed, got err=%v", err)
			}
			if invoker.calls.Load() != 0 {
				t.Fatalf("expected 0 local invoker calls for non-chat stream request, got %d", invoker.calls.Load())
			}
			if cloudCalls != 1 || result.Destination != "cloud" {
				t.Fatalf("expected 1 cloud call and destination=cloud, got cloudCalls=%d result=%+v", cloudCalls, result)
			}
		})
	}
}

func TestValidEmptyTextLocalReplyConvertsWithoutError(t *testing.T) {
	validEmptyLength := `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`
	validEmptyStop := `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`
	for _, protocol := range []string{"anthropic", "responses"} {
		for _, body := range []string{validEmptyLength, validEmptyStop} {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}
			w := httptest.NewRecorder()
			if err := writeLocalResponse(w, resp, protocol, []byte(`{"stream":false}`), modelWriterForTest(w)); err != nil {
				t.Fatalf("expected valid empty-text local reply to convert for %s, got err=%v", protocol, err)
			}
			if w.Body.Len() == 0 {
				t.Fatalf("expected converted payload for %s", protocol)
			}
		}
		for _, invalid := range []string{`{}`, `{"choices":[]}`, `{"choices":[{}]}`} {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(invalid)),
			}
			w := httptest.NewRecorder()
			if err := writeLocalResponse(w, resp, protocol, []byte(`{"stream":false}`), modelWriterForTest(w)); err == nil {
				t.Fatalf("expected malformed local reply %s to fail conversion for %s", invalid, protocol)
			}
		}
	}
}
