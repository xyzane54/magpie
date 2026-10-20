package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestLocalCapabilityChecksValidateJudgmentAndToolLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		messages, _ := request["messages"].([]any)
		content := `{"decision":"local"}`
		if strings.Contains(string(body), "labeled cloud") {
			content = `{"decision":"cloud"}`
		}
		if _, ok := request["tools"]; ok {
			content = ""
			response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "call-check", "type": "function", "function": map[string]string{"name": localToolName, "arguments": `{"path":"README.md"}`}}}}}}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		if len(messages) > 2 || strings.Contains(string(body), `"tool_call_id"`) || strings.Contains(string(body), localTextAnswer) {
			content = localTextAnswer
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()

	judgment, err := runJudgmentCapabilityCheck(context.Background(), server.URL, localModelID, "judgment-suite")
	if err != nil || !strings.HasPrefix(judgment, "judgment-suite:") {
		t.Fatalf("judgment check failed: %q %v", judgment, err)
	}
	execution, err := runExecutionCapabilityCheck(context.Background(), server.URL, localModelID, "execution-suite")
	if err != nil || !strings.HasPrefix(execution, "execution-suite:") {
		t.Fatalf("execution check failed: %q %v", execution, err)
	}
}

func TestLocalCapabilityChecksRejectUnsafeResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"not a decision"}}]}`)
	}))
	defer server.Close()
	if _, err := runJudgmentCapabilityCheck(context.Background(), server.URL, localModelID, "suite"); err == nil {
		t.Fatal("invalid judgment response accepted")
	}
	if _, err := runExecutionCapabilityCheck(context.Background(), server.URL, localModelID, "suite"); err == nil {
		t.Fatal("invalid execution response accepted")
	}
}

func TestLocalCapabilitySuitesBindIdentity(t *testing.T) {
	identity := routerruntime.LocalCallIdentity{ModelID: localModelID, ModelFingerprint: strings.Repeat("a", 64), RuntimeFingerprint: strings.Repeat("b", 64)}
	checks := localCapabilityChecks(identity, defaultPromptVersion)
	if len(checks) != 2 || !strings.Contains(checks[0].Suite, identity.ModelFingerprint) || !strings.Contains(checks[1].Suite, identity.RuntimeFingerprint) {
		t.Fatalf("capability suites are not bound: %+v", checks)
	}
}

func TestLocalSSEUsageIsCapturedWithoutBuffering(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))}
	w := httptest.NewRecorder()
	out := modelWriterForTest(w)
	var usage *cloudcodec.ReportedUsage
	if err := writeLocalResponseObserved(w, response, "chat", []byte(`{"stream":true}`), out, &usage); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 0 || usage.OutputTokens == nil || *usage.OutputTokens != 2 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("stream usage/output mismatch: usage=%+v body=%s", usage, w.Body.String())
	}
}
