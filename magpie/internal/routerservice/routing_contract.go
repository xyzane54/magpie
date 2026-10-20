package routerservice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

// Decision is the only routing result accepted by W4. A judge cannot select a
// provider or model; those remain pinned by the application's Profile.
type Decision string

const (
	DecisionLocal Decision = "local"
	DecisionCloud Decision = "cloud"
)

var (
	ErrInvalidDecision           = errors.New("invalid routing decision")
	ErrJudgmentFailed            = errors.New("routing judgment failed")
	ErrRedactionUnavailable      = errors.New("required redaction is unavailable")
	ErrJudgmentSummaryIncomplete = errors.New("judgment summary cannot retain all routing restrictions")
	ErrCloudReserveReached       = errors.New("local work stopped to preserve cloud fallback budget")
)

// RoutingVersions identifies the independently versioned local and privacy
// inputs. A cloud model name is deliberately not a substitute for any of
// these fingerprints.
type RoutingVersions struct {
	LocalRuntimeFingerprint  string
	LocalModelFingerprint    string
	LocalTemplateFingerprint string
	RedactionPromptVersion   string
	RedactionSchemaVersion   string
	ConfigVersion            string
}

// SessionInfo is populated only by a trusted, verified session adapter. A
// profile ID or an unverified client field must never set Verified.
type SessionInfo struct {
	ID       string
	Verified bool
}

// Judge receives the compact, protocol-neutral view. It must return exactly
// local or cloud and must not receive credentials, URLs or the raw body.
type Judge interface {
	Judge(context.Context, routerrequest.JudgmentView) (Decision, error)
}

// LocalInvoker is satisfied by routerruntime.Manager and by deterministic
// protocol fixtures. Invoke must consume the upstream body inside consume.
type LocalInvoker interface {
	Invoke(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error
}

// RedactionRequest is the W5 boundary. Body is the final target-protocol bytes
// after conversion and provider option expansion. Implementations return a
// private processed body and response restoration callbacks or an explicit
// error. It is never a client supplied "already redacted" credential.
type RedactionRequest struct {
	RequestID string
	AppID     string
	// SourceProtocol identifies the client request. TargetProtocol and Body
	// identify the exact protocol bytes that will be sent upstream.
	SourceProtocol  string
	TargetProtocol  string
	Operation       string
	SessionID       string
	SeedSessionID   string
	SessionVerified bool
	Versions        RoutingVersions
	Body            []byte
	Context         context.Context
}

// PreparedCloudRequest owns private data until Close. Restore receives one
// complete non-stream response. RestoreSSE consumes one continuous source
// stream and writes the restored client stream; it keeps cross-event state in
// the W5 implementation. W4 owns the reader, closes it after the executor
// returns, waits for the restore call to finish, and propagates its error.
// Close is idempotent and is called on success, error, cancellation and
// fallback. Close and Release are mutually exclusive: Release remains as a
// source-compatible migration alias for older adapters; new adapters must
// implement Close and leave Release nil.
type PreparedCloudRequest struct {
	Body       []byte
	Restore    func(context.Context, string, string, []byte) ([]byte, error)
	RestoreSSE func(context.Context, string, io.Reader, io.Writer) error
	Close      func() error
	Release    func()
}

type Redactor interface {
	Prepare(context.Context, RedactionRequest) (PreparedCloudRequest, error)
}

// RoutingOptions is injected by the service owner. W5 can supply Judge and
// Redactor later without changing protocol adapters. A nil value keeps the
// existing cloud-only behavior.
type RoutingOptions struct {
	Judge         Judge
	LocalInvoker  LocalInvoker
	Redactor      Redactor
	PromptVersion string
	Versions      RoutingVersions
}

// RuntimeJudge is the default judge adapter for a versioned Manager capability.
// It uses an OpenAI-compatible local response but consumes the body entirely
// inside Manager.Invoke; no response body escapes into the public Router.
type RuntimeJudge struct {
	Manager       *routerruntime.Manager
	PromptVersion string
}

func (j RuntimeJudge) Judge(ctx context.Context, view routerrequest.JudgmentView) (Decision, error) {
	if j.Manager == nil {
		return "", ErrJudgmentFailed
	}
	identity, settings := j.Manager.SnapshotBinding(ctx)
	ctx = routerruntime.WithRuntimeSettings(ctx, settings)
	if meta, ok := localCallContextFrom(ctx); ok {
		meta.Binding, meta.BindingCaptured, meta.Settings = identity, true, settings
		ctx = withLocalCallContext(ctx, meta)
	}
	compact := settings != (routerconfig.RuntimeSettings{})
	body, err := marshalJudgment(view, j.PromptVersion, compact)
	if err != nil {
		return "", err
	}
	var answer []byte
	var usage *cloudcodec.ReportedUsage
	started := time.Now()
	diagnostics := &routerruntime.GenerationDiagnostics{}
	callCtx := routerruntime.WithGenerationDiagnostics(ctx, diagnostics)
	if _, ok := localCallContextFrom(callCtx); !ok {
		// A trusted caller without an observer still gets the same runtime path.
		callCtx = ctx
	}
	err = j.Manager.Invoke(callCtx, routerruntime.Judgment, body, func(resp *http.Response) error {
		answer, err = readLocalResponse(resp, 64<<10)
		if err == nil {
			usage = reportedUsageFromBody(answer)
			diagnostics.SetReportedUsage(usage)
		}
		return err
	})
	if err == nil {
		var decision Decision
		if compact {
			decision, err = parseStrictDecision(answer)
		} else {
			decision, err = parseDecision(answer)
		}
		if err == nil {
			if meta, ok := localCallContextFrom(ctx); ok {
				emitObservedLocalCall(meta, j.Manager, routerruntime.Judgment, started, diagnostics.Snapshot(), usage, nil)
			}
			return decision, nil
		}
	}
	if meta, ok := localCallContextFrom(ctx); ok {
		emitObservedLocalCall(meta, j.Manager, routerruntime.Judgment, started, diagnostics.Snapshot(), usage, err)
	}
	return "", err
}

// localResponseLimit and helpers live in routing.go to keep this contract file
// free of protocol parsing policy; these declarations make time an explicit
// part of the shared acceptance record.
type routingTrace struct {
	Decision         Decision
	JudgmentDuration time.Duration
	CapabilityState  string
}
