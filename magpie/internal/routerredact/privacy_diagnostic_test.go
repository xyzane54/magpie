package routerredact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPrivacyParseDiagnosticSeparatesFailuresWithoutExposingData(t *testing.T) {
	spec := privacyParseSpec(t)
	in := Extraction{Versions: spec.Versions(), Texts: []string{"😀张三张三"}}
	finding := privacyParseFinding("private_person", "张三", 1, 3)
	valid := privacyParseEnvelope(privacyParseContent(privacyParseField(0, finding)), "stop")
	envelope := func(change func(map[string]any)) []byte {
		var root map[string]any
		if json.Unmarshal(valid, &root) != nil {
			t.Fatal("invalid diagnostic fixture")
		}
		change(root)
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	content := func(value string) []byte { return privacyParseEnvelope(value, "stop") }
	changedFinding := func(key string, value any) []byte {
		copy := privacyParseFinding("private_person", "张三", 1, 3)
		copy[key] = value
		return content(privacyParseContent(privacyParseField(0, copy)))
	}
	cases := []struct {
		code string
		raw  []byte
	}{
		{"envelope_json", []byte(`{"choices":`)},
		{"envelope_keys", envelope(func(root map[string]any) { root["synthetic-private-key"] = "synthetic-private-value" })},
		{"envelope_shape", envelope(func(root map[string]any) { root["choices"] = []any{} })},
		{"envelope_semantics", envelope(func(root map[string]any) {
			root["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["reasoning_content"] = "synthetic-private-value"
		})},
		{"finish_reason", privacyParseEnvelope(privacyParseContent(privacyParseField(0, finding)), "length")},
		{"truncation", envelope(func(root map[string]any) { root["prompt_truncated"] = true })},
		{"content_json", content(`{"complete":true,"fields":`)},
		{"content_shape", content(`{"complete":false,"fields":[]}`)},
		{"field_shape", content(`{"complete":true,"fields":[{"field":0,"complete":false,"findings":[]}]}`)},
		{"field_coverage", content(privacyParseContent())},
		{"field_coverage", content(privacyParseContent(privacyParseField(1)))},
		{"findings_shape", content(`{"complete":true,"fields":[{"field":0,"complete":true,"findings":null}]}`)},
		{"finding_shape", changedFinding("synthetic-private-key", "synthetic-private-value")},
		{"label", changedFinding("label", "synthetic-private-value")},
		{"span_bounds", changedFinding("start", 99)},
		{"span_text", changedFinding("text", "synthetic-private-value")},
		{"confidence", changedFinding("confidence", 2)},
		{"duplicate_entity", content(privacyParseContent(privacyParseField(0, finding, finding)))},
		{"overlap", content(privacyParseContent(privacyParseField(0, finding, privacyParseFinding("organization_name", "张三张三", 1, 5))))},
	}
	for index, tc := range cases {
		t.Run(fmt.Sprintf("%02d-%s", index, tc.code), func(t *testing.T) {
			result, err := spec.Parse(context.Background(), in, tc.raw)
			if err == nil || result.Complete || len(result.Entities) != 0 {
				t.Fatal("invalid output became a complete extraction")
			}
			var failure *Failure
			if err.Error() != "redaction_invalid_result" || !errors.As(err, &failure) || failure.Code != "redaction_invalid_result" {
				t.Fatal("diagnostic changed the public failure contract")
			}
			if got := PrivacyParseDiagnosticCode(fmt.Errorf("wrapped: %w", err)); got != tc.code {
				t.Fatalf("diagnostic = %q, want %q", got, tc.code)
			}
			for _, representation := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), PrivacyParseDiagnosticCode(err)} {
				if strings.Contains(representation, "张三") || strings.Contains(representation, "synthetic-private") {
					t.Fatal("diagnostic exposed a private source, key or value")
				}
			}
		})
	}
	result, err := spec.Parse(context.Background(), in, valid)
	if err != nil || !result.Complete || len(result.Entities) != 1 || PrivacyParseDiagnosticCode(err) != "" {
		t.Fatal("diagnostic changed valid Unicode extraction")
	}
}

func TestPrivacyParseDiagnosticUsesOnlyFixedCategories(t *testing.T) {
	for _, err := range []error{errors.New("synthetic-private-value"), fail("redaction_unavailable"), context.Canceled, privacyInvalid("synthetic-private-value")} {
		if got := PrivacyParseDiagnosticCode(err); got != "unclassified" {
			t.Fatalf("unknown error produced a data-dependent category: %q", got)
		}
	}
}
