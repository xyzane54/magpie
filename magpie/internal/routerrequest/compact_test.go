package routerrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactJudgmentRetainsTrailingRestrictionsAndCompleteHistory(t *testing.T) {
	system := "# Host role\n" + strings.Repeat("Background description for this desktop application.\n", 200) + "\nNever perform write operations without explicit user approval.\n如果需要联网查询，必须选择云端。"
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": "Read the file first"}, {"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]string{"name": "read", "arguments": `{"path":"one.txt"}`}}}}, {"role": "tool", "tool_call_id": "c1", "content": "complete file contents"}, {"role": "user", "content": "Summarize that file"}},
		"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "description": strings.Repeat("Read one file. ", 100) + "Never modify files.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}, "required": []string{"path"}}}}},
	})
	p, err := Normalize(Input{RequestID: "r-compact", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: body, Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	original := p.Judgment()
	compact := CompactJudgment(original)
	encoded, _ := json.Marshal(compact)
	for _, value := range []string{"Never perform write operations without explicit user approval", "如果需要联网查询，必须选择云端", "Never modify files", "one.txt", "complete file contents", "Read the file first", "Summarize that file"} {
		if !bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("compact view lost routing restriction or history %q: %s", value, encoded)
		}
	}
	if compact.SummaryPolicy != JudgmentSummaryPolicy || !compact.SystemSummaries[0].Omitted || compact.RestrictionsOmitted || len(compact.History) != 4 || compact.Tools[0].Parameters[0].Name != "path" || !compact.Tools[0].Parameters[0].Required {
		t.Fatalf("incomplete structured features: %+v", compact)
	}
	if original.System[0] != system || !bytes.Equal(p.ExecutionBody(), body) {
		t.Fatal("decision compaction changed the complete execution request")
	}
	if bytes.Count(encoded, []byte("complete file contents")) != 1 {
		t.Fatalf("duplicate result text: %s", encoded)
	}
}

func TestCompactJudgmentDeduplicatesMixedPartsWithoutLosingToolOrder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"read both"},{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"read","input":{"path":"one"}},{"type":"tool_use","id":"two","name":"read","input":{"path":"two"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"one","content":"first result"},{"type":"tool_result","tool_use_id":"two","content":"second result","is_error":true},{"type":"text","text":"compare them"}]}],"max_tokens":32}`)
	p, err := Normalize(Input{RequestID: "r-mixed", AppID: "codex", Protocol: "anthropic", Operation: "generate", OriginalBody: body, Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	compact := CompactJudgment(p.Judgment())
	last := compact.History[2]
	if len(last.Parts) != 3 || last.Parts[0].ToolResult.ID != "one" || last.Parts[1].ToolResult.ID != "two" || !last.Parts[1].ToolResult.IsError || last.Parts[2].Text != "compare them" {
		t.Fatalf("mixed tool dependencies changed: %+v", last)
	}
	if last.Text != "" || last.ToolResult != nil || len(last.ToolResults) != 0 || len(compact.History[1].ToolCalls) != 0 {
		t.Fatalf("duplicate aggregate representation retained: %+v", compact.History)
	}
	encoded, _ := json.Marshal(compact)
	for _, text := range []string{"first result", "second result", "compare them"} {
		if bytes.Count(encoded, []byte(text)) != 1 {
			t.Fatalf("%q repeated in compact history: %s", text, encoded)
		}
	}
	compact.History[1].Parts[0].ToolCall.Arguments[0] = '!'
	if !bytes.Equal(p.ExecutionBody(), body) || p.Judgment().History[1].Parts[0].ToolCall.Arguments[0] != '{' {
		t.Fatal("compact view aliases execution or normalization snapshot")
	}
}

func TestCompactJudgmentSignalsRestrictionsThatCannotFit(t *testing.T) {
	v := CompactJudgment(JudgmentView{System: []string{strings.Repeat("You must require explicit authorization before this operation.\n", 100)}, History: []JudgmentMessage{{Role: "user", Text: "do it"}}})
	if !v.RestrictionsOmitted || !v.SystemSummaries[0].RestrictionsOmitted {
		t.Fatal("bounded summary silently discarded restrictions")
	}
}

func TestCompactJudgmentPreservesNumericAndURLRestrictionsVerbatim(t *testing.T) {
	restrictions := []string{
		"Only use local when confidence is greater than 3.14; otherwise choose cloud.",
		"Never send requests to https://api.example.test/v3.14/endpoint; use cloud instead.",
		"The version must be 3.14.15 and the maximum rate must be 0.25.",
	}
	system := strings.Repeat("Background description without routing conditions.\n", 100) + strings.Join(restrictions, "\n")
	compact := CompactJudgment(JudgmentView{System: []string{system}})
	if len(compact.SystemSummaries) != 1 || compact.RestrictionsOmitted {
		t.Fatalf("numeric/URL constraint was dropped: %+v", compact)
	}
	summary := compact.SystemSummaries[0]
	joined := strings.Join(summary.Excerpts, " ")
	for _, text := range []string{"confidence is greater than 3.14;", "https://api.example.test/v3.14/endpoint;", "version must be 3.14.15", "rate must be 0.25."} {
		if !strings.Contains(joined, text) {
			t.Fatalf("summary changed numeric or URL constraint %q: %q", text, joined)
		}
	}
	for _, excerpt := range summary.Excerpts {
		if !strings.Contains(system, excerpt) {
			t.Fatalf("summary excerpt is not an unchanged source substring: %q", excerpt)
		}
	}
}

func TestCompactJudgmentKeepsShortAndLongSystemMessagesInOriginalOrder(t *testing.T) {
	systems := []string{"First instruction: use the earlier policy.", strings.Repeat("Long descriptive background.\n", 100) + "Never ignore the middle policy.", "Final instruction: use the later policy."}
	compact := CompactJudgment(JudgmentView{System: systems})
	if len(compact.System) != 0 || len(compact.SystemSummaries) != len(systems) {
		t.Fatalf("system messages split into unordered representations: %+v", compact)
	}
	if strings.Join(compact.SystemSummaries[0].Excerpts, "") != systems[0] || !strings.Contains(strings.Join(compact.SystemSummaries[1].Excerpts, ""), "Never ignore the middle policy.") || strings.Join(compact.SystemSummaries[2].Excerpts, "") != systems[2] {
		t.Fatalf("system precedence changed: %+v", compact.SystemSummaries)
	}
}
