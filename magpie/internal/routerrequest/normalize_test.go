package routerrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizePreservesExecutionBytesAndBuildsCompactChatView(t *testing.T) {
	body := []byte(`{"model":"profile","messages":[{"role":"system","content":"fixed system instructions"},{"role":"user","content":"read a file"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]},{"role":"tool","tool_call_id":"call-1","content":"contents from a.txt"}],"tools":[{"type":"function","function":{"name":"read_file","description":"Read one file","parameters":{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}}}]}`)
	original := append([]byte(nil), body...)
	p, err := Normalize(Input{RequestID: "req-1", AppID: "workbuddy", Protocol: "chat", Operation: "generate", Model: "profile", PromptVersion: "draft-1", OriginalBody: body, Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.OriginalBody(), original) || !bytes.Equal(p.ExecutionBody(), original) {
		t.Fatal("normalization changed execution bytes")
	}
	body[0] = 'X'
	if !bytes.Equal(p.ExecutionBody(), original) {
		t.Fatal("prepared request aliases caller body")
	}
	execution := p.ExecutionBody()
	execution[0] = 'Y'
	if !bytes.Equal(p.ExecutionBody(), original) {
		t.Fatal("execution accessor returned mutable storage")
	}
	view := p.Judgment()
	if len(view.System) != 1 || view.System[0] != "fixed system instructions" || len(view.History) != 3 || len(view.Tools) != 1 {
		t.Fatalf("unexpected judgment view: %+v", view)
	}
	if view.History[1].Role != "assistant" || len(view.History[1].ToolCalls) != 1 || string(view.History[1].ToolCalls[0].Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("assistant tool call was not preserved: %+v", view.History[1])
	}
	if view.History[2].ToolResult == nil || view.History[2].ToolResult.ID != "call-1" || view.History[2].ToolResult.Text != "contents from a.txt" {
		t.Fatalf("tool result was not preserved: %+v", view.History[2])
	}
	if len(view.Tools[0].Parameters) != 2 || view.Tools[0].Parameters[0].Name != "limit" || !view.Tools[0].Parameters[1].Required {
		t.Fatalf("tool summary is not stable/structured: %+v", view.Tools[0])
	}
	view.History[1].ToolCalls[0].Arguments[0] = 'X'
	if string(p.Judgment().History[1].ToolCalls[0].Arguments) != `{"path":"a.txt"}` {
		t.Fatal("judgment accessor returned mutable raw arguments")
	}
}

func TestNormalizeSupportsResponsesAndAnthropicToolHistory(t *testing.T) {
	cases := []struct {
		name, protocol, body               string
		wantSystem, wantHistory, wantTools int
	}{
		{"responses", "responses", `{"model":"m","instructions":"fixed","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call","call_id":"c1","name":"run","arguments":"{\"cmd\":\"pwd\"}"},{"type":"function_call_output","call_id":"c1","output":"/tmp"}],"tools":[{"type":"function","name":"run","description":"run command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`,
			1, 3, 1},
		{"anthropic", "anthropic", `{"model":"m","system":[{"type":"text","text":"fixed"}],"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"run","input":{"cmd":"pwd"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"/tmp"}]}]}],"max_tokens":32,"tools":[{"name":"run","description":"run command","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`,
			1, 3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Normalize(Input{RequestID: "req-" + tc.name, AppID: "codex", Protocol: tc.protocol, Operation: "generate", OriginalBody: []byte(tc.body), Context: context.Background()})
			if err != nil {
				t.Fatal(err)
			}
			v := p.Judgment()
			if len(v.System) != tc.wantSystem || len(v.History) != tc.wantHistory || len(v.Tools) != tc.wantTools {
				t.Fatalf("view counts = %d/%d/%d, want %d/%d/%d: %+v", len(v.System), len(v.History), len(v.Tools), tc.wantSystem, tc.wantHistory, tc.wantTools, v)
			}
			if len(v.History[1].ToolCalls) != 1 || v.History[1].ToolCalls[0].ID != "c1" || v.History[2].ToolResult == nil || v.History[2].ToolResult.ID != "c1" {
				t.Fatalf("tool interaction was not normalized: %+v", v.History)
			}
		})
	}
}

func TestNormalizePreservesMultipleAnthropicToolResultsAndFollowupOrder(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read both"},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"path":"one"}},{"type":"tool_use","id":"call-2","name":"read","input":{"path":"two"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"first","is_error":false},{"type":"tool_result","tool_use_id":"call-2","content":"second","is_error":true},{"type":"text","text":"compare them"}]}]}`)
	p, err := Normalize(Input{RequestID: "req-multi-results", AppID: "codex", Protocol: "anthropic", Operation: "generate", OriginalBody: body, Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	view := p.Judgment()
	if len(view.History) != 3 {
		t.Fatalf("unexpected history length: %+v", view.History)
	}
	last := view.History[2]
	if last.ToolResult == nil || last.ToolResult.ID != "call-1" {
		t.Fatalf("compatibility first result missing: %+v", last)
	}
	if len(last.ToolResults) != 2 || last.ToolResults[0].ID != "call-1" || last.ToolResults[1].ID != "call-2" || !last.ToolResults[1].IsError {
		t.Fatalf("multiple results or error marker lost: %+v", last)
	}
	if string(last.ToolResults[0].Content) != `"first"` || string(last.ToolResults[1].Content) != `"second"` {
		t.Fatalf("original result content was not retained: %+v", last.ToolResults)
	}
	if last.Text != "compare them" {
		t.Fatalf("mixed text was not retained: %q", last.Text)
	}
	if len(last.Parts) != 3 || last.Parts[0].ToolResult == nil || last.Parts[1].ToolResult == nil || last.Parts[2].Text != "compare them" {
		t.Fatalf("mixed content order was not retained: %+v", last.Parts)
	}
	if last.Parts[0].ToolResult.ID != "call-1" || last.Parts[1].ToolResult.ID != "call-2" {
		t.Fatalf("tool result order was not retained: %+v", last.Parts)
	}
}

func TestNormalizeRejectsRemoteHistoryReferenceBeforeExecution(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","previous_response_id":"resp-1","input":"hello"}`,
		`{"model":"m","conversation":"conv-1","input":"hello"}`,
		`{"model":"m","input":[{"type":"item_reference","id":"item-1"}]}`,
	} {
		p, err := Normalize(Input{RequestID: "req-history", AppID: "codex", Protocol: "responses", Operation: "generate", OriginalBody: []byte(body), Context: context.Background()})
		if p != nil || err == nil || !errors.Is(err, ErrRemoteHistory) {
			t.Fatalf("remote history accepted: p=%v err=%v", p, err)
		}
		if got := ClassifyError(err); got.Code != "remote_history_unsupported" || got.Status != 501 {
			t.Fatalf("wrong remote history classification: %+v", got)
		}
	}
}

func TestNormalizeCancellationIsObservedWithoutCallingValidator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := Normalize(Input{RequestID: "req-canceled", AppID: "codex", Protocol: "chat", Operation: "generate", OriginalBody: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), Context: ctx})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input normalized: p=%v err=%v", p, err)
	}
}

func TestJudgmentViewJSONDoesNotContainCompleteToolSchema(t *testing.T) {
	p, err := Normalize(Input{RequestID: "req-json", AppID: "codex", Protocol: "chat", Operation: "generate", OriginalBody: []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"run","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string","description":"long command details"}}}}}]}`), Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p.Judgment())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("long command details")) {
		t.Fatalf("judgment view leaked full schema: %s", b)
	}
}
