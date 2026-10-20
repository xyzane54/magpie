package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerruntime"
)

func privacyEvaluationCases(t *testing.T, versions Versions) []PrivacyCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/privacy-eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []PrivacyCase
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("invalid evaluation fixture")
	}
	for i := range cases {
		cases[i].Versions = versions
	}
	return cases
}

func TestPrivacyEvaluationCorpusHasAllLabelsAndSafeMetrics(t *testing.T) {
	spec := privacyTestSpecification(t)
	cases := privacyEvaluationCases(t, spec.Versions())
	labels := PrivacyLabels()
	covered := map[string]bool{}
	annotations := map[string][]PrivacyExpected{}
	for _, c := range cases {
		if _, err := privacyExpectedEntities(context.Background(), c, labels); err != nil {
			t.Fatal("invalid corpus annotation", err)
		}
		for _, want := range c.Expected {
			covered[want.Label] = true
			text := c.Texts[want.Field]
			duplicate := false
			for _, previous := range annotations[text] {
				if previous.Label == want.Label && previous.Start == want.Start && previous.End == want.End {
					duplicate = true
				}
			}
			if !duplicate {
				annotations[text] = append(annotations[text], want)
			}
		}
	}
	if len(covered) != 49 {
		t.Fatal("evaluation corpus does not cover all fixed labels")
	}
	runtime := &privacyTestRuntime{fingerprint: spec.RuntimeFingerprint(), suite: spec.CapabilitySuite(), classify: func(_ context.Context, _ int, fields []privacyTestField) ([]byte, error) {
		results := []any{}
		for _, field := range fields {
			findings := []privacyTestFinding{}
			for _, want := range annotations[field.Text] {
				runes := []rune(field.Text)
				findings = append(findings, privacyTestFinding{Label: want.Label, Start: want.Start, End: want.End, Text: string(runes[want.Start:want.End]), Confidence: 1})
			}
			results = append(results, map[string]any{"field": field.Field, "complete": true, "findings": findings})
		}
		content, _ := json.Marshal(map[string]any{"complete": true, "fields": results})
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
		return raw, nil
	}}
	report, err := EvaluatePrivacy(context.Background(), NewRuntimeExtractor(runtime, spec), cases)
	if err != nil {
		t.Fatal(err)
	}
	if report.InvalidBlocked != 1 || report.ExpectedBlocks != 1 || report.UnexpectedBlocks != 0 || report.UnexpectedAccepts != 0 || len(report.Cases) != len(cases) {
		t.Fatal("evaluation completion/block counts incorrect", report.InvalidBlocked, report.UnexpectedBlocks, report.UnexpectedAccepts, report.Cases)
	}
	for label, metric := range report.Labels {
		if metric.Expected < 1 || metric.Precision != 1 || metric.Recall != 1 || metric.ExactSpanRecall != 1 {
			t.Fatalf("synthetic metric mismatch for %s", label)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"张三", "synthetic@example.test", "sk-synthetic-secret-017", "932000元", "texts", "findings", "available", "passed"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("evaluation report contains private/acceptance content %s", secret)
		}
	}
	if got := fmt.Sprintf("%v %#v", cases[0], cases[0].Expected[0]); strings.Contains(got, cases[0].Texts[0]) {
		t.Fatal("case formatting leaked annotated input")
	}
	encoded, _ = json.Marshal(cases)
	if bytes.Contains(encoded, []byte(cases[0].Texts[0])) {
		t.Fatal("accidental case serialization leaked text")
	}
}

func TestPrivacyEvaluationClassMetricsSeparateExactSpan(t *testing.T) {
	versions := testInput().Versions
	cases := []PrivacyCase{{Versions: versions, Texts: []string{"Alice Bob Alice"}, Expected: []PrivacyExpected{{Field: 0, Label: "private_person", Start: 0, End: 5}}}}
	extractor := ExtractorFunc(func(context.Context, Extraction) (Result, error) {
		return Result{Complete: true, Entities: []Entity{{Field: 0, Start: 10, End: 15, Text: "Alice", Category: "PERSON"}, {Field: 0, Start: 6, End: 9, Text: "Bob", Category: "PERSON"}}}, nil
	})
	report, err := EvaluatePrivacy(context.Background(), extractor, cases)
	if err != nil {
		t.Fatal(err)
	}
	metric := report.Labels["private_person"]
	if metric.TruePositive != 1 || metric.FalsePositive != 1 || metric.FalseNegative != 0 || metric.Precision != 0.5 || metric.Recall != 1 || metric.ExactSpanRecall != 0 {
		t.Fatal("classification and exact-span metrics conflated", metric)
	}
}

func TestPrivacyEvaluationInvalidOutputsBlockedAndErrorsPrivate(t *testing.T) {
	cases := []PrivacyCase{{Versions: testInput().Versions, Texts: []string{"Alice"}, Expected: []PrivacyExpected{{Field: 0, Label: "private_person", Start: 0, End: 5}}}}
	for _, extractor := range []Extractor{ExtractorFunc(func(context.Context, Extraction) (Result, error) {
		return Result{}, errors.New("Alice raw backend secret")
	}), ExtractorFunc(func(context.Context, Extraction) (Result, error) {
		return Result{Complete: true, Entities: []Entity{{Field: 0, Start: 0, End: 1, Text: "wrong", Category: "PERSON"}}}, nil
	}), ExtractorFunc(func(context.Context, Extraction) (Result, error) {
		return Result{Complete: true, Entities: []Entity{{Field: 0, Start: 0, End: 5, Text: "Alice", Category: "UNKNOWN"}}}, nil
	})} {
		report, err := EvaluatePrivacy(context.Background(), extractor, cases)
		if err != nil {
			t.Fatal(err)
		}
		if report.InvalidBlocked != 1 || report.UnexpectedBlocks != 1 || report.Labels["private_person"].FalseNegative != 1 {
			t.Fatal("invalid response did not count as blocked/missed")
		}
		raw, _ := json.Marshal(report)
		if bytes.Contains(raw, []byte("Alice")) || bytes.Contains(raw, []byte("backend")) {
			t.Fatal("evaluation reflected private extractor error")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := EvaluatePrivacy(ctx, synthetic("Alice"), cases)
	if err == nil || len(report.Cases) != 0 {
		t.Fatal("evaluation ignored cancellation")
	}
	bad := cases
	bad[0].Expected = []PrivacyExpected{{Field: 0, Label: "private_person", Start: 0, End: 50}}
	if _, err := EvaluatePrivacy(context.Background(), synthetic("Alice"), bad); err == nil {
		t.Fatal("invalid annotations accepted")
	}
}

type privacyTestDiagnosticRuntime struct {
	*privacyTestRuntime
	generated int
}

func (r *privacyTestDiagnosticRuntime) Generate(ctx context.Context, p routerruntime.Purpose, body []byte, consume func(*http.Response) error) error {
	r.generated++
	return r.privacyTestRuntime.Invoke(ctx, p, body, consume)
}

func TestPrivacyDiagnosticEvaluationUsesOwnedRuntimeWithoutCapabilityWrites(t *testing.T) {
	_, runtime, spec := privacyTestExtractor(t, map[string]string{"Alice": "private_person"})
	owned := &privacyTestDiagnosticRuntime{privacyTestRuntime: runtime}
	cases := []PrivacyCase{{Versions: spec.Versions(), Texts: []string{"Alice"}, Expected: []PrivacyExpected{{Field: 0, Label: "private_person", Start: 0, End: 5}}}}
	report, err := EvaluatePrivacyDiagnostic(context.Background(), owned, spec, cases)
	if err != nil {
		t.Fatal(err)
	}
	if owned.generated != 1 || report.Source != PrivacySourceCommit || report.Fingerprints == nil || report.Fingerprints.Model != spec.Versions().Model || report.Labels["private_person"].Recall != 1 {
		t.Fatal("owned runtime diagnostic was not used")
	}
	owned.fingerprint = "different-selected-model"
	report, err = EvaluatePrivacyDiagnostic(context.Background(), owned, spec, cases)
	if err != nil {
		t.Fatal(err)
	}
	if report.InvalidBlocked != 1 || owned.generated != 1 {
		t.Fatal("diagnostic model-binding mismatch reached Generate")
	}
	cases[0].Versions.Prompt = "judgment-version"
	if _, err := EvaluatePrivacyDiagnostic(context.Background(), owned, spec, cases); err == nil {
		t.Fatal("diagnostic replaced redaction versions")
	}
}
