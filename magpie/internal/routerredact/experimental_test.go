package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerruntime"
)

func TestExperimentalPrivacyVersionsPreserveFormalMaterialAndSeparateCache(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		t.Run(modelID, func(t *testing.T) {
			binding := ModelBinding{ModelID: modelID, RuntimeID: "windows-amd64-cpu"}
			formal, err := NewPrivacySpecification(binding)
			if err != nil {
				t.Fatal(err)
			}
			trial, err := NewExperimentalPrivacySpecification(binding)
			if err != nil {
				t.Fatal(err)
			}
			if formal.Experimental() || formal.Budget() != 15*time.Second || !trial.Experimental() || trial.Budget() != 30*time.Second || !strings.HasPrefix(trial.CapabilitySuite(), "privacy-trial-v1:") {
				t.Fatal("trial changed the formal policy or failed to bind its budget")
			}
			f, tr := formal.Fingerprints(), trial.Fingerprints()
			if f.Config != "2b3b8a2b1ae307d29f71d2909ff933f642c2559d1e6066376ee4e96f99ad36cb" || f.Suite != "privacy-go-v5:a211c0e081094635477fa8cd37f1f573a9936296cbeb5f3e2d1831ba2bce018f" || f.Config == tr.Config || f.Suite == tr.Suite {
				t.Fatal("formal v5 evidence changed or trial inherited its config/suite")
			}
			f.Config, f.Suite, tr.Config, tr.Suite = "", "", "", ""
			if !reflect.DeepEqual(f, tr) {
				t.Fatal("trial changed model, prompt, schema or label material")
			}
			formalBody, fe := formal.Build(context.Background(), Extraction{Versions: formal.Versions(), Texts: []string{"fixture"}})
			trialBody, te := trial.Build(context.Background(), Extraction{Versions: trial.Versions(), Texts: []string{"fixture"}})
			if fe != nil || te != nil || !bytes.Equal(formalBody, trialBody) {
				t.Fatal("trial changed generation material or strict request schema")
			}
			var request struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(trialBody, &request) != nil || request.Model != modelID {
				t.Fatal("trial request did not use its bound model", request.Model)
			}
			if _, err := formal.ResolveVersions(trial.RoutingVersions()); err == nil {
				t.Fatal("formal version resolver accepted trial evidence")
			}
			if _, err := trial.ResolveVersions(formal.RoutingVersions()); err == nil {
				t.Fatal("trial version resolver accepted formal evidence")
			}
			calls := 0
			pipeline := New(Options{Experimental: true, Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) {
				calls++
				return Result{Complete: true}, nil
			})})
			defer pipeline.Close()
			in := testInput("fixture")
			in.Versions = formal.Versions()
			first := mustPrepare(t, pipeline, in)
			first.Close()
			second := mustPrepare(t, pipeline, in)
			second.Close()
			if calls != 1 || pipeline.Stats().CacheHits != 1 {
				t.Fatal("formal complete prefix fixture did not hit its own cache")
			}
			in.Versions = trial.Versions()
			third := mustPrepare(t, pipeline, in)
			third.Close()
			if calls != 2 || pipeline.Stats().CacheHits != 1 {
				t.Fatal("trial reused formally versioned recognition cache")
			}
		})
	}
}

type experimentalPrivacyRuntime struct {
	fingerprint, suite string
	calls, verified    int
	deadlines          []time.Time
	fields             []int
	classify           func(context.Context, int, []privacyTestField) ([]byte, error)
}

func (r *experimentalPrivacyRuntime) Require(p routerruntime.Purpose) error {
	if p != routerruntime.Redaction {
		return routerruntime.ErrCapability
	}
	return nil
}
func (r *experimentalPrivacyRuntime) VerifyBinding(fingerprint, suite string) error {
	r.verified++
	if fingerprint != r.fingerprint || suite != r.suite {
		return routerruntime.ErrCapability
	}
	return nil
}
func (r *experimentalPrivacyRuntime) Status() routerruntime.Status {
	return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {State: "unknown", Fingerprint: r.fingerprint}}}
}
func (r *experimentalPrivacyRuntime) Invoke(ctx context.Context, purpose routerruntime.Purpose, raw []byte, consume func(*http.Response) error) error {
	if purpose != routerruntime.Redaction {
		return routerruntime.ErrCapability
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("fixture_unbounded_call")
	}
	r.deadlines = append(r.deadlines, deadline)
	var request struct {
		Stream   bool                             `json:"stream"`
		Kwargs   map[string]any                   `json:"chat_template_kwargs"`
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Stream || request.Kwargs["enable_thinking"] != false || len(request.Messages) != 2 {
		return errors.New("fixture_invalid_request")
	}
	var input struct {
		Fields []privacyTestField `json:"fields"`
	}
	if json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		return errors.New("fixture_invalid_fields")
	}
	r.calls++
	r.fields = append(r.fields, len(input.Fields))
	raw, err := r.classify(ctx, r.calls, input.Fields)
	if err != nil {
		return err
	}
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw))}
	defer response.Body.Close()
	return consume(response)
}

// Only package tests can inject this fixture verifier. The public experimental
// constructor requires the concrete handle made by the service-owned Manager.
func experimentalPrivacyFixture(t *testing.T) (*RuntimeExtractor, *experimentalPrivacyRuntime, *PrivacySpecification) {
	t.Helper()
	spec, err := NewExperimentalPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &experimentalPrivacyRuntime{fingerprint: spec.RuntimeFingerprint(), suite: spec.CapabilitySuite(), classify: func(_ context.Context, _ int, fields []privacyTestField) ([]byte, error) {
		return privacyTestEnvelope(fields, map[string]string{"Alice": "private_person"}), nil
	}}
	return &RuntimeExtractor{runtime: runtime, specification: spec, experimental: runtime}, runtime, spec
}

func TestExperimentalPrivacyBatchesShareOneThirtySecondDeadline(t *testing.T) {
	extractor, runtime, spec := experimentalPrivacyFixture(t)
	texts := make([]string, 17)
	for index := range texts {
		texts[index] = "Alice"
	}
	result, err := extractor.Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: texts})
	if err != nil || !result.Complete || len(result.Entities) != 17 || !reflect.DeepEqual(runtime.fields, []int{16, 1}) || runtime.verified < 3 {
		t.Fatal("trial lost full-field coverage, findings or binding checks", err)
	}
	if len(runtime.deadlines) != 2 || !runtime.deadlines[0].Equal(runtime.deadlines[1]) {
		t.Fatal("trial renewed its request deadline per batch")
	}
	remaining := time.Until(runtime.deadlines[0])
	if remaining <= 20*time.Second || remaining > 30*time.Second {
		t.Fatal("trial did not use its independently versioned 30-second budget")
	}
	if state := runtime.Status().Capabilities[routerruntime.Redaction]; state.State != "unknown" || state.Ready || state.Suite != "" {
		t.Fatal("trial fixture fabricated formal capability acceptance")
	}
	for index, entity := range result.Entities {
		if entity.Field != index || entity.Text != "Alice" || entity.Start != 0 || entity.End != 5 {
			t.Fatal("trial changed field identity or exact source coordinates")
		}
	}
}

func TestExperimentalPrivacyRequiresExplicitHandleAndChecksCacheAvailability(t *testing.T) {
	extractor, runtime, spec := experimentalPrivacyFixture(t)
	if result, err := NewRuntimeExtractor(runtime, spec).Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{"Alice"}}); err == nil || result.Complete || runtime.calls != 0 {
		t.Fatal("ordinary constructor implicitly enabled experimental recognition")
	}
	if _, err := NewExperimentalRuntimeExtractor(nil, spec).Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{"Alice"}}); err == nil {
		t.Fatal("nil trial handle admitted recognition")
	}
	pipeline := New(Options{Extractor: extractor, Experimental: true})
	defer pipeline.Close()
	in := testInput("Alice")
	in.Versions = spec.Versions()
	prepared := mustPrepare(t, pipeline, in)
	prepared.Close()
	before := runtime.calls
	runtime.suite = "revoked"
	if prepared, err := pipeline.Prepare(context.Background(), in); err == nil || prepared != nil || runtime.calls != before {
		t.Fatal("complete cached trial result bypassed its current immutable binding")
	}
}

func TestExperimentalPrivacyLastBatchFailureHasZeroOutbound(t *testing.T) {
	for _, mode := range []string{"missing", "label", "bounds", "truncated", "error", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			extractor, runtime, spec := experimentalPrivacyFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			runtime.classify = func(call context.Context, index int, fields []privacyTestField) ([]byte, error) {
				if index == 1 {
					return privacyTestEnvelope(fields, map[string]string{"Alice": "private_person"}), nil
				}
				switch mode {
				case "error":
					return nil, errors.New("Alice private fixture error")
				case "timeout":
					<-call.Done()
					return nil, call.Err()
				case "cancel":
					cancel()
					return nil, call.Err()
				}
				finding := privacyParseFinding("private_person", "Alice", 0, 5)
				finish := "stop"
				fieldsOut := []any{privacyParseField(0, finding)}
				switch mode {
				case "missing":
					fieldsOut = []any{}
				case "label":
					finding["label"] = "unknown_sensitive"
				case "bounds":
					finding["end"] = 999
				case "truncated":
					finish = "length"
				}
				return privacyParseEnvelope(privacyParseContent(fieldsOut...), finish), nil
			}
			pipeline := New(Options{Extractor: extractor, Experimental: true})
			defer pipeline.Close()
			texts := make([]string, 17)
			for index := range texts {
				texts[index] = "Alice"
			}
			in := testInput(texts...)
			in.Versions, in.SessionVerified = spec.Versions(), false
			var outbound atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { outbound.Add(1); w.WriteHeader(http.StatusOK) }))
			defer server.Close()
			err := finalBoundaryExecute(ctx, pipeline, server.URL, finalBoundaryPlan{Input: in, ClientProtocol: Chat, CloudProtocol: Chat, CloudModel: "bound"}, io.Discard)
			if err == nil || outbound.Load() != 0 || pipeline.Stats().Blocked != 1 || runtime.calls != 2 {
				t.Fatal("last trial batch failure returned partial success or sent original text", err)
			}
			if len(runtime.deadlines) != 2 || !runtime.deadlines[0].Equal(runtime.deadlines[1]) {
				t.Fatal("trial did not retain the shorter caller deadline across batches")
			}
			if strings.Contains(err.Error(), "Alice") {
				t.Fatal("trial failure exposed private text")
			}
		})
	}
}

func TestExperimentalPipelineBudgetRequiresExplicitOptIn(t *testing.T) {
	for _, test := range []struct {
		name               string
		experimental       bool
		requested, maximum time.Duration
	}{
		{"default", false, 0, 15 * time.Second}, {"ordinary over limit", false, 30 * time.Second, 15 * time.Second},
		{"trial default", true, 0, 30 * time.Second}, {"trial over limit", true, 60 * time.Second, 30 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			var deadline time.Time
			pipeline := New(Options{Experimental: test.experimental, Budget: test.requested, Extractor: ExtractorFunc(func(ctx context.Context, _ Extraction) (Result, error) {
				deadline, _ = ctx.Deadline()
				return Result{Complete: true}, nil
			})})
			defer pipeline.Close()
			prepared := mustPrepare(t, pipeline, testInput("fixture"))
			prepared.Close()
			remaining := time.Until(deadline)
			if remaining <= test.maximum-5*time.Second || remaining > test.maximum {
				t.Fatal("pipeline budget ignored its explicit mode ceiling")
			}
		})
	}
}
