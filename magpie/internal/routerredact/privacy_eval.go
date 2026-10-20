package routerredact

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routerruntime"
)

// PrivacyExpected uses Unicode code-point offsets, matching the source labeling
// contract. Text is private annotated input; it is never included in a report.
type PrivacyExpected struct {
	Field int    `json:"field"`
	Label string `json:"label"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

type PrivacyCase struct {
	Versions      Versions          `json:"versions"`
	Texts         []string          `json:"texts"`
	Expected      []PrivacyExpected `json:"expected"`
	ExpectBlocked bool              `json:"expectBlocked,omitempty"`
}

func (PrivacyCase) String() string                   { return "privacy evaluation case (private)" }
func (PrivacyCase) GoString() string                 { return "privacy evaluation case (private)" }
func (PrivacyCase) MarshalJSON() ([]byte, error)     { return []byte(`{"private":true}`), nil }
func (PrivacyExpected) String() string               { return "privacy expected entity (private)" }
func (PrivacyExpected) GoString() string             { return "privacy expected entity (private)" }
func (PrivacyExpected) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }

type PrivacyLabelEvaluation struct {
	Expected        int     `json:"expected"`
	Predicted       int     `json:"predicted"`
	TruePositive    int     `json:"truePositive"`
	FalsePositive   int     `json:"falsePositive"`
	FalseNegative   int     `json:"falseNegative"`
	ExactSpans      int     `json:"exactSpans"`
	Precision       float64 `json:"precision"`
	Recall          float64 `json:"recall"`
	ExactSpanRecall float64 `json:"exactSpanRecall"`
}

type PrivacyCaseEvaluation struct {
	Index           int      `json:"index"`
	Versions        Versions `json:"versions"`
	DurationMS      int64    `json:"durationMs"`
	ExpectedBlocked bool     `json:"expectedBlocked"`
	Blocked         bool     `json:"blocked"`
	FailureCode     string   `json:"failureCode,omitempty"`
	Entities        int      `json:"entities"`
}

// PrivacyEvaluation is descriptive evidence. It has no pass/capability field and
// cannot enable a runtime capability. Zero-denominator rates are zero.
type PrivacyEvaluation struct {
	Source            string                            `json:"source,omitempty"`
	Fingerprints      *PrivacyFingerprints              `json:"fingerprints,omitempty"`
	Cases             []PrivacyCaseEvaluation           `json:"cases"`
	Labels            map[string]PrivacyLabelEvaluation `json:"labels"`
	InvalidBlocked    int                               `json:"invalidBlocked"`
	ExpectedBlocks    int                               `json:"expectedBlocks"`
	UnexpectedBlocks  int                               `json:"unexpectedBlocks"`
	UnexpectedAccepts int                               `json:"unexpectedAccepts"`
	DurationMS        int64                             `json:"durationMs"`
}

// PrivacyDiagnosticRuntime is implemented by the already-owned runtime Manager.
// Generate uses that Manager's single process and queue without setting its
// capability gate. Production extraction must use Require/Invoke instead.
type PrivacyDiagnosticRuntime interface {
	Generate(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error
	Status() routerruntime.Status
}

type privacyDiagnosticInvoker struct {
	runtime     PrivacyDiagnosticRuntime
	fingerprint string
}

// Package-private marker: only this explicit measurement wrapper can use an
// unaccepted material suite. It never changes the Manager's sampled state.
func (privacyDiagnosticInvoker) privacyDiagnosticOnly() {}

func (d privacyDiagnosticInvoker) Require(p routerruntime.Purpose) error {
	if p != routerruntime.Redaction || d.fingerprint == "" || d.Status().Capabilities[p].Fingerprint != d.fingerprint {
		return fail("redaction_unavailable")
	}
	return nil
}
func (d privacyDiagnosticInvoker) Status() routerruntime.Status { return d.runtime.Status() }
func (d privacyDiagnosticInvoker) Invoke(ctx context.Context, p routerruntime.Purpose, body []byte, consume func(*http.Response) error) error {
	if err := d.Require(p); err != nil {
		return err
	}
	return d.runtime.Generate(ctx, p, body, consume)
}

// EvaluatePrivacyDiagnostic is an explicit pre-acceptance measurement entry.
// Pass only the service's existing Manager, under the shared model/test lock.
// It neither constructs/starts a Manager nor writes CheckCapability or intent.
// Supplied case versions must match the fixed specification exactly.
func EvaluatePrivacyDiagnostic(ctx context.Context, runtime PrivacyDiagnosticRuntime, spec *PrivacySpecification, cases []PrivacyCase) (PrivacyEvaluation, error) {
	if runtime == nil || spec == nil {
		return PrivacyEvaluation{}, fail("redaction_unavailable")
	}
	for _, c := range cases {
		if c.Versions != spec.Versions() {
			return PrivacyEvaluation{}, fail("redaction_unavailable")
		}
	}
	report, err := EvaluatePrivacy(ctx, NewRuntimeExtractor(privacyDiagnosticInvoker{runtime, spec.RuntimeFingerprint()}, spec), cases)
	fingerprints := spec.Fingerprints()
	report.Source = PrivacySourceCommit
	report.Fingerprints = &fingerprints
	return report, err
}

func privacyExpectedEntities(ctx context.Context, c PrivacyCase, labels map[string]string) ([]Entity, error) {
	if len(c.Texts) > 256 || len(c.Expected) > maxEntities {
		return nil, fail("redaction_invalid_input")
	}
	rawBytes := 0
	boundaries := make([][]int, len(c.Texts))
	for field, text := range c.Texts {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		rawBytes += len(text)
		if !utf8.ValidString(text) || rawBytes > 1<<20 {
			return nil, fail("redaction_invalid_input")
		}
		offsets := make([]int, 0, utf8.RuneCountInString(text)+1)
		for offset := range text {
			if len(offsets)%1024 == 0 {
				if err := contextError(ctx); err != nil {
					return nil, err
				}
			}
			offsets = append(offsets, offset)
		}
		boundaries[field] = append(offsets, len(text))
	}
	entities := make([]Entity, 0, len(c.Expected))
	for _, want := range c.Expected {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		category, ok := labels[want.Label]
		if !ok || want.Field < 0 || want.Field >= len(c.Texts) || want.Start < 0 || want.End <= want.Start {
			return nil, fail("redaction_invalid_input")
		}
		text := c.Texts[want.Field]
		offsets := boundaries[want.Field]
		if want.End >= len(offsets) {
			return nil, fail("redaction_invalid_input")
		}
		actual := text[offsets[want.Start]:offsets[want.End]]
		if want.Text != "" && actual != want.Text {
			return nil, fail("redaction_invalid_input")
		}
		entities = append(entities, Entity{Field: want.Field, Start: offsets[want.Start], End: offsets[want.End], Text: actual, Category: category})
	}
	validated, err := validateEntities(c.Texts, Result{Complete: true, Entities: entities})
	if err != nil || len(validated) != len(entities) {
		return nil, fail("redaction_invalid_input")
	}
	return validated, nil
}

// EvaluatePrivacy runs only the supplied Extractor. Use NewRuntimeExtractor with
// the service's existing Manager; this function never creates HTTP transports,
// loads models, changes capability state, or logs input. The caller owns the
// extractor/Manager lifetime. Cancellation returns the partial safe report.
func EvaluatePrivacy(ctx context.Context, extractor Extractor, cases []PrivacyCase) (PrivacyEvaluation, error) {
	started := time.Now()
	report := PrivacyEvaluation{Labels: map[string]PrivacyLabelEvaluation{}}
	labels := PrivacyLabels()
	reverse := map[string]string{}
	for label, category := range labels {
		report.Labels[label] = PrivacyLabelEvaluation{}
		if old := reverse[category]; old != "" {
			return report, fail("redaction_invalid_input")
		}
		reverse[category] = label
	}
	if extractor == nil || len(cases) == 0 || len(cases) > 1024 {
		return report, fail("redaction_invalid_input")
	}
	// Validate the complete annotation set before starting model work.
	expected := make([][]Entity, len(cases))
	bytes := 0
	for i, c := range cases {
		if err := contextError(ctx); err != nil {
			return report, err
		}
		if c.Versions.Model == "" || c.Versions.Prompt == "" || c.Versions.Config == "" {
			return report, fail("redaction_invalid_input")
		}
		var err error
		expected[i], err = privacyExpectedEntities(ctx, c, labels)
		if err != nil {
			return report, err
		}
		for _, text := range c.Texts {
			bytes += len(text)
		}
		if bytes > 16<<20 {
			return report, fail("redaction_input_limit")
		}
	}
	for index, c := range cases {
		if err := contextError(ctx); err != nil {
			report.DurationMS = time.Since(started).Milliseconds()
			return report, err
		}
		begin := time.Now()
		result, err := extractor.Extract(ctx, Extraction{Versions: c.Versions, Texts: append([]string(nil), c.Texts...)})
		var predicted []Entity
		if err == nil {
			predicted, err = validateEntities(c.Texts, result)
		}
		for _, entity := range predicted {
			if reverse[entity.Category] == "" {
				err = fail("redaction_invalid_result")
				break
			}
		}
		entry := PrivacyCaseEvaluation{Index: index, Versions: c.Versions, DurationMS: time.Since(begin).Milliseconds(), ExpectedBlocked: c.ExpectBlocked, Blocked: err != nil}
		if c.ExpectBlocked {
			report.ExpectedBlocks++
		}
		if err != nil {
			report.InvalidBlocked++
			entry.FailureCode = "redaction_extraction_failed"
			var failure *Failure
			if errors.As(err, &failure) {
				entry.FailureCode = SafeFailureCode(failure.Code)
			}
			if !c.ExpectBlocked {
				report.UnexpectedBlocks++
			}
			predicted = nil
		} else {
			entry.Entities = len(predicted)
			if c.ExpectBlocked {
				report.UnexpectedAccepts++
			}
		}
		report.Cases = append(report.Cases, entry)
		if !c.ExpectBlocked {
			used := make([]bool, len(expected[index]))
			for _, want := range expected[index] {
				label := reverse[want.Category]
				metric := report.Labels[label]
				metric.Expected++
				report.Labels[label] = metric
			}
			for _, got := range predicted {
				label := reverse[got.Category]
				metric := report.Labels[label]
				metric.Predicted++
				match := -1
				// Prefer exact positions before matching another repeated value.
				for i, want := range expected[index] {
					if !used[i] && want == got {
						match = i
						break
					}
				}
				if match < 0 {
					for i, want := range expected[index] {
						if !used[i] && want.Field == got.Field && want.Category == got.Category && want.Text == got.Text {
							match = i
							break
						}
					}
				}
				if match >= 0 {
					used[match] = true
					metric.TruePositive++
					if expected[index][match] == got {
						metric.ExactSpans++
					}
				} else {
					metric.FalsePositive++
				}
				report.Labels[label] = metric
			}
			for i, want := range expected[index] {
				if !used[i] {
					label := reverse[want.Category]
					metric := report.Labels[label]
					metric.FalseNegative++
					report.Labels[label] = metric
				}
			}
		}
		if err := contextError(ctx); err != nil {
			report.DurationMS = time.Since(started).Milliseconds()
			return report, err
		}
	}
	for label, metric := range report.Labels {
		if metric.Predicted > 0 {
			metric.Precision = float64(metric.TruePositive) / float64(metric.Predicted)
		}
		if metric.Expected > 0 {
			metric.Recall = float64(metric.TruePositive) / float64(metric.Expected)
			metric.ExactSpanRecall = float64(metric.ExactSpans) / float64(metric.Expected)
		}
		report.Labels[label] = metric
	}
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}
