package routerservice

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

// runtimeRedactor is the only production adapter between W4 and W5. It keeps
// the manager, the fixed privacy specification and all pipeline instances in
// one service-owned lifetime. A model selection change creates a new
// specification and clears the previous cache epoch; prepared request
// snapshots remain valid until their owner closes them.
type runtimeRedactor struct {
	mu                sync.Mutex
	local             *LocalModels
	active            *routerredact.Pipeline
	activeID          string
	activeRT          string
	activeFingerprint string
	activeSP          *routerredact.PrivacySpecification
	retired           []*routerredact.Pipeline
	closed            bool
	experimental      bool
}

func newRuntimeRedactor(local *LocalModels, experimental ...bool) *runtimeRedactor {
	return &runtimeRedactor{local: local, experimental: len(experimental) > 0 && experimental[0]}
}

func (r *runtimeRedactor) currentLocked() (*routerredact.Pipeline, *routerredact.PrivacySpecification, error) {
	if r == nil || r.local == nil || r.local.Manager == nil || r.closed {
		return nil, nil, errors.New("redaction_unavailable")
	}
	identity, settings := r.local.Manager.SnapshotBinding(context.Background())
	selected := identity.ModelID
	runtimeID := identity.RuntimeID
	if selected == "" || runtimeID == "" {
		return nil, nil, errors.New("redaction_unavailable")
	}
	if r.active != nil && r.activeSP != nil && r.activeID == selected && r.activeRT == runtimeID && r.activeFingerprint == identity.ModelFingerprint {
		return r.active, r.activeSP, nil
	}
	binding := routerredact.ModelBinding{ModelID: selected, RuntimeID: runtimeID, ContextTokens: settings.Effective().ContextTokens}
	spec, err := routerredact.NewPrivacySpecification(binding)
	if r.experimental {
		spec, err = routerredact.NewExperimentalPrivacySpecification(binding)
	}
	if err != nil {
		return nil, nil, err
	}
	if spec.RuntimeFingerprint() != identity.ModelFingerprint {
		return nil, nil, errors.New("redaction_unavailable")
	}
	extractor := routerredact.NewRuntimeExtractor(r.local.Manager, spec)
	budget := 15 * time.Second
	if r.experimental {
		invoker, trialErr := r.local.Manager.NewExperimentalRedactionInvoker(spec.RuntimeFingerprint(), spec.CapabilitySuite())
		if trialErr != nil {
			return nil, nil, errors.New("redaction_unavailable")
		}
		extractor = routerredact.NewExperimentalRuntimeExtractor(invoker, spec)
		budget = 30 * time.Second
	}
	pipeline := routerredact.New(routerredact.Options{Extractor: extractor, Budget: budget, Experimental: r.experimental})
	if r.active != nil {
		// Clear the old cache immediately. Its existing snapshots are independent
		// and are retained until the request closes them.
		r.active.Clear()
		r.retired = append(r.retired, r.active)
	}
	r.active, r.activeSP, r.activeID, r.activeRT, r.activeFingerprint = pipeline, spec, selected, runtimeID, identity.ModelFingerprint
	return pipeline, spec, nil
}

func (r *runtimeRedactor) current() (*routerredact.Pipeline, *routerredact.PrivacySpecification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentLocked()
}

func (r *runtimeRedactor) RoutingVersions() RoutingVersions {
	_, spec, err := r.current()
	if err != nil || spec == nil {
		return RoutingVersions{}
	}
	v := spec.RoutingVersions()
	return RoutingVersions{
		LocalRuntimeFingerprint:  v.LocalRuntimeFingerprint,
		LocalModelFingerprint:    v.LocalModelFingerprint,
		LocalTemplateFingerprint: v.LocalTemplateFingerprint,
		RedactionPromptVersion:   v.RedactionPromptVersion,
		RedactionSchemaVersion:   v.RedactionSchemaVersion,
		ConfigVersion:            v.ConfigVersion,
	}
}

func redactProtocol(value string) (routerredact.Protocol, error) {
	switch value {
	case "chat":
		return routerredact.Chat, nil
	case "responses":
		return routerredact.Responses, nil
	case "anthropic":
		return routerredact.Anthropic, nil
	default:
		return "", errors.New("redaction_protocol_unsupported")
	}
}

func (r *runtimeRedactor) Prepare(ctx context.Context, req RedactionRequest) (PreparedCloudRequest, error) {
	targetProtocol, err := redactProtocol(req.TargetProtocol)
	if err != nil {
		return PreparedCloudRequest{}, err
	}
	clientProtocol, err := redactProtocol(req.SourceProtocol)
	if err != nil {
		return PreparedCloudRequest{}, err
	}
	pipeline, spec, err := r.current()
	if err != nil {
		return PreparedCloudRequest{}, err
	}
	privacyVersions, err := spec.ResolveVersions(routerredact.PrivacyRoutingVersions{
		LocalRuntimeFingerprint:  req.Versions.LocalRuntimeFingerprint,
		LocalModelFingerprint:    req.Versions.LocalModelFingerprint,
		LocalTemplateFingerprint: req.Versions.LocalTemplateFingerprint,
		RedactionPromptVersion:   req.Versions.RedactionPromptVersion,
		RedactionSchemaVersion:   req.Versions.RedactionSchemaVersion,
		ConfigVersion:            req.Versions.ConfigVersion,
	})
	if err != nil {
		return PreparedCloudRequest{}, err
	}
	seedSessionID := req.SeedSessionID
	if !req.SessionVerified && req.SessionID == "" && seedSessionID == "" {
		_, _, seedSessionID = parseRedactionSessionMaterial(req.AppID, string(targetProtocol), req.Body)
	}
	prepared, err := pipeline.Prepare(ctx, routerredact.Input{
		RequestID: req.RequestID, AppID: req.AppID, Protocol: targetProtocol,
		Operation: req.Operation, SessionID: req.SessionID, SessionVerified: req.SessionVerified,
		SeedSessionID: seedSessionID,
		Versions:      privacyVersions, Body: append([]byte(nil), req.Body...),
	})
	if err != nil {
		return PreparedCloudRequest{}, err
	}
	body, err := prepared.Body()
	if err != nil {
		prepared.Close()
		return PreparedCloudRequest{}, err
	}
	return PreparedCloudRequest{
		Body: body,
		Restore: func(restoreCtx context.Context, protocol string, _ string, response []byte) ([]byte, error) {
			if err := restoreCtx.Err(); err != nil {
				return nil, err
			}
			actual, protocolErr := redactProtocol(protocol)
			if protocolErr != nil || actual != clientProtocol {
				return nil, errors.New("redaction_protocol_mismatch")
			}
			return prepared.Snapshot.RestoreResponse(actual, response)
		},
		RestoreSSE: func(restoreCtx context.Context, protocol string, input io.Reader, output io.Writer) error {
			actual, protocolErr := redactProtocol(protocol)
			if protocolErr != nil || actual != clientProtocol {
				return errors.New("redaction_protocol_mismatch")
			}
			return prepared.Snapshot.RestoreSSE(restoreCtx, actual, input, output)
		},
		Close: func() error {
			prepared.Close()
			return nil
		},
	}, nil
}

func (r *runtimeRedactor) Stats() routerredact.Stats {
	r.mu.Lock()
	pipelines := append([]*routerredact.Pipeline(nil), r.retired...)
	if r.active != nil {
		pipelines = append(pipelines, r.active)
	}
	r.mu.Unlock()
	var out routerredact.Stats
	out.Failures = map[string]uint64{}
	for _, pipeline := range pipelines {
		stats := pipeline.Stats()
		out.Processed += stats.Processed
		out.Entities += stats.Entities
		out.CacheHits += stats.CacheHits
		out.Blocked += stats.Blocked
		out.DurationMS += stats.DurationMS
		for code, count := range stats.Failures {
			out.Failures[routerredact.SafeFailureCode(code)] += count
		}
	}
	return out
}

func (r *runtimeRedactor) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	pipelines := append([]*routerredact.Pipeline(nil), r.retired...)
	if r.active != nil {
		pipelines = append(pipelines, r.active)
	}
	r.active, r.activeSP, r.retired = nil, nil, nil
	r.mu.Unlock()
	for _, pipeline := range pipelines {
		pipeline.Close()
	}
}

// redactionRuntimeState is the management projection. It contains only
// aggregate state and no model output, mapping, credentials or error text.
type redactionRuntimeState struct {
	Integrated       bool
	Capability       routerconfig.RedactionCapability
	Reason           string
	ModelReady       bool
	RecognitionReady bool
	CloudActive      bool
	Statistics       bool
	Experimental     bool
	TrialReady       bool
	BudgetMS         int64
	TrialSuite       string
	TrialConfig      string
}

func (r *runtimeRedactor) state() redactionRuntimeState {
	if r == nil || r.local == nil || r.local.Manager == nil {
		return redactionRuntimeState{Capability: routerconfig.RedactionUnavailable, Reason: "runtime_unavailable"}
	}
	status := r.local.Manager.Status()
	capability := status.Capabilities[routerruntime.Redaction]
	state := routerconfig.RedactionUnknown
	switch capability.State {
	case "available":
		state = routerconfig.RedactionAvailable
	case "unavailable":
		state = routerconfig.RedactionUnavailable
	}
	modelReady := status.Installation == "installed" && status.Dependencies.State != "missing" && status.Dependencies.State != "corrupt" && status.Dependencies.State != "failed"
	if r.experimental {
		_, spec, err := r.current()
		trialReady := false
		trialSuite, trialConfig := "", ""
		if err == nil && spec != nil && modelReady {
			invoker, err := r.local.Manager.NewExperimentalRedactionInvoker(spec.RuntimeFingerprint(), spec.CapabilitySuite())
			trialReady = err == nil && invoker.VerifyBinding(spec.RuntimeFingerprint(), spec.CapabilitySuite()) == nil
			trialSuite, trialConfig = spec.CapabilitySuite(), spec.Fingerprints().Config
		}
		// The projection identifies the selected policy even while disabled.
		// Writers independently require TrialReady before enabling new intent.
		return redactionRuntimeState{Integrated: true, Capability: routerconfig.RedactionExperimental, Reason: "experimental_quality_not_accepted", ModelReady: modelReady, RecognitionReady: false, CloudActive: trialReady, Statistics: true, Experimental: true, TrialReady: trialReady, BudgetMS: 30000, TrialSuite: trialSuite, TrialConfig: trialConfig}
	}
	// Manager.Ready binds the model/runtime fingerprint, but the recognition
	// suite is a second versioned material binding. Re-derive the current spec
	// here so management state, enablement and the production extractor agree.
	// An old suite must never be projected as currently accepted merely because
	// the selected model has not changed.
	_, currentSpec, specErr := r.current()
	formalAccepted := specErr == nil && currentSpec != nil &&
		state == routerconfig.RedactionAvailable && capability.Ready && modelReady &&
		capability.Fingerprint == currentSpec.RuntimeFingerprint() &&
		capability.Suite != "" && capability.Suite == currentSpec.CapabilitySuite()
	reason := "recognition_acceptance_pending"
	if formalAccepted {
		reason = "recognition_accepted"
	} else if state == routerconfig.RedactionAvailable {
		reason = "recognition_material_mismatch"
	}
	return redactionRuntimeState{
		Integrated: true, Capability: state, Reason: reason, ModelReady: modelReady,
		RecognitionReady: formalAccepted,
		CloudActive:      formalAccepted && status.Intent.Enabled, Statistics: true,
		BudgetMS: 15000,
	}
}
