package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerruntime"
)

type plannedSpecification struct {
	syntheticSpecification
	plan [][]int
}

func (s plannedSpecification) Batches(context.Context, Extraction) ([][]int, error) {
	return s.plan, nil
}

func TestRuntimeBatchPlanMustCoverEveryFieldOnce(t *testing.T) {
	for _, plan := range [][][]int{nil, {}, {{}}, {{0}}, {{0, 1}, {1}}, {{0, 2}}, {{-1}, {0, 1}}, {{0, 1}, {}}} {
		runtime := &fakeRuntime{available: true}
		extractor := NewRuntimeExtractor(runtime, plannedSpecification{plan: plan})
		result, err := extractor.Extract(context.Background(), Extraction{Versions: testInput().Versions, Texts: []string{"first", "second"}})
		if err == nil || result.Complete || runtime.calls != 0 {
			t.Fatal("incomplete/duplicate plan admitted or returned a clean result")
		}
	}
}

type budgetRuntime struct {
	deadlines  []time.Time
	calls      int
	failSecond bool
}

type fingerprintRuntime struct {
	fakeRuntime
	fingerprint string
}

func (r *fingerprintRuntime) Status() routerruntime.Status {
	return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {Fingerprint: r.fingerprint}}}
}

func TestPrivacyRuntimeBindingRejectsSelectionMismatch(t *testing.T) {
	spec, err := NewPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fingerprintRuntime{fakeRuntime: fakeRuntime{available: true}, fingerprint: "other-model-template-runtime"}
	extractor := NewRuntimeExtractor(runtime, spec)
	if result, err := extractor.Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{"private"}}); err == nil || result.Complete || runtime.calls != 0 {
		t.Fatal("mismatched current model invoked recognition")
	}
	// A generic Invoke-only fake cannot silently bypass fixed fingerprint binding.
	if _, err := NewRuntimeExtractor(&fakeRuntime{available: true}, spec).Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{"private"}}); err == nil {
		t.Fatal("missing runtime identity accepted")
	}
	runtime.fingerprint = spec.RuntimeFingerprint()
	if _, err := extractor.Extract(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{"private"}}); err == nil || runtime.calls != 0 {
		t.Fatal("same model's old/missing prompt suite inherited acceptance")
	}
}

func (*budgetRuntime) Require(routerruntime.Purpose) error { return nil }
func (r *budgetRuntime) Invoke(ctx context.Context, purpose routerruntime.Purpose, raw []byte, consume func(*http.Response) error) error {
	r.calls++
	deadline, ok := ctx.Deadline()
	if !ok || purpose != routerruntime.Redaction {
		return fail("redaction_failed")
	}
	r.deadlines = append(r.deadlines, deadline)
	var request map[string]any
	if json.Unmarshal(raw, &request) != nil || request["stream"] != false || request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		return fail("redaction_failed")
	}
	if r.failSecond && r.calls == 2 {
		<-ctx.Done()
		return ctx.Err()
	}
	return consume(&http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"synthetic":true}`))})
}

func TestRuntimeBatchesShareOneDeadlineAndNeverReturnPartialSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		runtime := &budgetRuntime{failSecond: fail}
		extractor := NewRuntimeExtractor(runtime, plannedSpecification{plan: [][]int{{1}, {0}}})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		result, err := extractor.Extract(ctx, Extraction{Versions: testInput().Versions, Texts: []string{"one", "two"}})
		cancel()
		if runtime.calls != 2 || len(runtime.deadlines) != 2 || !runtime.deadlines[0].Equal(runtime.deadlines[1]) {
			t.Fatal("batch renewed budget or skipped execution")
		}
		if fail {
			if err == nil || result.Complete || len(result.Entities) != 0 {
				t.Fatal("last-batch timeout returned partial success")
			}
		} else if err != nil || !result.Complete {
			t.Fatal("complete batches rejected", err)
		}
	}
}

func TestPrivacyBindingFingerprintsAndWholeFieldLimits(t *testing.T) {
	zero := &PrivacySpecification{}
	if _, err := zero.ResolveVersions(PrivacyRoutingVersions{}); err == nil {
		t.Fatal("uninitialized specification resolved empty binding")
	}
	if _, err := zero.Build(context.Background(), Extraction{}); err == nil {
		t.Fatal("uninitialized specification built request")
	}
	newSpec := func(model, runtime string) *PrivacySpecification {
		s, err := NewPrivacySpecification(ModelBinding{ModelID: model, RuntimeID: runtime})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := newSpec("spark-x2.5-1.7B", "windows-amd64-cpu")
	again := newSpec("spark-x2.5-1.7B", "windows-amd64-cpu")
	if s.Versions() != again.Versions() || s.Fingerprints() != again.Fingerprints() {
		t.Fatal("binding unstable")
	}
	for _, changed := range []*PrivacySpecification{newSpec("spark-x2.5-4B", "windows-amd64-cpu"), newSpec("spark-x2.5-1.7B", "linux-amd64-cpu")} {
		if changed.Versions().Model == s.Versions().Model || changed.Versions().Prompt != s.Versions().Prompt || changed.Versions().Config != s.Versions().Config {
			t.Fatal("model binding polluted prompt/config or failed to isolate model")
		}
	}
	if _, err := NewPrivacySpecification(ModelBinding{ModelID: "cloud-model", RuntimeID: "windows-amd64-cpu"}); err == nil {
		t.Fatal("arbitrary cloud model bound as local")
	}
	f := s.Fingerprints()
	// Safe version metadata for the fixed-candidate evidence runner; no input
	// text, model output or private mapping is formatted here.
	fingerprintJSON, _ := json.Marshal(f)
	t.Logf("privacy fingerprints: %s", fingerprintJSON)
	if f.Prompt == f.Schema || f.Prompt == f.Config || len(f.Prompt) != 64 || len(f.Schema) != 64 || len(f.Config) != 64 || f.PromptSchema != s.Versions().Prompt || f.Source != PrivacySourceCommit {
		t.Fatal("independent material fingerprints missing")
	}
	if v, err := s.ResolveVersions(s.RoutingVersions()); err != nil || v != s.Versions() {
		t.Fatal("W4 tuple mapping changed trusted binding", err)
	}
	tuple := s.RoutingVersions()
	tuple.RedactionSchemaVersion = "judgment-schema"
	if _, err := s.ResolveVersions(tuple); err == nil {
		t.Fatal("W4 tuple accepted judgment or mismatched redaction schema")
	}
	in := Extraction{Versions: s.Versions(), Texts: []string{"first", "second"}}
	before := append([]string(nil), in.Texts...)
	plans, err := s.Batches(context.Background(), in)
	if err != nil || !reflect.DeepEqual(plans, [][]int{{0, 1}}) || !reflect.DeepEqual(before, in.Texts) {
		t.Fatal("planner mutated inputs or numbering", err)
	}
	bad := in
	bad.Versions.Prompt = "other"
	if _, err := s.Build(context.Background(), bad); err == nil {
		t.Fatal("mismatched prompt built")
	}
}
