package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func privacyParseSpec(t *testing.T) *PrivacySpecification {
	t.Helper()
	specification, err := NewPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	return specification
}

func privacyParseEnvelope(content, finish string) []byte {
	body, _ := json.Marshal(map[string]any{"id": "local-reply", "object": "chat.completion", "model": "fixed-local", "created": 1, "usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 1}, "timings": map[string]any{"predicted_ms": 1.25}, "choices": []any{map[string]any{"index": 0, "finish_reason": finish, "logprobs": nil, "message": map[string]any{"role": "assistant", "content": content}}}})
	return body
}

func privacyParseJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func privacyParseField(index int, findings ...any) map[string]any {
	if findings == nil {
		findings = []any{}
	}
	return map[string]any{"field": index, "complete": true, "findings": findings}
}

func privacyParseContent(fields ...any) string {
	if fields == nil {
		fields = []any{}
	}
	return privacyParseJSON(map[string]any{"complete": true, "fields": fields})
}

func privacyParseFinding(label, text string, start, end int) map[string]any {
	return map[string]any{"label": label, "text": text, "start": start, "end": end, "confidence": 0.99}
}

func TestPrivacyParseUnicodeRepeatedAndMultipleFields(t *testing.T) {
	specification := privacyParseSpec(t)
	in := Extraction{Versions: specification.Versions(), Texts: []string{"😀张三联系张三。", "mail a@b.test", ""}}
	content := privacyParseContent(
		privacyParseField(2),
		privacyParseField(0, privacyParseFinding("private_person", "张三", 5, 7), privacyParseFinding("private_person", "张三", 1, 3)),
		privacyParseField(1, privacyParseFinding("private_email", "a@b.test", 5, 13)),
	)
	result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(content, "stop"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Entity{
		{Field: 0, Start: 4, End: 10, Text: "张三", Category: PrivacyLabels()["private_person"]},
		{Field: 0, Start: 16, End: 22, Text: "张三", Category: PrivacyLabels()["private_person"]},
		{Field: 1, Start: 5, End: 13, Text: "a@b.test", Category: PrivacyLabels()["private_email"]},
	}
	if !result.Complete || !reflect.DeepEqual(result.Entities, want) {
		t.Fatal("Unicode positions, duplicate appearances or field association changed")
	}
}

// These five cases reproduce the strict refusal requirements that failed in
// privacy_gateway at fixed source 6fcdae87; only the envelope/schema is adapted.
func TestPrivacyParseReviewFiveInvalidOutputsFailClosed(t *testing.T) {
	specification := privacyParseSpec(t)
	in := Extraction{Versions: specification.Versions(), Texts: []string{"张三联系张三。"}}
	cases := []struct{ name, content, finish string }{
		{"missing-findings", `{}`, "stop"},
		{"invalid-list-member", `{"complete":true,"fields":[{"field":0,"complete":true,"findings":[7]}]}`, "stop"},
		{"unknown-label", privacyParseContent(privacyParseField(0, privacyParseFinding("unknown_sensitive", "张三", 0, 2))), "stop"},
		{"ambiguous-invalid-offsets", privacyParseContent(privacyParseField(0, privacyParseFinding("private_person", "张三", 99, 100))), "stop"},
		{"unfinished-completion", privacyParseContent(privacyParseField(0)), "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(tc.content, tc.finish))
			if err == nil || err.Error() != "redaction_invalid_result" || result.Complete || len(result.Entities) != 0 {
				t.Fatal("invalid model output became a clean result")
			}
			if strings.Contains(err.Error(), "张三") || strings.Contains(err.Error(), "unknown_sensitive") {
				t.Fatal("failure exposed private model output")
			}
		})
	}
}

func TestPrivacyParseRejectsInexactAndIncompleteSchema(t *testing.T) {
	specification := privacyParseSpec(t)
	in := Extraction{Versions: specification.Versions(), Texts: []string{"张三联系张三。", "safe"}}
	validFinding := `{"label":"private_person","start":0,"end":2,"text":"张三","confidence":0.99}`
	cases := map[string]string{
		"top false":            `{"complete":false,"fields":[]}`,
		"top missing complete": `{"fields":[]}`,
		"top extra":            `{"complete":true,"fields":[],"explanation":"private"}`,
		"null fields":          `{"complete":true,"fields":null}`,
		"missing field":        privacyParseContent(privacyParseField(0)),
		"duplicate field":      privacyParseContent(privacyParseField(0), privacyParseField(0)),
		"extra field":          privacyParseContent(privacyParseField(0), privacyParseField(1), privacyParseField(2)),
		"false field complete": `{"complete":true,"fields":[{"field":0,"complete":false,"findings":[]},{"field":1,"complete":true,"findings":[]}]}`,
		"field bool":           `{"complete":true,"fields":[{"field":false,"complete":true,"findings":[]},{"field":1,"complete":true,"findings":[]}]}`,
		"field float":          `{"complete":true,"fields":[{"field":0.0,"complete":true,"findings":[]},{"field":1,"complete":true,"findings":[]}]}`,
		"field negative":       `{"complete":true,"fields":[{"field":-1,"complete":true,"findings":[]},{"field":1,"complete":true,"findings":[]}]}`,
		"field extra key":      `{"complete":true,"fields":[{"field":0,"complete":true,"findings":[],"note":"private"},{"field":1,"complete":true,"findings":[]}]}`,
		"findings null":        `{"complete":true,"fields":[{"field":0,"complete":true,"findings":null},{"field":1,"complete":true,"findings":[]}]}`,
		"duplicate finding":    `{"complete":true,"fields":[{"field":0,"complete":true,"findings":[` + validFinding + `,` + validFinding + `]},{"field":1,"complete":true,"findings":[]}]}`,
		"overlap":              privacyParseContent(privacyParseField(0, privacyParseFinding("private_person", "张三", 0, 2), privacyParseFinding("organization_name", "张三联系", 0, 4)), privacyParseField(1)),
		"wrong text no repair": privacyParseContent(privacyParseField(0, privacyParseFinding("private_person", "张三", 1, 3)), privacyParseField(1)),
		"duplicate JSON keys":  `{"complete":true,"complete":true,"fields":[]}`,
		"trailing JSON":        privacyParseContent(privacyParseField(0), privacyParseField(1)) + `{}`,
		"markdown":             "```json\n" + privacyParseContent(privacyParseField(0), privacyParseField(1)) + "\n```",
	}
	for _, change := range []struct{ name, old, next string }{
		{"start boolean", `"start":0`, `"start":false`},
		{"start null", `"start":0`, `"start":null`},
		{"start float", `"start":0`, `"start":0.0`},
		{"start exponent", `"start":0`, `"start":0e0`},
		{"start negative", `"start":0`, `"start":-1`},
		{"end huge", `"end":2`, `"end":999999999999999999999999`},
		{"end reversed", `"end":2`, `"end":0`},
		{"confidence null", `"confidence":0.99`, `"confidence":null`},
		{"confidence bool", `"confidence":0.99`, `"confidence":true`},
		{"confidence high", `"confidence":0.99`, `"confidence":1.01`},
		{"confidence low", `"confidence":0.99`, `"confidence":-0.01`},
		{"confidence overflow", `"confidence":0.99`, `"confidence":1e9999`},
		{"missing confidence", `,"confidence":0.99`, ``},
		{"unknown finding key", `"confidence":0.99`, `"confidence":0.99,"extra":true`},
	} {
		finding := strings.Replace(validFinding, change.old, change.next, 1)
		cases[change.name] = `{"complete":true,"fields":[{"field":0,"complete":true,"findings":[` + finding + `]},{"field":1,"complete":true,"findings":[]}]}`
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(content, "stop")); err == nil || result.Complete || len(result.Entities) != 0 {
				t.Fatal("malformed, incomplete or conflicting result accepted")
			}
		})
	}
}

func TestPrivacyParseCompletionEnvelopeIsStrict(t *testing.T) {
	specification := privacyParseSpec(t)
	in := Extraction{Versions: specification.Versions(), Texts: []string{"safe"}}
	content := privacyParseContent(privacyParseField(0))
	valid := privacyParseEnvelope(content, "stop")
	cases := map[string][]byte{}
	for _, mutation := range []string{"missing index", "wrong index", "two choices", "wrong role", "refusal", "tool call", "function call", "reasoning", "unknown root", "unknown choice", "unknown message", "array content", "null content", "empty content", "missing finish", "filter finish"} {
		var root map[string]any
		_ = json.Unmarshal(valid, &root)
		choice := root["choices"].([]any)[0].(map[string]any)
		message := choice["message"].(map[string]any)
		switch mutation {
		case "missing index":
			delete(choice, "index")
		case "wrong index":
			choice["index"] = 1
		case "two choices":
			root["choices"] = []any{choice, choice}
		case "wrong role":
			message["role"] = "user"
		case "refusal":
			message["refusal"] = "cannot inspect private text"
		case "tool call":
			message["tool_calls"] = []any{map[string]any{"id": "call"}}
		case "function call":
			message["function_call"] = map[string]any{"name": "run"}
		case "reasoning":
			message["reasoning_content"] = "private thoughts"
		case "unknown root":
			root["unreviewed"] = true
		case "unknown choice":
			choice["unreviewed"] = true
		case "unknown message":
			message["unreviewed"] = true
		case "array content":
			message["content"] = []any{map[string]any{"type": "text", "text": content}}
		case "null content":
			message["content"] = nil
		case "empty content":
			message["content"] = " \n "
		case "missing finish":
			delete(choice, "finish_reason")
		case "filter finish":
			choice["finish_reason"] = "content_filter"
		}
		cases[mutation], _ = json.Marshal(root)
	}
	for _, mutation := range []string{"truncated", "prompt_truncated", "nested prompt truncation", "unknown truncation details", "nested unknown truncation details"} {
		var root map[string]any
		_ = json.Unmarshal(valid, &root)
		switch mutation {
		case "truncated", "prompt_truncated":
			root[mutation] = true
		case "nested prompt truncation":
			root["timings"] = map[string]any{"prompt_truncated": true}
		case "unknown truncation details":
			root["truncation_details"] = map[string]any{"reason": "context"}
		case "nested unknown truncation details":
			root["usage"] = map[string]any{"details": map[string]any{"truncation_details": map[string]any{"reason": "context"}}}
		}
		cases[mutation], _ = json.Marshal(root)
	}
	cases["invalid UTF8"] = []byte("{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"\xff\"}}]}")
	cases["unpaired surrogate"] = []byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"\ud800"}}]}`)
	cases["duplicate outer key"] = bytes.Replace(valid, []byte(`"index":0`), []byte(`"index":0,"index":0`), 1)
	cases["over response limit"] = append(bytes.Clone(valid), bytes.Repeat([]byte(" "), 1<<20)...)
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if result, err := specification.Parse(context.Background(), in, raw); err == nil || result.Complete {
				t.Fatal("ambiguous or invalid completion envelope accepted")
			}
		})
	}
}

func TestPrivacyParseAllExplicitLabelsAndEmptyCompletion(t *testing.T) {
	specification := privacyParseSpec(t)
	labels := PrivacyLabels()
	if len(labels) != 49 {
		t.Fatal("fixed explicit label map incomplete")
	}
	for label, category := range labels {
		in := Extraction{Versions: specification.Versions(), Texts: []string{"x"}}
		result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(privacyParseContent(privacyParseField(0, privacyParseFinding(label, "x", 0, 1))), "stop"))
		if err != nil || !result.Complete || len(result.Entities) != 1 || result.Entities[0].Category != category {
			t.Fatal("explicit label mapping rejected or changed")
		}
	}
	in := Extraction{Versions: specification.Versions(), Texts: []string{"", "ordinary"}}
	result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(privacyParseContent(privacyParseField(0), privacyParseField(1)), "stop"))
	if err != nil || !result.Complete || len(result.Entities) != 0 {
		t.Fatal("explicit full empty scan rejected")
	}
	in.Texts = nil
	result, err = specification.Parse(context.Background(), in, privacyParseEnvelope(privacyParseContent(), "stop"))
	if err != nil || !result.Complete || len(result.Entities) != 0 {
		t.Fatal("explicit zero-field completion rejected")
	}
	for _, content := range []string{`{}`, `{"complete":true}`, `{"complete":true,"fields":null}`, privacyParseContent(privacyParseField(0))} {
		if result, err := specification.Parse(context.Background(), in, privacyParseEnvelope(content, "stop")); err == nil || result.Complete {
			t.Fatal("zero-field input treated missing/extra fields as complete")
		}
	}
}

func TestPrivacyParseMasksZeroConfidenceAndBoundsEntityBytes(t *testing.T) {
	specification := privacyParseSpec(t)
	for _, confidence := range []float64{0, 1} {
		finding := privacyParseFinding("public_information", "x", 0, 1)
		finding["confidence"] = confidence
		result, err := specification.Parse(context.Background(), Extraction{Versions: specification.Versions(), Texts: []string{"x"}}, privacyParseEnvelope(privacyParseContent(privacyParseField(0, finding)), "stop"))
		if err != nil || !result.Complete || len(result.Entities) != 1 {
			t.Fatal("confidence caused silent entity filtering")
		}
	}
	text := strings.Repeat("😀", 1100)
	result, err := specification.Parse(context.Background(), Extraction{Versions: specification.Versions(), Texts: []string{text}}, privacyParseEnvelope(privacyParseContent(privacyParseField(0, privacyParseFinding("private_person", text, 0, 1100))), "stop"))
	if err == nil || result.Complete {
		t.Fatal("entity exceeding UTF-8 byte limit accepted")
	}
}

func TestPrivacyParseInputBoundsCancellationAndIsolation(t *testing.T) {
	specification := privacyParseSpec(t)
	validRaw := privacyParseEnvelope(privacyParseContent(privacyParseField(0)), "stop")
	for _, in := range []Extraction{
		{Versions: Versions{Model: "wrong"}, Texts: []string{"safe"}},
		{Versions: specification.Versions(), Texts: []string{"\xff"}},
		{Versions: specification.Versions(), Texts: []string{strings.Repeat("字", PrivacyMaxFieldRunes+1)}},
		{Versions: specification.Versions(), Texts: make([]string, PrivacyMaxBatchFields+1)},
		{Versions: specification.Versions(), Texts: []string{strings.Repeat("字", PrivacyMaxFieldRunes), "overflow"}},
	} {
		if result, err := specification.Parse(context.Background(), in, validRaw); err == nil || result.Complete {
			t.Fatal("invalid input/version was parsed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := specification.Parse(ctx, Extraction{Versions: specification.Versions(), Texts: []string{"safe"}}, validRaw); !errors.Is(err, context.Canceled) || result.Complete {
		t.Fatal("cancel ignored")
	}
	var wg sync.WaitGroup
	for _, text := range []string{"甲", "😀", "é", "张"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			in := Extraction{Versions: specification.Versions(), Texts: []string{text + text}}
			raw := privacyParseEnvelope(privacyParseContent(privacyParseField(0, privacyParseFinding("private_person", text, 1, 2))), "stop")
			result, err := specification.Parse(context.Background(), in, raw)
			if err != nil || len(result.Entities) != 1 || result.Entities[0].Start != len(text) || result.Entities[0].End != 2*len(text) || result.Entities[0].Text != text {
				t.Error("concurrent parse used another request's source")
			}
		}(text)
	}
	wg.Wait()
}
