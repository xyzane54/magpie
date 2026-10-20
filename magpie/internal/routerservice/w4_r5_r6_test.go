package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/routerrequest"
)

// R5 guards the boundary between protocol adapters and the local runtime.
// The runtime is an OpenAI-compatible chat endpoint, so Responses input must
// be represented as messages before it reaches LocalInvoker. The original
// Responses envelope remains available to the cloud branch through the
// Prepared request and is not mutated by this conversion.
func TestR5ResponsesRequestConvertedBeforeLocalChat(t *testing.T) {
	body := []byte(`{"model":"profile-pinned","instructions":"You are terse","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},{"type":"function_call","call_id":"call-1","name":"run","arguments":"{\"cmd\":\"pwd\"}"},{"type":"function_call_output","call_id":"call-1","output":"ok"}],"tools":[{"type":"function","name":"run","description":"run a command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), "http://127.0.0.1:1", false, body)
	defer cancel()
	invoker := &scriptedInvoker{response: localChatResponse()}
	w := httptest.NewRecorder()
	result, err := executeRoutedModel(w, prepared, "responses", body, ModelOptions{Routing: &RoutingOptions{Judge: &scriptedJudge{decision: DecisionLocal}, LocalInvoker: invoker}}, nil, modelWriterForTest(w))
	if err != nil || result.Destination != string(DecisionLocal) {
		t.Fatalf("responses local result=%+v err=%v", result, err)
	}
	var local map[string]json.RawMessage
	if err := json.Unmarshal(invoker.request, &local); err != nil {
		t.Fatalf("local body is not JSON: %v; body=%s", err, invoker.request)
	}
	if _, ok := local["input"]; ok {
		t.Fatalf("Responses input leaked into local Chat body: %s", invoker.request)
	}
	if _, ok := local["instructions"]; ok {
		t.Fatalf("Responses instructions leaked into local Chat body: %s", invoker.request)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(local["messages"], &messages); err != nil || len(messages) < 3 {
		t.Fatalf("local body has no complete chat history: %s", invoker.request)
	}
	if !bytes.Contains(invoker.request, []byte("run it")) || !bytes.Contains(invoker.request, []byte("call-1")) || !bytes.Contains(invoker.request, []byte("pwd")) || !bytes.Contains(invoker.request, []byte("ok")) {
		t.Fatalf("local conversion lost Responses text/tool semantics: %s", invoker.request)
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(local["tools"], &tools); err != nil || len(tools) != 1 {
		t.Fatalf("local conversion lost tool definitions: %s", invoker.request)
	}
}

// R6 is deliberately a strict protocol-substitute test. A Go struct assigned
// directly to message.content is encoded as a JSON object, which is not valid
// Chat text content. The judgment transport must send protocol text or an
// explicitly supported text content block. This test accepts either form and
// validates the text itself as the serialized judgment view.
func TestR6JudgmentUserContentIsProtocolText(t *testing.T) {
	view := routerrequest.JudgmentView{RequestID: "req-r6", AppID: "codex", Protocol: "responses", Operation: "generate", Model: "local", PromptVersion: "p", History: []routerrequest.JudgmentMessage{{Role: "user", Text: "hello"}}}
	body, err := jsonMarshalJudgment(view, "p")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ChatTemplateKwargs map[string]bool `json:"chat_template_kwargs"`
		Messages           []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Messages) != 2 {
		t.Fatalf("invalid judgment envelope: %s", body)
	}
	if value, ok := envelope.ChatTemplateKwargs["enable_thinking"]; !ok || value {
		t.Fatalf("judgment must force enable_thinking=false: %s", body)
	}
	if envelope.Messages[1].Role != "user" {
		t.Fatalf("judgment user message missing: %+v", envelope.Messages)
	}
	text := protocolTextContent(t, envelope.Messages[1].Content)
	var got routerrequest.JudgmentView
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("judgment user.content is not serialized protocol text: %q (%v)", text, err)
	}
	if got.RequestID != view.RequestID || got.Protocol != view.Protocol || len(got.History) != 1 || got.History[0].Text != "hello" {
		t.Fatalf("judgment content changed semantic view: got=%+v want=%+v", got, view)
	}
}

func TestR5AnthropicRequestConvertedBeforeLocalChat(t *testing.T) {
	body := []byte(`{"model":"profile-pinned","system":"You are terse","messages":[{"role":"user","content":"run it"},{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"run","input":{"cmd":"pwd"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-a","content":[{"type":"text","text":"ok"}]}]}],"max_tokens":32,"tools":[{"name":"run","description":"run","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), "http://127.0.0.1:1", false, body)
	defer cancel()
	invoker := &scriptedInvoker{response: localChatResponse()}
	w := httptest.NewRecorder()
	if _, err := executeRoutedModel(w, prepared, "anthropic", body, ModelOptions{Routing: &RoutingOptions{Judge: &scriptedJudge{decision: DecisionLocal}, LocalInvoker: invoker}}, nil, modelWriterForTest(w)); err != nil {
		t.Fatal(err)
	}
	var local map[string]json.RawMessage
	if err := json.Unmarshal(invoker.request, &local); err != nil {
		t.Fatal(err)
	}
	if local["messages"] == nil || local["system"] != nil || !bytes.Contains(invoker.request, []byte("call-a")) || !bytes.Contains(invoker.request, []byte("tool_call_id")) {
		t.Fatalf("Anthropic envelope was not converted with tool history: %s", invoker.request)
	}
}

func protocolTextContent(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			t.Fatal("empty judgment user.content")
		}
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		t.Fatalf("user.content must be text or text blocks, got %s", raw)
	}
	var out bytes.Buffer
	for _, block := range blocks {
		if block.Type != "text" && block.Type != "input_text" && block.Type != "output_text" || block.Text == "" {
			t.Fatalf("unsupported judgment content block: %+v", block)
		}
		out.WriteString(block.Text)
	}
	return out.String()
}
