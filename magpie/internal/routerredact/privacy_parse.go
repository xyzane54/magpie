package routerredact

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// privacyParseFailure preserves the public failure code while retaining only a
// fixed diagnostic category. It never retains the response, source or bad value.
type privacyParseFailure struct{ diagnostic string }

func (privacyParseFailure) Error() string { return "redaction_invalid_result" }
func (privacyParseFailure) Unwrap() error { return fail("redaction_invalid_result") }

func privacyInvalid(diagnostic string) error {
	return privacyParseFailure{diagnostic: diagnostic}
}

// PrivacyParseDiagnosticCode describes a parser rejection without exposing any
// request or response data. The public error and capability gate are unchanged.
// An empty code means Parse succeeded; other error types remain unclassified.
func PrivacyParseDiagnosticCode(err error) string {
	if err == nil {
		return ""
	}
	var failure privacyParseFailure
	if !errors.As(err, &failure) {
		return "unclassified"
	}
	switch failure.diagnostic {
	case "envelope_json", "envelope_keys", "envelope_shape", "envelope_semantics",
		"finish_reason", "truncation", "content_json", "content_shape", "field_shape",
		"field_coverage", "findings_shape", "finding_shape", "label", "span_bounds",
		"span_text", "confidence", "duplicate_entity", "overlap":
		return failure.diagnostic
	default:
		return "unclassified"
	}
}

// Parse accepts one complete local-model batch. Coordinates in the pinned
// privacy prompt count Unicode code points; Entity coordinates count UTF-8
// bytes. No search, coordinate repair or silent finding removal is permitted.
func (s *PrivacySpecification) Parse(ctx context.Context, in Extraction, raw []byte) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if s == nil || in.Versions != s.Versions() || in.Versions.Model == "" || in.Versions.Prompt == "" || in.Versions.Config == "" {
		return Result{}, fail("redaction_unavailable")
	}
	if len(in.Texts) > PrivacyMaxBatchFields {
		return Result{}, fail("redaction_invalid_input")
	}
	boundaries := make([][]int, len(in.Texts))
	totalRunes, totalBytes := 0, 0
	for field, text := range in.Texts {
		if len(text) > PrivacyMaxInputBytes-totalBytes {
			return Result{}, fail("redaction_input_limit")
		}
		if !utf8.ValidString(text) {
			return Result{}, fail("redaction_invalid_input")
		}
		positions := make([]int, 0, min(len(text), PrivacyMaxFieldRunes)+1)
		for offset := range text {
			if len(positions) >= PrivacyMaxFieldRunes {
				return Result{}, fail("redaction_input_limit")
			}
			positions = append(positions, offset)
		}
		totalRunes += len(positions)
		totalBytes += len(text)
		if totalRunes > PrivacyMaxBatchRunes || totalBytes > PrivacyMaxInputBytes {
			return Result{}, fail("redaction_input_limit")
		}
		boundaries[field] = append(positions, len(text))
	}
	content, err := privacyCompletion(raw)
	if err != nil {
		return Result{}, err
	}
	v, err := wireDecode([]byte(content), 1<<20)
	root, ok := v.(map[string]any)
	if err != nil {
		return Result{}, privacyInvalid("content_json")
	}
	if !ok || !privacyExactKeys(root, "complete", "fields") || root["complete"] != true {
		return Result{}, privacyInvalid("content_shape")
	}
	fields, ok := root["fields"].([]any)
	if !ok {
		return Result{}, privacyInvalid("content_shape")
	}
	if len(fields) != len(in.Texts) {
		return Result{}, privacyInvalid("field_coverage")
	}
	labels := PrivacyLabels()
	seenFields := make([]bool, len(in.Texts))
	seenEntities := make(map[Entity]bool)
	result := Result{Complete: true}
	for _, value := range fields {
		if err := contextError(ctx); err != nil {
			return Result{}, err
		}
		field, ok := value.(map[string]any)
		if !ok || !privacyExactKeys(field, "field", "complete", "findings") || field["complete"] != true {
			return Result{}, privacyInvalid("field_shape")
		}
		index, ok := privacyInteger(field["field"], len(in.Texts)-1)
		if !ok || seenFields[index] {
			return Result{}, privacyInvalid("field_coverage")
		}
		seenFields[index] = true
		findings, ok := field["findings"].([]any)
		if !ok || len(findings) > maxEntities-len(result.Entities) {
			return Result{}, privacyInvalid("findings_shape")
		}
		for _, value := range findings {
			finding, ok := value.(map[string]any)
			if !ok || !privacyExactKeys(finding, "label", "start", "end", "text", "confidence") {
				return Result{}, privacyInvalid("finding_shape")
			}
			label, ok := finding["label"].(string)
			category, known := labels[label]
			if !ok || !known || !categoryValid(category) {
				return Result{}, privacyInvalid("label")
			}
			limit := len(boundaries[index]) - 1
			start, startOK := privacyInteger(finding["start"], limit)
			end, endOK := privacyInteger(finding["end"], limit)
			text, textOK := finding["text"].(string)
			if !startOK || !endOK || start >= end {
				return Result{}, privacyInvalid("span_bounds")
			}
			if !textOK || text == "" || len(text) > maxEntityBytes {
				return Result{}, privacyInvalid("span_text")
			}
			if !privacyConfidence(finding["confidence"]) {
				return Result{}, privacyInvalid("confidence")
			}
			byteStart, byteEnd := boundaries[index][start], boundaries[index][end]
			if in.Texts[index][byteStart:byteEnd] != text {
				return Result{}, privacyInvalid("span_text")
			}
			entity := Entity{Field: index, Start: byteStart, End: byteEnd, Text: strings.Clone(text), Category: category}
			if seenEntities[entity] {
				return Result{}, privacyInvalid("duplicate_entity")
			}
			seenEntities[entity] = true
			result.Entities = append(result.Entities, entity)
		}
	}
	for _, seen := range seenFields {
		if !seen {
			return Result{}, privacyInvalid("field_coverage")
		}
	}
	validated, err := validateEntities(in.Texts, result)
	if err != nil {
		return Result{}, privacyInvalid("overlap")
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	result.Entities = validated
	return result, nil
}

func privacyExactKeys(object map[string]any, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			return false
		}
	}
	return true
}

func privacyInteger(value any, maximum int) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(string(number), 10, 64)
	return int(n), err == nil && n >= 0 && n <= int64(maximum)
}

func privacyConfidence(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	n, err := number.Float64()
	return err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1
}

// The transport envelope has a separate strict boundary from the model JSON.
// Known accounting/timing fields are ignored; anything that changes completion
// meaning (extra choices, truncation, tool/refusal/reasoning output) is rejected.
func privacyCompletion(raw []byte) (string, error) {
	v, err := wireDecode(raw, 1<<20)
	root, ok := v.(map[string]any)
	if err != nil || !ok {
		return "", privacyInvalid("envelope_json")
	}
	for key, value := range root {
		switch key {
		case "choices":
		case "id", "object", "model":
			if _, ok := value.(string); !ok {
				return "", privacyInvalid("envelope_shape")
			}
		case "system_fingerprint", "service_tier":
			if _, ok := value.(string); value != nil && !ok {
				return "", privacyInvalid("envelope_shape")
			}
		case "created":
			if _, ok := privacyInteger(value, int(^uint(0)>>1)); !ok {
				return "", privacyInvalid("envelope_shape")
			}
		case "truncated", "prompt_truncated":
			if value != false {
				return "", privacyInvalid("truncation")
			}
		case "usage", "timings":
			if _, ok := value.(map[string]any); value != nil && !ok {
				return "", privacyInvalid("envelope_shape")
			}
			if !privacyUntruncatedMetadata(value) {
				return "", privacyInvalid("truncation")
			}
		default:
			if strings.Contains(strings.ToLower(key), "truncat") {
				return "", privacyInvalid("truncation")
			}
			return "", privacyInvalid("envelope_keys")
		}
	}
	choices, ok := root["choices"].([]any)
	if !ok || len(choices) != 1 {
		return "", privacyInvalid("envelope_shape")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return "", privacyInvalid("envelope_shape")
	}
	for key, value := range choice {
		switch key {
		case "index", "message", "finish_reason":
		case "logprobs":
			if value != nil {
				return "", privacyInvalid("envelope_semantics")
			}
		default:
			return "", privacyInvalid("envelope_keys")
		}
	}
	index, indexOK := privacyInteger(choice["index"], 0)
	message, messageOK := choice["message"].(map[string]any)
	if !indexOK || index != 0 || !messageOK {
		return "", privacyInvalid("envelope_shape")
	}
	if choice["finish_reason"] != "stop" {
		return "", privacyInvalid("finish_reason")
	}
	if message["role"] != "assistant" {
		return "", privacyInvalid("envelope_semantics")
	}
	for key, value := range message {
		switch key {
		case "role", "content":
		case "refusal", "reasoning", "reasoning_content":
			if value != nil && value != "" {
				return "", privacyInvalid("envelope_semantics")
			}
		case "tool_calls", "annotations":
			if values, ok := value.([]any); value != nil && (!ok || len(values) != 0) {
				return "", privacyInvalid("envelope_semantics")
			}
		case "function_call":
			if object, ok := value.(map[string]any); value != nil && (!ok || len(object) != 0) {
				return "", privacyInvalid("envelope_semantics")
			}
		default:
			return "", privacyInvalid("envelope_keys")
		}
	}
	content, ok := message["content"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return "", privacyInvalid("content_shape")
	}
	return content, nil
}

func privacyUntruncatedMetadata(value any) bool {
	switch object := value.(type) {
	case map[string]any:
		for key, child := range object {
			if key == "truncated" || key == "prompt_truncated" {
				if child != false {
					return false
				}
			} else if strings.Contains(strings.ToLower(key), "truncat") {
				// A newly introduced truncation/detail marker has no accepted
				// meaning in this version, including inside known metadata.
				return false
			}
			if !privacyUntruncatedMetadata(child) {
				return false
			}
		}
	case []any:
		for _, child := range object {
			if !privacyUntruncatedMetadata(child) {
				return false
			}
		}
	}
	return true
}
