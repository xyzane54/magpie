package routerservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerruntime"
)

type localCallContextKey struct{}

type localCallContext struct {
	RequestID       string
	AppID           string
	Observer        routerruntime.LocalCallObserver
	Mode            routerruntime.LocalCallMode
	Check           bool
	PromptVersion   string
	Material        string
	Suite           string
	Settings        routerconfig.RuntimeSettings
	Binding         routerruntime.LocalCallIdentity
	BindingCaptured bool
}

type redactionOnlyObserver struct {
	target routerruntime.LocalCallObserver
}

func (o redactionOnlyObserver) ObserveLocalCall(value routerruntime.LocalCallObservation) {
	if o.target != nil && value.Purpose == routerruntime.LocalCallRedaction {
		o.target.ObserveLocalCall(value)
	}
}

const (
	localJudgmentMaterial  = "w4-judgment-contract-v1"
	localExecutionMaterial = "w4-execution-tool-contract-v1"
)

func withLocalCallContext(ctx context.Context, value localCallContext) context.Context {
	ctx = routerruntime.WithLocalCallContext(ctx, routerruntime.LocalCallContext{RequestID: value.RequestID, AppID: value.AppID, Check: value.Check, Mode: value.Mode, PromptVersion: value.PromptVersion, Material: value.Material})
	return context.WithValue(ctx, localCallContextKey{}, value)
}

func localCallContextFrom(ctx context.Context) (localCallContext, bool) {
	value, ok := ctx.Value(localCallContextKey{}).(localCallContext)
	return value, ok && value.Observer != nil
}

var localCallSequence atomic.Uint64
var localCallNonce = newServiceLocalCallNonce()

func nextLocalCallID() string {
	return "router-local-call-" + localCallNonce + "-" + strconvFormatUint(localCallSequence.Add(1))
}

func newServiceLocalCallNonce() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	seed := sha256.Sum256([]byte(strconvFormatUint(uint64(time.Now().UnixNano())) + "-" + strconvFormatUint(uint64(os.Getpid()))))
	return hex.EncodeToString(seed[:8])
}

// Kept local to avoid adding formatting work to every model call.
func strconvFormatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}

func localPurpose(p routerruntime.Purpose) routerruntime.LocalCallPurpose {
	switch p {
	case routerruntime.Judgment:
		return routerruntime.LocalCallJudgment
	case routerruntime.Execution:
		return routerruntime.LocalCallExecution
	default:
		return routerruntime.LocalCallRedaction
	}
}

func localOutcome(err error) routerruntime.LocalCallOutcome {
	if err == nil {
		return routerruntime.LocalCallSucceeded
	}
	for _, stage := range []error{routerruntime.ErrJudgmentQueueTimeout, routerruntime.ErrJudgmentInferenceTimeout, routerruntime.ErrExecutionQueueTimeout, routerruntime.ErrExecutionTimeout} {
		if errors.Is(err, stage) {
			return routerruntime.LocalCallFailed
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return routerruntime.LocalCallCanceled
	}
	if errors.Is(err, routerruntime.ErrDisabled) || errors.Is(err, routerruntime.ErrCapability) || errors.Is(err, routerruntime.ErrNotInstalled) || errors.Is(err, routerruntime.ErrQueueFull) {
		return routerruntime.LocalCallRejected
	}
	return routerruntime.LocalCallFailed
}

func localErrorCode(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []struct {
		err  error
		code string
	}{
		{routerruntime.ErrJudgmentQueueTimeout, "judgment_queue_timeout"},
		{routerruntime.ErrJudgmentInferenceTimeout, "judgment_inference_timeout"},
		{routerruntime.ErrExecutionQueueTimeout, "execution_queue_timeout"},
		{routerruntime.ErrExecutionTimeout, "execution_timeout"},
		{routerruntime.ErrContextExceeded, "context_limit_exceeded"},
		{routerruntime.ErrJudgmentInputExceeded, "judgment_input_exceeded"},
		{routerruntime.ErrTokenCountUnavailable, "token_count_unavailable"},
		{routerruntime.ErrSettingsChanged, "local_runtime_settings_changed"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	if errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "budget_exhausted"
	}
	for _, known := range []error{routerruntime.ErrDisabled, routerruntime.ErrCapability, routerruntime.ErrNotInstalled, routerruntime.ErrQueueFull, routerruntime.ErrFault, routerruntime.ErrBusy} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	code := strings.TrimSpace(err.Error())
	for _, allowed := range []string{
		"local_generation_http_error", "local_generation_failed", "local response unavailable", "local generation failed",
		"local response too large", "local_protocol_conversion_unavailable", "local_protocol_stream_conversion_unavailable",
		"local_response_event_too_large", "invalid local response", "invalid local tool arguments",
	} {
		if code == allowed {
			return strings.ReplaceAll(code, " ", "_")
		}
	}
	return "local_generation_failed"
}

func phase(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func emitObservedLocalCall(meta localCallContext, manager *routerruntime.Manager, purpose routerruntime.Purpose, started time.Time, diagnostics routerruntime.GenerationDiagnosticSnapshot, usageReported *cloudcodec.ReportedUsage, err error) {
	if meta.Observer == nil || manager == nil {
		return
	}
	identity := meta.Binding
	if !meta.BindingCaptured {
		identity, _ = manager.SnapshotBinding(routerruntime.WithRuntimeSettings(context.Background(), meta.Settings))
	}
	ended := time.Now().UTC()
	event := routerruntime.LocalCallObservation{
		Settings: meta.Settings, SettingsFingerprint: meta.Settings.Fingerprint(), HTTPStatus: diagnostics.HTTPStatus,
		RequestID: meta.RequestID, CallID: nextLocalCallID(), Check: meta.Check,
		Purpose: localPurpose(purpose), Mode: meta.Mode,
		ModelID: identity.ModelID, ModelFingerprint: identity.ModelFingerprint,
		RuntimeID: identity.RuntimeID, RuntimeFingerprint: identity.RuntimeFingerprint,
		MaterialVersion: localMaterialVersion(purpose, meta.Material), PromptVersion: meta.PromptVersion, Suite: localSuite(meta.Suite, meta.Material),
		StartedAt: started.UTC(), EndedAt: ended, Outcome: localOutcome(err), ErrorCode: localErrorCode(err), Attempted: diagnostics.Attempted,
		Usage: usageReported, QueueLoadMS: phase(diagnostics.QueueLoadMS), RequestHeadersMS: phase(diagnostics.RequestHeadersMS), ConsumeMS: phase(diagnostics.ConsumeMS), TotalMS: phase(diagnostics.TotalMS),
	}
	if diagnostics.InputTokens != nil && diagnostics.OutputReserveTokens != nil {
		event.ContextUsage = &routerruntime.ContextUsage{InputTokens: *diagnostics.InputTokens, OutputTokens: *diagnostics.OutputReserveTokens, ContextTokens: diagnostics.ContextTokens}
	}
	meta.Observer.ObserveLocalCall(event)
}

func localMaterialVersion(purpose routerruntime.Purpose, material string) string {
	if material != "" && len(material) <= 128 && !strings.Contains(material, ":") {
		return material
	}
	switch purpose {
	case routerruntime.Judgment:
		return localJudgmentMaterial
	case routerruntime.Execution:
		return localExecutionMaterial
	default:
		return "w5-redaction-v1"
	}
}

func localSuite(suite, material string) string {
	if suite != "" {
		return suite
	}
	return material
}

// localCallMode reflects the admission path that actually authorized the
// purpose. Trial authorization is independent from formal quality evidence;
// a trial call must therefore remain experimental in the ledger.
func localCallMode(manager *routerruntime.Manager, purpose routerruntime.Purpose) routerruntime.LocalCallMode {
	if manager != nil {
		capability := manager.Status().Capabilities[purpose]
		if capability.TrialEnabled {
			return routerruntime.LocalCallExperimental
		}
		if capability.Ready {
			return routerruntime.LocalCallFormal
		}
	}
	return routerruntime.LocalCallFormal
}
