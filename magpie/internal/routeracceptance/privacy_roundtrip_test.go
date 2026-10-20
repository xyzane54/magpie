package routeracceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

// This callback is a serialized model-response fixture, not model recognition.
// The production entry always supplies the existing Manager.Generate instead.
func privacyRoundtripFixture(mode string) privacyRoundtripGenerate {
	return func(ctx context.Context, purpose routerruntime.Purpose, raw []byte, consume func(*http.Response) error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if purpose != routerruntime.Redaction {
			return errors.New("fixture_wrong_purpose")
		}
		var request struct {
			Messages []struct{ Role, Content string }
			Stream   bool
			Kwargs   map[string]any `json:"chat_template_kwargs"`
		}
		if json.Unmarshal(raw, &request) != nil || request.Stream || request.Kwargs["enable_thinking"] != false {
			return errors.New("fixture_wrong_request")
		}
		var input struct {
			Fields []struct {
				Field int
				Text  string
			}
		}
		for _, message := range request.Messages {
			if message.Role == "user" && json.Unmarshal([]byte(message.Content), &input) != nil {
				return errors.New("fixture_wrong_fields")
			}
		}
		fields := make([]any, 0, len(input.Fields))
		for _, field := range input.Fields {
			findings := []any{}
			if mode != "empty" {
				for _, fixture := range []struct{ text, label string }{{"李芳", "private_person"}, {"roundtrip@example.test", "private_email"}, {"synthetic@example.test", "private_email"}} {
					for cursor := 0; cursor < len(field.Text); {
						at := strings.Index(field.Text[cursor:], fixture.text)
						if at < 0 {
							break
						}
						at += cursor
						start := utf8.RuneCountInString(field.Text[:at])
						end := start + utf8.RuneCountInString(fixture.text)
						if mode == "invalid_span" {
							start = 999
						}
						label := fixture.label
						if mode == "wrong_label" {
							label = "organization_name"
						}
						findings = append(findings, map[string]any{"label": label, "text": fixture.text, "start": start, "end": end, "confidence": 1})
						cursor = at + len(fixture.text)
					}
				}
			}
			fields = append(fields, map[string]any{"field": field.Field, "complete": true, "findings": findings})
		}
		if mode == "missing_field" && len(fields) > 0 {
			fields = fields[:len(fields)-1]
		}
		content, _ := json.Marshal(map[string]any{"complete": true, "fields": fields})
		envelope, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
		response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(envelope))}
		defer response.Body.Close()
		return consume(response)
	}
}

func privacyRoundtripTestSpec(t *testing.T) *routerredact.PrivacySpecification {
	t.Helper()
	spec, err := routerredact.NewPrivacySpecification(routerredact.ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestPrivacyRoundtripRealW5JSONSSEAndExactToolNumbers(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	directory := t.TempDir()
	report, err := runPrivacyRoundtripWith(context.Background(), privacyRoundtripFixture("valid"), status, spec, directory)
	if err != nil {
		t.Fatalf("roundtrip failed: %s (%v)", report.ErrorCode, err)
	}
	if report.Case != "person-email-tools-numbers" || !report.ToolCheckApplicable || !report.NumberCheckApplicable || !report.CaptureBlocked || !report.OutboundSensitiveWordsAbsent || !report.JSONRestored || !report.SSERestored || !report.ToolArgumentsRestored || !report.ExactNumbersPreserved || !report.RedactionCapabilityUnknown || report.UpstreamRequests != 2 || report.Entities != 4 || report.Fields != 10 || report.Batches == 0 {
		t.Fatalf("incomplete roundtrip: %+v", report)
	}
	safe, err := json.Marshal(report)
	if err != nil || bytes.Contains(safe, []byte("李芳")) || bytes.Contains(safe, []byte("roundtrip@example.test")) || bytes.Contains(safe, []byte("<PERSON:")) {
		t.Fatal("safe evidence contained original text or a mapping")
	}
	files, err := filepath.Glob(filepath.Join(directory, "synthetic-only", "*.response.json"))
	if err != nil || len(files) != report.Batches {
		t.Fatal("raw synthetic evidence was not isolated by batch")
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil || !json.Valid(raw) {
			t.Fatal("raw synthetic evidence missing or invalid")
		}
	}
}

func TestPrivacySmallRoundtripIndependentScopedEvidenceKeepsLargerFailure(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	directory := t.TempDir()
	larger, largerErr := runPrivacyRoundtripWith(context.Background(), privacyRoundtripFixture("empty"), status, spec, directory)
	if largerErr == nil || larger.Case != "person-email-tools-numbers" || larger.Fields != 10 || larger.UpstreamRequests != 0 || !larger.NumberCheckApplicable || larger.ExactNumbersPreserved {
		t.Fatal("original larger fixture was reduced or its failure hidden")
	}
	largerPath := filepath.Join(directory, "synthetic-only", "roundtrip-batch-00.response.json")
	largerRaw, err := os.ReadFile(largerPath)
	if err != nil {
		t.Fatal("larger failure evidence missing")
	}
	smaller, err := runPrivacySmallRoundtripWith(context.Background(), privacyRoundtripFixture("valid"), status, spec, directory)
	if err != nil {
		t.Fatalf("small roundtrip failed: %s (%v)", smaller.ErrorCode, err)
	}
	if smaller.Case != "email-tools-small" || !smaller.ToolCheckApplicable || smaller.NumberCheckApplicable || smaller.ExactNumbersPreserved || smaller.Fields != 4 || smaller.Entities != 2 || smaller.Batches == 0 || smaller.UpstreamRequests != 2 || !smaller.CaptureBlocked || !smaller.OutboundSensitiveWordsAbsent || !smaller.JSONRestored || !smaller.SSERestored || !smaller.ToolArgumentsRestored || !smaller.RedactionCapabilityUnknown {
		t.Fatalf("small result exceeded or missed its scoped checks: %+v", smaller)
	}
	after, err := os.ReadFile(largerPath)
	if err != nil || !bytes.Equal(after, largerRaw) || larger.ErrorCode == "" || larger.UpstreamRequests != 0 {
		t.Fatal("small positive case replaced the larger failure or its raw evidence")
	}
	smallerRaw, err := os.ReadFile(filepath.Join(directory, "synthetic-only", "small-roundtrip-batch-00.response.json"))
	if err != nil || !json.Valid(smallerRaw) {
		t.Fatal("small raw evidence did not use its independent filename")
	}
	safe, err := json.Marshal(smaller)
	if err != nil || bytes.Contains(safe, []byte("roundtrip@example.test")) || bytes.Contains(safe, []byte("<EMAIL:")) {
		t.Fatal("small safe report contained raw text or a mapping")
	}
}

func TestPrivacyTextRoundtripUsesExactCase4AndKeepsToolFailures(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	cases, err := routerredact.PrivacyEvaluationCases(spec)
	if err != nil || len(cases[4].Texts) != 1 || cases[4].Texts[0] != "邮箱：synthetic@example.test" || len(cases[4].Expected) != 1 || cases[4].Expected[0].Field != 0 || cases[4].Expected[0].Label != "private_email" || cases[4].Expected[0].Start != 3 || cases[4].Expected[0].End != 25 {
		t.Fatal("text-only diagnostic diverged from the original case 4 annotation")
	}
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	directory := t.TempDir()
	larger, largerErr := runPrivacyRoundtripWith(context.Background(), privacyRoundtripFixture("empty"), status, spec, directory)
	smaller, smallerErr := runPrivacySmallRoundtripWith(context.Background(), privacyRoundtripFixture("invalid_span"), status, spec, directory)
	if largerErr == nil || smallerErr == nil || larger.UpstreamRequests != 0 || smaller.UpstreamRequests != 0 {
		t.Fatal("tool diagnostic failures were not retained")
	}
	retained := map[string][]byte{}
	for _, name := range []string{"roundtrip-batch-00.response.json", "small-roundtrip-batch-00.response.json"} {
		data, err := os.ReadFile(filepath.Join(directory, "synthetic-only", name))
		if err != nil {
			t.Fatal("tool diagnostic raw failure evidence missing")
		}
		retained[name] = data
	}
	report, err := runPrivacyTextRoundtripWith(context.Background(), privacyRoundtripFixture("valid"), status, spec, directory)
	if err != nil {
		t.Fatalf("text-only roundtrip failed: %s (%v)", report.ErrorCode, err)
	}
	if report.Case != "email-text-only" || report.Fields != 1 || report.Entities != 1 || report.Batches == 0 || report.UpstreamRequests != 2 || !report.CaptureBlocked || !report.OutboundSensitiveWordsAbsent || !report.JSONRestored || !report.SSERestored || report.ToolCheckApplicable || report.ToolArgumentsRestored || report.NumberCheckApplicable || report.ExactNumbersPreserved || !report.RedactionCapabilityUnknown {
		t.Fatalf("text-only result claimed untested behavior or missed text restoration: %+v", report)
	}
	for name, before := range retained {
		after, err := os.ReadFile(filepath.Join(directory, "synthetic-only", name))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("text-only positive case replaced tool failure evidence")
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "synthetic-only", "text-roundtrip-batch-00.response.json"))
	if err != nil || !json.Valid(data) {
		t.Fatal("text-only raw response was not independently retained")
	}
	safe, err := json.Marshal(report)
	if err != nil || bytes.Contains(safe, []byte("synthetic@example.test")) || bytes.Contains(safe, []byte("<EMAIL:")) {
		t.Fatal("text-only safe report contained raw text or a mapping")
	}
}

func TestPrivacyTextRoundtripMissWrongCoordinatesOrLabelSendNothing(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	for _, mode := range []string{"empty", "invalid_span", "wrong_label", "missing_field", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			report, err := runPrivacyTextRoundtripWith(ctx, privacyRoundtripFixture(mode), status, spec, "")
			if err == nil || report.Case != "email-text-only" || report.ErrorCode == "" || report.UpstreamRequests != 0 || report.JSONRestored || report.SSERestored || report.ToolCheckApplicable || report.ToolArgumentsRestored || report.NumberCheckApplicable || report.ExactNumbersPreserved || !report.RedactionCapabilityUnknown {
				t.Fatalf("incorrect text-only finding reached upstream or claimed unrelated checks: %+v", report)
			}
			if (mode == "wrong_label" || mode == "empty") && report.ErrorCode != "roundtrip_expected_entity_mismatch" {
				t.Fatal("text-only annotation was not checked against the actual model finding")
			}
		})
	}
}

func TestPrivacySmallRoundtripFailuresRemainClosed(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	for _, mode := range []string{"empty", "invalid_span", "missing_field", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			report, err := runPrivacySmallRoundtripWith(ctx, privacyRoundtripFixture(mode), status, spec, "")
			if err == nil || report.Case != "email-tools-small" || report.ErrorCode == "" || report.NumberCheckApplicable || report.ExactNumbersPreserved || report.UpstreamRequests != 0 || report.JSONRestored || report.SSERestored || !report.RedactionCapabilityUnknown {
				t.Fatalf("small failure sent upstream or claimed untested numeric behavior: %+v", report)
			}
		})
	}
}

func TestPrivacyRoundtripMissInvalidSpanMissingFieldAndCancelSendNothing(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: spec.RuntimeFingerprint()}}}
	}
	for _, mode := range []string{"empty", "invalid_span", "missing_field", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			report, err := runPrivacyRoundtripWith(ctx, privacyRoundtripFixture(mode), status, spec, "")
			if err == nil || report.ErrorCode == "" || report.UpstreamRequests != 0 || report.JSONRestored || report.SSERestored || !report.RedactionCapabilityUnknown {
				t.Fatalf("failure sent upstream or changed capability: %+v", report)
			}
		})
	}
}

func TestPrivacyRoundtripRejectsCapabilityStateAndPreservesNumberLexemes(t *testing.T) {
	spec := privacyRoundtripTestSpec(t)
	called := false
	generate := func(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error {
		called = true
		return nil
	}
	status := func() routerruntime.Status {
		return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "available", Ready: true, Fingerprint: spec.RuntimeFingerprint()}}}
	}
	report, err := runPrivacyRoundtripWith(context.Background(), generate, status, spec, "")
	if err == nil || called || report.UpstreamRequests != 0 {
		t.Fatal("roundtrip accepted an uncontrolled capability state")
	}
	if privacyRoundtripEqualJSON(`{"amount":932000.00}`, `{"amount":932000}`) || privacyRoundtripEqualJSON(`{"large":9007199254740993}`, `{"large":9007199254740992}`) || privacyRoundtripEqualJSON(`{} {}`, `{}`) {
		t.Fatal("comparison hid numeric or trailing-data changes")
	}
}
