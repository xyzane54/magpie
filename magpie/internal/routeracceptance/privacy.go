package routeracceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

type privacyRepairEvidence struct {
	Suite               string                           `json:"suite"`
	Fingerprints        routerredact.PrivacyFingerprints `json:"fingerprints"`
	CaseIndices         []int                            `json:"caseIndices"`
	Evaluation          *routerredact.PrivacyEvaluation  `json:"evaluation,omitempty"`
	Heldout             *routerredact.PrivacyEvaluation  `json:"heldout,omitempty"`
	HeldoutVersion      string                           `json:"heldoutVersion,omitempty"`
	Calls               []privacyCallEvidence            `json:"calls"`
	Roundtrip           *privacyRoundtripEvidence        `json:"roundtrip,omitempty"`
	SmallRoundtrip      *privacyRoundtripEvidence        `json:"smallRoundtrip,omitempty"`
	TextRoundtrip       *privacyRoundtripEvidence        `json:"textRoundtrip,omitempty"`
	Lifecycle           *privacyLifecycleEvidence        `json:"lifecycle,omitempty"`
	DurationMS          int64                            `json:"durationMs"`
	CapabilitiesUnknown bool                             `json:"capabilitiesUnknown"`
}

type privacyCallEvidence struct {
	Material       string                                     `json:"material"`
	Case           int                                        `json:"case"`
	Batch          int                                        `json:"batch"`
	RequestSHA256  string                                     `json:"requestSha256"`
	RequestBytes   int                                        `json:"requestBytes"`
	Fields         int                                        `json:"fields"`
	MaxTokens      int                                        `json:"maxTokens"`
	Timing         routerruntime.GenerationDiagnosticSnapshot `json:"timing"`
	Usage          map[string]int                             `json:"usage,omitempty"`
	ServerTimings  map[string]float64                         `json:"serverTimings,omitempty"`
	FinishReason   string                                     `json:"finishReason,omitempty"`
	ResponseBytes  int                                        `json:"responseBytes"`
	ResponseSHA256 string                                     `json:"responseSha256,omitempty"`
	ParseMS        float64                                    `json:"diagnosticParseMs"`
	ParseCode      string                                     `json:"parseCode,omitempty"`
	ErrorCode      string                                     `json:"errorCode,omitempty"`
	ActiveAfter    int                                        `json:"activeAfter"`
	WaitingAfter   int                                        `json:"waitingAfter"`
}

func validatePrivacyDiagnostic(o options) error {
	if o.PrivacyDiagnostic == "" {
		return nil
	}
	if (o.PrivacyDiagnostic != "representative" && o.PrivacyDiagnostic != "full") || o.Model != "spark-x2.5-1.7B" || o.PrepareOnly || o.InstallOnly || o.BusinessPreacceptance {
		return errors.New("privacy diagnostics require the fixed 1.7B representative/full suite exclusively")
	}
	return nil
}

// This recorder is reachable only from the explicit fixed-synthetic CLI suite.
// It delegates to the service's owned Manager, never a model HTTP endpoint.
type privacyRecordingRuntime struct {
	manager             *routerruntime.Manager
	spec                *routerredact.PrivacySpecification
	directory, material string
	index, batch        int
	calls               *[]privacyCallEvidence
}

func (r *privacyRecordingRuntime) Status() routerruntime.Status { return r.manager.Status() }
func (r *privacyRecordingRuntime) Generate(ctx context.Context, p routerruntime.Purpose, body []byte, consume func(*http.Response) error) error {
	r.batch++
	entry := privacyCallEvidence{Material: r.material, Case: r.index, Batch: r.batch, RequestSHA256: hashDiagnostic(body), RequestBytes: len(body)}
	var request struct {
		MaxTokens int `json:"max_tokens"`
		Messages  []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	var input struct {
		Fields []struct {
			Field int    `json:"field"`
			Text  string `json:"text"`
		} `json:"fields"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.Messages) != 2 || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		return errors.New("invalid fixed privacy diagnostic request")
	}
	entry.MaxTokens, entry.Fields = request.MaxTokens, len(input.Fields)
	in := routerredact.Extraction{Versions: r.spec.Versions(), Texts: make([]string, len(input.Fields))}
	for i, f := range input.Fields {
		if f.Field != i {
			return errors.New("invalid fixed privacy diagnostic field")
		}
		in.Texts[i] = f.Text
	}
	var measured routerruntime.GenerationDiagnostics
	call := routerruntime.WithGenerationDiagnostics(ctx, &measured)
	stem := fmt.Sprintf("%s-case-%02d-batch-%02d", r.material, r.index, r.batch)
	err := r.manager.Generate(call, p, body, func(response *http.Response) error {
		raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if e != nil {
			return e
		}
		if len(raw) > 1<<20 {
			return errors.New("diagnostic_response_limit")
		}
		entry.ResponseBytes, entry.ResponseSHA256 = len(raw), hashDiagnostic(raw)
		if e = os.WriteFile(filepath.Join(r.directory, stem+".json"), raw, 0600); e != nil {
			return e
		}
		privacyResponseMetadata(raw, &entry)
		started := time.Now()
		_, parseErr := r.spec.Parse(ctx, in, raw)
		entry.ParseMS = milliseconds(time.Since(started))
		entry.ParseCode = routerredact.PrivacyParseDiagnosticCode(parseErr)
		copy := *response
		copy.Body = io.NopCloser(bytes.NewReader(raw))
		defer copy.Body.Close()
		return consume(&copy)
	})
	entry.Timing = measured.Snapshot()
	if err != nil {
		entry.ErrorCode = safeDiagnosticError(err)
	}
	var httpError *routerruntime.GenerationHTTPError
	if errors.As(err, &httpError) {
		entry.ErrorCode = "http_error"
		if raw := httpError.DiagnosticBody(); len(raw) > 0 {
			if e := os.WriteFile(filepath.Join(r.directory, stem+".http-error.json"), raw, 0600); e != nil {
				return e
			}
		}
	}
	status := r.manager.Status()
	entry.ActiveAfter, entry.WaitingAfter = status.Active, status.Waiting
	*r.calls = append(*r.calls, entry)
	return err
}

func privacyResponseMetadata(raw []byte, entry *privacyCallEvidence) {
	var root struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage   map[string]json.RawMessage `json:"usage"`
		Timings map[string]float64         `json:"timings"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return
	}
	if len(root.Choices) == 1 {
		switch root.Choices[0].FinishReason {
		case "stop", "length", "tool_calls", "content_filter":
			entry.FinishReason = root.Choices[0].FinishReason
		default:
			entry.FinishReason = "other"
		}
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if raw, ok := root.Usage[key]; ok {
			var value int
			if json.Unmarshal(raw, &value) != nil {
				continue
			}
			if entry.Usage == nil {
				entry.Usage = map[string]int{}
			}
			entry.Usage[key] = value
		}
	}
	for _, key := range []string{"prompt_n", "prompt_ms", "predicted_n", "predicted_ms", "cache_n"} {
		if value, ok := root.Timings[key]; ok {
			if entry.ServerTimings == nil {
				entry.ServerTimings = map[string]float64{}
			}
			entry.ServerTimings[key] = value
		}
	}
}

func runPrivacyRepairDiagnostic(ctx context.Context, m *routerruntime.Manager, model routermanifest.Model, rt routermanifest.Runtime, o options) (report privacyRepairEvidence, result error) {
	started := time.Now()
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		report.CapabilitiesUnknown = unknownCapabilities(m) == nil
	}()
	spec, err := routerredact.NewPrivacySpecification(routerredact.ModelBinding{ModelID: model.ID, RuntimeID: rt.ID})
	if err != nil {
		return report, err
	}
	report.Suite, report.Fingerprints = spec.CapabilitySuite(), spec.Fingerprints()
	cases, err := routerredact.PrivacyEvaluationCases(spec)
	if err != nil {
		return report, err
	}
	indices := []int{0, 1, 4, 3, 49, 53}
	if o.PrivacyDiagnostic == "full" {
		indices = make([]int, len(cases))
		for i := range cases {
			indices[i] = i
		}
	}
	report.CaseIndices = indices
	directory := filepath.Join(filepath.Dir(o.Output), "privacy-synthetic-only")
	if err = os.Mkdir(directory, 0700); err != nil {
		return report, err
	}
	recorder := &privacyRecordingRuntime{manager: m, spec: spec, directory: directory, calls: &report.Calls, material: "fixed56"}
	selected := make([]routerredact.PrivacyCase, len(indices))
	for i, index := range indices {
		selected[i] = cases[index]
	}
	// Evaluate individually only in representative mode to retain original corpus
	// indices. Each case still owns one deadline across all of its complete batches.
	if o.PrivacyDiagnostic == "representative" {
		for _, index := range indices {
			recorder.index, recorder.batch = index, 0
			evaluation, e := routerredact.EvaluatePrivacyDiagnostic(ctx, recorder, spec, []routerredact.PrivacyCase{cases[index]})
			if e != nil {
				return report, e
			}
			evaluation.Cases[0].Index = index
			if report.Evaluation == nil {
				report.Evaluation = &evaluation
			} else {
				mergePrivacyEvaluation(report.Evaluation, evaluation)
			}
			fmt.Fprintf(os.Stderr, "privacy case %d blocked=%v entities=%d duration=%dms\n", index, evaluation.Cases[0].Blocked, evaluation.Cases[0].Entities, evaluation.Cases[0].DurationMS)
		}
	} else {
		// The evaluator makes cases sequentially; its fixed order and the callback
		// wrapper below preserve case/batch indices, including blocked-before-call.
		for index, c := range selected {
			recorder.index, recorder.batch = index, 0
			evaluation, e := routerredact.EvaluatePrivacyDiagnostic(ctx, recorder, spec, []routerredact.PrivacyCase{c})
			if e != nil {
				return report, e
			}
			evaluation.Cases[0].Index = index
			if report.Evaluation == nil {
				report.Evaluation = &evaluation
			} else {
				mergePrivacyEvaluation(report.Evaluation, evaluation)
			}
			fmt.Fprintf(os.Stderr, "privacy case %d blocked=%v entities=%d duration=%dms\n", index, evaluation.Cases[0].Blocked, evaluation.Cases[0].Entities, evaluation.Cases[0].DurationMS)
		}
		heldout, e := routerredact.PrivacyHeldoutCases(spec)
		if e != nil {
			return report, e
		}
		report.HeldoutVersion = routerredact.PrivacyHeldoutVersion
		for index, c := range heldout {
			recorder.material, recorder.index, recorder.batch = "heldout", index, 0
			evaluation, e := routerredact.EvaluatePrivacyDiagnostic(ctx, recorder, spec, []routerredact.PrivacyCase{c})
			if e != nil {
				return report, e
			}
			evaluation.Cases[0].Index = index
			if report.Heldout == nil {
				report.Heldout = &evaluation
			} else {
				mergePrivacyEvaluation(report.Heldout, evaluation)
			}
		}
		roundtrip, e := runPrivacyRoundtrip(ctx, m, spec, filepath.Dir(o.Output))
		report.Roundtrip = &roundtrip
		result = errors.Join(result, e)
		lifecycle, e := runPrivacyLifecycle(ctx, m, spec, filepath.Dir(o.Output))
		report.Lifecycle = &lifecycle
		result = errors.Join(result, e)
	}
	if o.PrivacyDiagnostic == "representative" {
		roundtrip, e := runPrivacyRoundtrip(ctx, m, spec, filepath.Dir(o.Output))
		report.Roundtrip = &roundtrip
		result = errors.Join(result, e)
	}
	small, e := runPrivacySmallRoundtrip(ctx, m, spec, filepath.Dir(o.Output))
	report.SmallRoundtrip = &small
	result = errors.Join(result, e)
	text, e := runPrivacyTextRoundtrip(ctx, m, spec, filepath.Dir(o.Output))
	report.TextRoundtrip = &text
	result = errors.Join(result, e)
	return report, result
}

func mergePrivacyEvaluation(target *routerredact.PrivacyEvaluation, source routerredact.PrivacyEvaluation) {
	target.Cases = append(target.Cases, source.Cases...)
	target.DurationMS += source.DurationMS
	target.InvalidBlocked += source.InvalidBlocked
	target.ExpectedBlocks += source.ExpectedBlocks
	target.UnexpectedBlocks += source.UnexpectedBlocks
	target.UnexpectedAccepts += source.UnexpectedAccepts
	for label, m := range source.Labels {
		a := target.Labels[label]
		a.Expected += m.Expected
		a.Predicted += m.Predicted
		a.TruePositive += m.TruePositive
		a.FalsePositive += m.FalsePositive
		a.FalseNegative += m.FalseNegative
		a.ExactSpans += m.ExactSpans
		if a.Predicted > 0 {
			a.Precision = float64(a.TruePositive) / float64(a.Predicted)
		}
		if a.Expected > 0 {
			a.Recall = float64(a.TruePositive) / float64(a.Expected)
			a.ExactSpanRecall = float64(a.ExactSpans) / float64(a.Expected)
		}
		target.Labels[label] = a
	}
}
