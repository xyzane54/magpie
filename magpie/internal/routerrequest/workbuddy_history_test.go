package routerrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestNormalizeWorkBuddyWebSearchHistory(t *testing.T) {
	body, err := os.ReadFile("../routercloud/testdata/workbuddy/workbuddy-websearch-tool-result.request.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Normalize(Input{RequestID: "req-websearch", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: body, Context: context.Background()})
	if err != nil {
		t.Fatalf("installed WebSearch continuation rejected before routing judgment: %v", err)
	}
	if !bytes.Equal(p.OriginalBody(), body) || !bytes.Equal(p.ExecutionBody(), body) {
		t.Fatal("routing changed WorkBuddy tool history bytes")
	}
	view := p.Judgment()
	if len(view.History) != 3 || len(view.History[1].ToolCalls) != 1 || view.History[2].ToolResult == nil || view.History[2].ToolResult.ID != view.History[1].ToolCalls[0].ID || view.History[2].ToolResult.Text != "fixture-text-12" {
		t.Fatal("routing judgment lost tool content or call/result linkage")
	}
	encoded, _ := json.Marshal(view)
	if bytes.Contains(encoded, []byte("toolResult")) || bytes.Contains(encoded, []byte("fixture-text-13")) {
		t.Fatal("host renderer metadata was promoted into routing instructions")
	}
}

func TestNormalizeWorkBuddyToolMetadataIndependently(t *testing.T) {
	for _, fields := range []string{`"messageId":"fixture"`, `"toolResult":{"content":"result","renderer":{"type":"text"}}`} {
		body := []byte(`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result",` + fields + `}]}`)
		p, err := Normalize(Input{RequestID: "req-metadata", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: body})
		if err != nil || !bytes.Equal(p.ExecutionBody(), body) {
			t.Fatalf("installed tool metadata rejected or changed: %v", err)
		}
	}
	for _, fields := range []string{
		`"role":"user","content":"text","toolResult":{}`,
		`"role":"system","content":"text","toolResult":{}`,
		`"role":"developer","content":"text","toolResult":{}`,
		`"role":"tool","tool_call_id":"call_a","content":"result","toolResult":null`,
		`"role":"tool","tool_call_id":"call_a","content":"result","toolResult":[]`,
		`"role":"tool","tool_call_id":"call_a","content":"result","toolResult":"result"`,
		`"role":"tool","tool_call_id":"call_a","content":"result","toolResult":{},"unknown":true`,
		`"role":"tool","tool_call_id":"call_a","content":"result","toolResult":{"content":"a","content":"b"}`,
		`"role":"user","content":"text","messageId":"fixture"`,
		`"role":"tool","tool_call_id":"call_a","content":"result","usage":{}`,
	} {
		_, err := Normalize(Input{RequestID: "req-invalid", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: []byte(`{"messages":[{` + fields + `}]}`)})
		if err == nil {
			t.Fatal("invalid neighboring tool metadata accepted by routing")
		}
	}
}
