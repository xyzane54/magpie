package routerredact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerruntime"
)

type RuntimeInvoker interface {
	Require(routerruntime.Purpose) error
	Invoke(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error
}

// Specification is supplied by trusted integration code with fixed versions.
// A concrete specification is not capability acceptance. Build/Parse must be
// bounded and respect ctx; Parse receives this call's immutable input snapshot.
type Specification interface {
	Versions() Versions
	Build(context.Context, Extraction) ([]byte, error)
	Parse(context.Context, Extraction, []byte) (Result, error)
}

// BatchSpecification plans whole input fields. The runtime independently checks
// exact coverage and maps each local result back to the original field index.
// It must not drop/split fields or retain the extraction after this call.
type BatchSpecification interface {
	Batches(context.Context, Extraction) ([][]int, error)
}
type RuntimeExtractor struct {
	runtime       RuntimeInvoker
	specification Specification
	experimental  interface{ VerifyBinding(string, string) error }
}

func NewRuntimeExtractor(runtime RuntimeInvoker, specification Specification) *RuntimeExtractor {
	if _, experimental := runtime.(*routerruntime.ExperimentalRedactionInvoker); experimental {
		return &RuntimeExtractor{}
	}
	return &RuntimeExtractor{runtime: runtime, specification: specification}
}

// NewExperimentalRuntimeExtractor requires the Manager's explicit startup-only
// trial handle. It preserves strict output validation and the real capability
// state; diagnostic wrappers cannot stand in for this handle.
func NewExperimentalRuntimeExtractor(invoker *routerruntime.ExperimentalRedactionInvoker, spec *PrivacySpecification) *RuntimeExtractor {
	if invoker == nil || spec == nil || !spec.Experimental() {
		return &RuntimeExtractor{}
	}
	return &RuntimeExtractor{runtime: invoker, specification: spec, experimental: invoker}
}

func (e *RuntimeExtractor) Available(ctx context.Context, versions Versions) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if e == nil || e.runtime == nil || e.specification == nil || e.specification.Versions() != versions {
		return fail("redaction_unavailable")
	}
	if spec, ok := e.specification.(*PrivacySpecification); ok && spec.Experimental() {
		if e.experimental == nil || e.experimental.VerifyBinding(spec.RuntimeFingerprint(), spec.CapabilitySuite()) != nil || e.runtime.Require(routerruntime.Redaction) != nil {
			return fail("redaction_unavailable")
		}
		return nil
	}
	if e.experimental != nil {
		return fail("redaction_unavailable")
	}
	if e.runtime.Require(routerruntime.Redaction) != nil {
		return fail("redaction_unavailable")
	}
	if bound, ok := e.specification.(interface{ RuntimeFingerprint() string }); ok {
		status, ok := e.runtime.(interface{ Status() routerruntime.Status })
		if !ok || bound.RuntimeFingerprint() == "" {
			return fail("redaction_unavailable")
		}
		capability := status.Status().Capabilities[routerruntime.Redaction]
		if capability.Fingerprint != bound.RuntimeFingerprint() {
			return fail("redaction_unavailable")
		}
		if suite, ok := e.specification.(interface{ CapabilitySuite() string }); ok {
			// The private diagnostic wrapper measures an unaccepted suite using
			// the owner's Generate seam. Normal extraction requires exact suite
			// evidence so changing prompt/config cannot inherit model acceptance.
			_, diagnostic := e.runtime.(interface{ privacyDiagnosticOnly() })
			if !diagnostic && (suite.CapabilitySuite() == "" || capability.Suite != suite.CapabilitySuite()) {
				return fail("redaction_unavailable")
			}
		}
	}
	return nil
}
func (e *RuntimeExtractor) Extract(parent context.Context, in Extraction) (Result, error) {
	// Also bound direct uses outside Pipeline. Each subsequent Invoke receives
	// this same deadline, so batching cannot renew the queue/execution budget.
	budget := 15 * time.Second
	if e != nil && e.experimental != nil {
		if spec, ok := e.specification.(*PrivacySpecification); ok && spec.Experimental() {
			budget = spec.Budget()
		}
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	ctx = routerruntime.WithGenerationUsageCollector(ctx, routerruntime.NewGenerationUsageCollector())
	if err := e.Available(ctx, in.Versions); err != nil {
		return Result{}, err
	}
	if len(in.Texts) > 8192 {
		return Result{}, fail("redaction_input_limit")
	}
	in.Texts = append([]string(nil), in.Texts...)
	batches := make([][]int, 1)
	for field := range in.Texts {
		batches[0] = append(batches[0], field)
	}
	if planner, ok := e.specification.(BatchSpecification); ok {
		var err error
		batches, err = planner.Batches(ctx, in)
		if err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return Result{}, ctxErr
			}
			return Result{}, fail("redaction_input_limit")
		}
	}
	if len(batches) == 0 || len(batches) > 16 {
		return Result{}, fail("redaction_specification_failed")
	}
	seen := make([]bool, len(in.Texts))
	for _, fields := range batches {
		if len(fields) == 0 && (len(in.Texts) != 0 || len(batches) != 1) {
			return Result{}, fail("redaction_specification_failed")
		}
		for _, field := range fields {
			if field < 0 || field >= len(in.Texts) || seen[field] {
				return Result{}, fail("redaction_specification_failed")
			}
			seen[field] = true
		}
	}
	for _, covered := range seen {
		if !covered {
			return Result{}, fail("redaction_specification_failed")
		}
	}
	result := Result{Complete: true}
	for _, fields := range batches {
		if err := e.Available(ctx, in.Versions); err != nil {
			return Result{}, err
		}
		batch := Extraction{Versions: in.Versions, Texts: make([]string, len(fields))}
		for local, field := range fields {
			batch.Texts[local] = in.Texts[field]
		}
		parsed, err := e.extractBatch(ctx, batch)
		if err != nil {
			return Result{}, err
		}
		entities, err := validateEntities(batch.Texts, parsed)
		if err != nil {
			return Result{}, err
		}
		for _, entity := range entities {
			entity.Field = fields[entity.Field]
			result.Entities = append(result.Entities, entity)
		}
		if len(result.Entities) > maxEntities {
			return Result{}, fail("redaction_invalid_result")
		}
	}
	if err := e.Available(ctx, in.Versions); err != nil {
		return Result{}, err
	}
	entities, err := validateEntities(in.Texts, result)
	if err != nil {
		return Result{}, err
	}
	return Result{Complete: true, Entities: entities}, nil
}

func (e *RuntimeExtractor) extractBatch(ctx context.Context, in Extraction) (Result, error) {
	body, err := e.specification.Build(ctx, in)
	if err != nil || len(body) > 8<<20 {
		return Result{}, fail("redaction_specification_failed")
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return Result{}, fail("redaction_specification_failed")
	}
	var kwargs map[string]json.RawMessage
	if raw, ok := request["chat_template_kwargs"]; ok {
		if json.Unmarshal(raw, &kwargs) != nil || kwargs == nil {
			return Result{}, fail("redaction_specification_failed")
		}
	}
	if kwargs == nil {
		kwargs = map[string]json.RawMessage{}
	}
	kwargs["enable_thinking"] = json.RawMessage("false")
	request["chat_template_kwargs"], _ = json.Marshal(kwargs)
	request["stream"] = json.RawMessage("false")
	body, _ = json.Marshal(request)
	var result Result
	consumed := 0
	validConsumption := false
	err = e.runtime.Invoke(ctx, routerruntime.Redaction, body, func(response *http.Response) error {
		consumed++
		if consumed != 1 {
			return fail("redaction_invalid_result")
		}
		if response == nil || response.Body == nil || response.StatusCode != http.StatusOK {
			return fail("redaction_extraction_failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if readErr != nil || len(raw) > 1<<20 {
			return fail("redaction_invalid_result")
		}
		if usage := reportedUsageFromBody(raw); usage != nil {
			// The Manager wraps this trusted context sink into its own diagnostic
			// recorder, so the observation receives the exact provider counts.
			routerruntime.PublishGenerationUsage(ctx, usage)
		}
		var parseErr error
		result, parseErr = e.specification.Parse(ctx, in, raw)
		if parseErr != nil {
			return parseErr
		}
		validConsumption = true
		return nil
	})
	if ctxErr := contextError(ctx); ctxErr != nil {
		return Result{}, ctxErr
	}
	if err != nil || consumed != 1 || !validConsumption {
		var failure *Failure
		if errors.As(err, &failure) {
			return Result{}, fail(SafeFailureCode(failure.Code))
		}
		return Result{}, fail("redaction_extraction_failed")
	}
	return result, nil
}

func reportedUsageFromBody(body []byte) *cloudcodec.ReportedUsage {
	var envelope struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Usage) == 0 {
		return nil
	}
	read := func(names ...string) *int64 {
		for _, name := range names {
			raw := envelope.Usage[name]
			var value int64
			if len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value >= 0 {
				return &value
			}
		}
		return nil
	}
	usage := &cloudcodec.ReportedUsage{InputTokens: read("input_tokens", "prompt_tokens"), OutputTokens: read("output_tokens", "completion_tokens"), TotalTokens: read("total_tokens")}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.TotalTokens == nil {
		return nil
	}
	return usage
}
