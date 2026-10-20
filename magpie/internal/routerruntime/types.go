// Package routerruntime owns the explicitly enabled, single local model runtime.
// It implements no routing classifier or sensitive-entity extraction algorithm.
package routerruntime

import (
	"context"
	"errors"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdevice"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
)

var (
	ErrDisabled                 = errors.New("local_model_disabled")
	ErrNotInstalled             = errors.New("local_model_not_installed")
	ErrQueueFull                = errors.New("local_model_queue_full")
	ErrFault                    = errors.New("local_model_process_fault")
	ErrBusy                     = errors.New("local_model_busy")
	ErrCapability               = errors.New("local_model_capability_unavailable")
	ErrJudgmentQueueTimeout     = errors.New("local_judgment_queue_timeout")
	ErrJudgmentInferenceTimeout = errors.New("local_judgment_inference_timeout")
	ErrExecutionQueueTimeout    = errors.New("local_execution_queue_timeout")
	ErrExecutionTimeout         = errors.New("local_execution_timeout")
	ErrContextExceeded          = errors.New("local_context_exceeded")
	ErrJudgmentInputExceeded    = errors.New("local_judgment_input_exceeded")
	ErrTokenCountUnavailable    = errors.New("local_token_count_unavailable")
	ErrSettingsChanged          = errors.New("local_runtime_settings_changed")
)

type Purpose string

const (
	Judgment  Purpose = "judgment"
	Execution Purpose = "execution"
	Redaction Purpose = "redaction"
)

type RunState string

const (
	Unloaded  RunState = "unloaded"
	Loading   RunState = "loading"
	Ready     RunState = "ready"
	Busy      RunState = "busy"
	Idle      RunState = "idle"
	Unloading RunState = "unloading"
	Fault     RunState = "fault"
)

type Intent struct {
	Enabled  bool                         `json:"enabled"`
	Selected string                       `json:"selected"`
	Trial    TrialIntent                  `json:"trial,omitempty"`
	Settings routerconfig.RuntimeSettings `json:"settings,omitzero"`
}

// TrialIntent is deliberately comparable so lifecycle tests can compare the
// persisted runtime intent without hidden map state.
type TrialIntent struct {
	Judgment  bool `json:"judgment,omitempty"`
	Execution bool `json:"execution,omitempty"`
}
type IntentStore interface {
	Load(context.Context) (Intent, error)
	Save(context.Context, Intent) error
}
type Progress struct {
	Artifact   string `json:"artifact"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Stage      string `json:"stage,omitempty"`
	Step       int    `json:"step,omitempty"`
	TotalSteps int    `json:"totalSteps,omitempty"`
	Host       string `json:"host,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Installer verifies physical files; persisted status is never installation proof.
type Installer interface {
	Verify(context.Context, routermanifest.Model, routermanifest.Runtime) (routerprocess.Spec, error)
	Install(context.Context, routermanifest.Model, routermanifest.Runtime, func(Progress)) (routerprocess.Spec, error)
}

type Capability struct {
	Ready         bool      `json:"ready"` // Formal effective gate, separate from sample result.
	Trial         bool      `json:"trial,omitempty"`
	TrialEnabled  bool      `json:"trialEnabled,omitempty"`
	TrialReady    bool      `json:"trialReady,omitempty"`
	TrialEvidence bool      `json:"trialEvidence,omitempty"`
	State         string    `json:"state"` // unknown, available, unavailable
	Fingerprint   string    `json:"fingerprint"`
	Suite         string    `json:"suite,omitempty"`
	Evidence      string    `json:"evidence,omitempty"`
	CheckedAt     time.Time `json:"checkedAt,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

// Check is supplied by a versioned W4/W5 acceptance suite, never by HTTP health.
// A successful check requires a nonempty suite identity and sample evidence.
type Check struct {
	Purpose Purpose
	Suite   string
	// Trial marks a server-owned engineering check whose evidence may authorize
	// the explicitly enabled experimental local route, while formal quality
	// remains unknown.
	Trial bool
	Run   func(context.Context, string) (evidence string, err error)
}

type Status struct {
	MemoryBeforeLoad  routerdevice.Fact[uint64]    `json:"memoryBeforeLoad"`
	MemoryAfterLoad   routerdevice.Fact[uint64]    `json:"memoryAfterLoad"`
	Settings          routerconfig.RuntimeSettings `json:"settings"`
	EffectiveSettings routerconfig.RuntimeSettings `json:"effectiveSettings"`
	Intent            Intent                       `json:"intent"`
	Installation      string                       `json:"installation"`
	Runtime           RunState                     `json:"runtime"`
	Healthy           bool                         `json:"healthy"`
	PID               int                          `json:"pid,omitempty"`
	Waiting           int                          `json:"waiting"`
	Active            int                          `json:"active"`
	QueueLimit        int                          `json:"queueLimit"`
	ExecutionSlots    int                          `json:"executionSlots"`
	Progress          Progress                     `json:"progress"`
	Error             string                       `json:"error,omitempty"`
	Capabilities      map[Purpose]Capability       `json:"capabilities"`
	LastUsed          time.Time                    `json:"lastUsed,omitempty"`
	Dependencies      DependencyStatus             `json:"dependencies"`
}

type DependencyStatus struct {
	State    string `json:"state"`
	ID       string `json:"id,omitempty"`
	Version  string `json:"version,omitempty"`
	Error    string `json:"error,omitempty"`
	Recovery string `json:"recovery,omitempty"`
}

type Options struct {
	AvailableMemory      func(context.Context) (uint64, error)
	Catalog              routermanifest.Catalog
	RuntimeID            string
	Store                IntentStore
	Installer            Installer
	Factory              routerprocess.Factory
	LoadTimeout          time.Duration
	IdleTimeout          time.Duration
	JudgmentBudget       time.Duration
	ExecutionQueueBudget time.Duration
	ExecutionBudget      time.Duration
	RedactionBudget      time.Duration
	// ExperimentalRedaction permits only an explicitly bound fixed-model trial handle.
	// It never changes capability evidence and has no management HTTP setter.
	ExperimentalRedaction bool
	Now                   func() time.Time
	// After replaces timer waits in tests. Cancellation always remains live.
	After         func(time.Duration) <-chan time.Time
	LocalObserver LocalCallObserver
}
