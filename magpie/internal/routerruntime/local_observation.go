package routerruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
)

// LocalCallPurpose identifies the one purpose of a local model call. It is
// deliberately separate from a business operation so judgment, execution and
// redaction cannot be merged into one usage row.
type LocalCallPurpose string

const (
	LocalCallJudgment  LocalCallPurpose = "judgment"
	LocalCallExecution LocalCallPurpose = "execution"
	LocalCallRedaction LocalCallPurpose = "redaction"
)

// LocalCallMode identifies the server-owned admission mode. A management
// check/diagnostic is never a business call, even when it uses the same model.
type LocalCallMode string

const (
	LocalCallFormal       LocalCallMode = "formal"
	LocalCallExperimental LocalCallMode = "experimental"
	LocalCallDiagnostic   LocalCallMode = "diagnostic"
)

type LocalCallOutcome string

const (
	LocalCallSucceeded LocalCallOutcome = "succeeded"
	LocalCallFailed    LocalCallOutcome = "failed"
	LocalCallCanceled  LocalCallOutcome = "canceled"
	LocalCallRejected  LocalCallOutcome = "rejected"
)

// LocalCallObservation is the complete safe completion record for one actual
// Manager call. It contains no prompt, response, tool argument, URL, secret,
// entity, mapping or raw error. A nil usage field/count means unknown; zero is
// retained only when the model explicitly reported zero.
//
// CallID is unique per internal call. RequestID links a business call to the
// Router request; checks and diagnostics leave RequestID empty and set Check.
// Attempted is true only after the Manager sent a request to the local model.
// QueueLoadMS is the measured queue-plus-load phase; individual queue/load
// values must not be inferred from it.
type LocalCallObservation struct {
	SettingsFingerprint string                       `json:"settings_fingerprint,omitempty"`
	Settings            routerconfig.RuntimeSettings `json:"settings"`
	ContextUsage        *ContextUsage                `json:"context_usage,omitempty"`
	HTTPStatus          int                          `json:"http_status,omitempty"`
	RequestID           string                       `json:"request_id,omitempty"`
	CallID              string                       `json:"call_id"`
	Check               bool                         `json:"check"`
	Purpose             LocalCallPurpose             `json:"purpose"`
	Mode                LocalCallMode                `json:"mode"`

	ModelID            string `json:"model_id"`
	ModelFingerprint   string `json:"model_fingerprint"`
	RuntimeID          string `json:"runtime_id"`
	RuntimeFingerprint string `json:"runtime_fingerprint"`
	MaterialVersion    string `json:"material_version,omitempty"`
	PromptVersion      string `json:"prompt_version,omitempty"`
	Suite              string `json:"suite,omitempty"`

	StartedAt time.Time        `json:"started_at"`
	EndedAt   time.Time        `json:"ended_at"`
	Outcome   LocalCallOutcome `json:"outcome"`
	ErrorCode string           `json:"error_code,omitempty"`
	Attempted bool             `json:"attempted"`

	Usage            *cloudcodec.ReportedUsage `json:"usage,omitempty"`
	QueueLoadMS      *float64                  `json:"queue_load_ms,omitempty"`
	RequestHeadersMS *float64                  `json:"request_headers_ms,omitempty"`
	ConsumeMS        *float64                  `json:"consume_ms,omitempty"`
	TotalMS          *float64                  `json:"total_ms,omitempty"`
}

// LocalCallObserver is a best-effort, non-blocking completion hook. The
// Manager invokes it after the model call and after releasing its scheduler
// state; an observer error must never change the model result. Implementations
// that persist observations should enqueue a copy and expose persistence
// failures in their own explicit health/status surface.
type LocalCallObserver interface {
	ObserveLocalCall(LocalCallObservation)
}

// LocalCallContext carries trusted Router correlation metadata. It is never
// populated from HTTP headers or model bodies.
type LocalCallContext struct {
	RequestID     string
	AppID         string
	Check         bool
	Mode          LocalCallMode
	PromptVersion string
	Material      string
}

type localCallContextKey struct{}

func WithLocalCallContext(ctx context.Context, value LocalCallContext) context.Context {
	return context.WithValue(ctx, localCallContextKey{}, value)
}

func localCallContextFrom(ctx context.Context) (LocalCallContext, bool) {
	value, ok := ctx.Value(localCallContextKey{}).(LocalCallContext)
	return value, ok
}

// LocalCallObserverFunc adapts a function to LocalCallObserver.
type LocalCallObserverFunc func(LocalCallObservation)

func (f LocalCallObserverFunc) ObserveLocalCall(v LocalCallObservation) {
	if f != nil {
		f(v)
	}
}

func validLocalCallPurpose(p LocalCallPurpose) bool {
	return p == LocalCallJudgment || p == LocalCallExecution || p == LocalCallRedaction
}

func validLocalCallMode(m LocalCallMode) bool {
	return m == LocalCallFormal || m == LocalCallExperimental || m == LocalCallDiagnostic
}

var localCallSequence atomic.Uint64
var localCallNonce = newLocalCallNonce()

func nextLocalCallID() string {
	return "runtime-local-call-" + localCallNonce + "-" + strconvFormatUint(localCallSequence.Add(1))
}

func newLocalCallNonce() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	// crypto/rand is expected on supported platforms. Keep a process-specific
	// fallback for unusual test environments without ever returning a fixed ID.
	seed := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(seed[:8])
}

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

func observationOutcome(err error) LocalCallOutcome {
	if err == nil {
		return LocalCallSucceeded
	}
	if isPurposeTimeout(err) {
		return LocalCallFailed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return LocalCallCanceled
	}
	if errors.Is(err, ErrDisabled) || errors.Is(err, ErrCapability) || errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrQueueFull) {
		return LocalCallRejected
	}
	return LocalCallFailed
}

func observationPurpose(p Purpose) LocalCallPurpose {
	switch p {
	case Judgment:
		return LocalCallJudgment
	case Execution:
		return LocalCallExecution
	default:
		return LocalCallRedaction
	}
}

func observationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []error{ErrJudgmentQueueTimeout, ErrJudgmentInferenceTimeout, ErrExecutionQueueTimeout, ErrExecutionTimeout, ErrContextExceeded, ErrJudgmentInputExceeded, ErrTokenCountUnavailable, ErrSettingsChanged} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "budget_exhausted"
	}
	for _, known := range []error{ErrDisabled, ErrCapability, ErrNotInstalled, ErrQueueFull, ErrFault, ErrBusy} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	code := strings.TrimSpace(err.Error())
	for _, allowed := range []string{"local_generation_http_error", "local_generation_failed", "local_protocol_conversion_unavailable", "local_protocol_stream_conversion_unavailable", "local_response_event_too_large"} {
		if code == allowed {
			return code
		}
	}
	return "local_generation_failed"
}

func observationPhase(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func observationMaterialVersion(p Purpose, material string) string {
	if material != "" && len(material) <= 128 && !strings.Contains(material, ":") {
		return material
	}
	switch p {
	case Judgment:
		return "w4-judgment-contract-v1"
	case Execution:
		return "w4-execution-tool-contract-v1"
	default:
		return "w5-redaction-v1"
	}
}

// LocalCallIdentity is derived only from the Manager's fixed catalog. It is
// useful to service adapters that need to attach safe identity to a completion.
type LocalCallIdentity struct {
	ModelID            string
	ModelFingerprint   string
	RuntimeID          string
	RuntimeFingerprint string
}

func RuntimeFingerprint(v routermanifest.Runtime) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func isPurposeTimeout(err error) bool {
	for _, known := range []error{ErrJudgmentQueueTimeout, ErrJudgmentInferenceTimeout, ErrExecutionQueueTimeout, ErrExecutionTimeout} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}
