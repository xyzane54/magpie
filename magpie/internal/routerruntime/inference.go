package routerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerprocess"
)

// GenerationHTTPError preserves an actual upstream status without exposing an
// upstream body through ordinary error formatting or serialization.
type GenerationHTTPError struct {
	HTTPStatus      int `json:"http_status"`
	body            []byte
	contextExceeded bool
}

func (e GenerationHTTPError) Is(target error) bool {
	return target == ErrContextExceeded && e.contextExceeded
}

func (e GenerationHTTPError) Error() string    { return "local_generation_http_error" }
func (e GenerationHTTPError) String() string   { return e.Error() }
func (e GenerationHTTPError) GoString() string { return e.Error() }
func (e GenerationHTTPError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Code       string `json:"code"`
		HTTPStatus int    `json:"http_status"`
	}{e.Error(), e.HTTPStatus})
}

// DiagnosticBody returns a detached, at most 4096-byte upstream error body.
// Bodies are retained only for a trusted WithGenerationDiagnostics call. Its
// caller must keep synthetic raw evidence separate from logs and statistics.
func (e GenerationHTTPError) DiagnosticBody() []byte { return bytes.Clone(e.body) }

// GenerationDiagnosticSnapshot contains measured transport phases, never request
// text, response content, credentials or an endpoint. QueueLoadMS combines cold
// loading and scheduler waiting; those phases are not measured separately here.
type GenerationDiagnosticSnapshot struct {
	InputTokens         *int                      `json:"input_tokens,omitempty"`
	OutputReserveTokens *int                      `json:"output_reserve_tokens,omitempty"`
	ContextTokens       int                       `json:"context_tokens,omitempty"`
	Attempted           bool                      `json:"attempted"`
	HTTPStatus          int                       `json:"http_status"`
	QueueLoadMS         float64                   `json:"queue_load_ms"`
	RequestHeadersMS    float64                   `json:"request_headers_ms"`
	ConsumeMS           float64                   `json:"consume_ms"`
	TotalMS             float64                   `json:"total_ms"`
	ReportedUsage       *cloudcodec.ReportedUsage `json:"-"`
}

// GenerationDiagnostics records one Generate/Invoke call. Use a fresh recorder
// for each call and read Snapshot after it returns. Snapshot is safe to inspect
// concurrently, but only the completed call publishes its measurements.
type GenerationDiagnostics struct {
	mu       sync.Mutex
	snapshot GenerationDiagnosticSnapshot
}

func (d *GenerationDiagnostics) Snapshot() GenerationDiagnosticSnapshot {
	if d == nil {
		return GenerationDiagnosticSnapshot{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot
}

// SetReportedUsage lets a protocol consumer publish counts it actually
// parsed while preserving nil-versus-zero semantics. It never derives totals.
func (d *GenerationDiagnostics) SetReportedUsage(usage *cloudcodec.ReportedUsage) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.snapshot.ReportedUsage = usage
	d.mu.Unlock()
}

type generationDiagnosticsKey struct{}

type generationUsageSink func(*cloudcodec.ReportedUsage)

type generationUsageSinkKey struct{}

// GenerationUsageCollector is a mutable in-process bridge for response
// consumers whose callback retains the caller's original context. Manager
// installs the completed-call recorder into it before invoking the consumer.
type GenerationUsageCollector struct {
	mu   sync.Mutex
	sink func(*cloudcodec.ReportedUsage)
}

func NewGenerationUsageCollector() *GenerationUsageCollector { return &GenerationUsageCollector{} }

func WithGenerationUsageCollector(ctx context.Context, collector *GenerationUsageCollector) context.Context {
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, generationUsageCollectorKey{}, collector)
}

type generationUsageCollectorKey struct{}

func generationUsageCollectorFrom(ctx context.Context) *GenerationUsageCollector {
	collector, _ := ctx.Value(generationUsageCollectorKey{}).(*GenerationUsageCollector)
	return collector
}

func (c *GenerationUsageCollector) setSink(sink func(*cloudcodec.ReportedUsage)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.sink = sink
	c.mu.Unlock()
}

func (c *GenerationUsageCollector) publish(usage *cloudcodec.ReportedUsage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		sink(usage)
	}
}

// WithGenerationDiagnostics is an in-process diagnostic seam. It changes no
// capability decision, admission rule, request body, timeout or HTTP surface.
func WithGenerationDiagnostics(ctx context.Context, d *GenerationDiagnostics) context.Context {
	return context.WithValue(ctx, generationDiagnosticsKey{}, d)
}

// WithGenerationUsageSink lets the single response consumer publish the
// provider-reported token counts to the Manager diagnostic record. Nil fields
// remain unknown; this hook never derives counts.
func WithGenerationUsageSink(ctx context.Context, sink func(*cloudcodec.ReportedUsage)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, generationUsageSinkKey{}, generationUsageSink(sink))
}

func generationUsageSinkFrom(ctx context.Context) generationUsageSink {
	sink, _ := ctx.Value(generationUsageSinkKey{}).(generationUsageSink)
	return sink
}

// PublishGenerationUsage forwards one provider-reported usage value to the
// current Manager diagnostic recorder. Consumers call this after parsing the
// response they already own.
func PublishGenerationUsage(ctx context.Context, usage *cloudcodec.ReportedUsage) {
	if sink := generationUsageSinkFrom(ctx); sink != nil {
		sink(usage)
	}
	if collector := generationUsageCollectorFrom(ctx); collector != nil {
		collector.publish(usage)
	}
}

func elapsedMS(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}

// Invoke is the W4/W5 entry point: the purpose must have independent acceptance
// evidence for this exact model/runtime before any request can start it.
func (m *Manager) Invoke(ctx context.Context, p Purpose, body []byte, consume func(*http.Response) error) error {
	if err := m.Require(p); err != nil {
		return err
	}
	return m.generate(ctx, p, body, consume, func() error { return m.Require(p) })
}

// Generate is the diagnostic/acceptance transport seam. consume must read over the
// response within this call; returning releases the execution slot and cancels
// the upstream body. It preserves messages/tools and execution thinking flags.
// Judgment/redaction callers own their schemas and versioned acceptance suites.
func (m *Manager) Generate(ctx context.Context, p Purpose, body []byte, consume func(*http.Response) error) error {
	return m.generate(ctx, p, body, consume, nil)
}
func (m *Manager) generate(ctx context.Context, p Purpose, body []byte, consume func(*http.Response) error, admission func() error) (err error) {
	identity, settings := m.SnapshotBinding(ctx)
	diagnostics, _ := ctx.Value(generationDiagnosticsKey{}).(*GenerationDiagnostics)
	usageSink := generationUsageSinkFrom(ctx)
	usageCollector := generationUsageCollectorFrom(ctx)
	m.mu.Lock()
	observer := m.observer
	m.mu.Unlock()
	if (observer != nil || usageCollector != nil) && diagnostics == nil {
		diagnostics = &GenerationDiagnostics{}
	}
	callCtx := WithRuntimeSettings(ctx, settings)
	if diagnostics != nil {
		callCtx = WithGenerationDiagnostics(callCtx, diagnostics)
		if usageSink == nil {
			usageSink = func(usage *cloudcodec.ReportedUsage) { diagnostics.SetReportedUsage(usage) }
		} else {
			prior := usageSink
			usageSink = func(usage *cloudcodec.ReportedUsage) { prior(usage); diagnostics.SetReportedUsage(usage) }
		}
		callCtx = WithGenerationUsageSink(callCtx, usageSink)
		if usageCollector != nil {
			usageCollector.setSink(usageSink)
		}
	}
	var measured GenerationDiagnosticSnapshot
	started := time.Now()
	defer func() {
		if diagnostics != nil {
			var statusErr *GenerationHTTPError
			if errors.As(err, &statusErr) && measured.HTTPStatus == 0 {
				measured.HTTPStatus = statusErr.HTTPStatus
			}
			measured.TotalMS = elapsedMS(started)
			diagnostics.mu.Lock()
			reportedUsage := diagnostics.snapshot.ReportedUsage
			diagnostics.snapshot = measured
			diagnostics.snapshot.ReportedUsage = reportedUsage
			diagnostics.mu.Unlock()
		}
		if observer != nil {
			meta, ok := localCallContextFrom(ctx)
			if !ok {
				meta.Mode = LocalCallFormal
			}
			snapshot := GenerationDiagnosticSnapshot{}
			if diagnostics != nil {
				snapshot = diagnostics.Snapshot()
			}
			material := observationMaterialVersion(p, meta.Material)
			var contextUsage *ContextUsage
			if snapshot.InputTokens != nil && snapshot.OutputReserveTokens != nil {
				contextUsage = &ContextUsage{InputTokens: *snapshot.InputTokens, OutputTokens: *snapshot.OutputReserveTokens, ContextTokens: snapshot.ContextTokens}
			}
			observer.ObserveLocalCall(LocalCallObservation{
				Settings: settings, SettingsFingerprint: settings.Fingerprint(), ContextUsage: contextUsage, HTTPStatus: snapshot.HTTPStatus,
				RequestID: meta.RequestID, CallID: nextLocalCallID(), Check: meta.Check,
				Purpose: observationPurpose(p), Mode: meta.Mode,
				ModelID: identity.ModelID, ModelFingerprint: identity.ModelFingerprint,
				RuntimeID: identity.RuntimeID, RuntimeFingerprint: identity.RuntimeFingerprint,
				MaterialVersion: material, PromptVersion: meta.PromptVersion, Suite: meta.Material,
				StartedAt: started.UTC(), EndedAt: time.Now().UTC(), Outcome: observationOutcome(err), ErrorCode: observationErrorCode(err), Attempted: snapshot.Attempted,
				Usage: snapshot.ReportedUsage, QueueLoadMS: observationPhase(snapshot.QueueLoadMS), RequestHeadersMS: observationPhase(snapshot.RequestHeadersMS), ConsumeMS: observationPhase(snapshot.ConsumeMS), TotalMS: observationPhase(snapshot.TotalMS),
			})
		}
	}()
	if len(body) > 16<<20 || consume == nil {
		return errors.New("invalid local generation request")
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return errors.New("invalid local generation JSON")
	}
	if p == Judgment || p == Redaction {
		var kwargs map[string]json.RawMessage
		if raw, ok := request["chat_template_kwargs"]; ok {
			if err := json.Unmarshal(raw, &kwargs); err != nil || kwargs == nil {
				return errors.New("invalid template arguments")
			}
		} else {
			kwargs = make(map[string]json.RawMessage)
		}
		kwargs["enable_thinking"] = json.RawMessage("false")
		request["chat_template_kwargs"], _ = json.Marshal(kwargs)
	}
	// This flag is documented in the pinned server version. Cache correctness
	// remains server-owned; no latency/benefit claim follows from enabling it.
	request["cache_prompt"] = json.RawMessage("true")
	body, err = json.Marshal(request)
	if err != nil {
		return err
	}
	var outputReserve int
	if !settings.IsZero() {
		outputReserve, err = requestOutputReserve(request)
		if err != nil {
			return err
		}
	}
	var admissionStarted time.Time
	admitted := false
	if diagnostics != nil {
		admissionStarted = time.Now()
	}
	err = m.Use(callCtx, p, func(call context.Context, endpoint string) error {
		admitted = true
		if diagnostics != nil {
			measured.QueueLoadMS = elapsedMS(admissionStarted)
		}
		if admission != nil {
			if err := admission(); err != nil {
				return err
			}
		}
		if !settings.IsZero() {
			usage, err := m.countContext(call, endpoint, body, outputReserve)
			if err != nil {
				return err
			}
			publishContextUsage(ctx, usage)
			measured.InputTokens = &usage.InputTokens
			measured.OutputReserveTokens = &usage.OutputTokens
			measured.ContextTokens = usage.ContextTokens
			if outputReserve == 0 && usage.OutputTokens < 32 {
				return ErrContextExceeded
			}
			if p == Judgment && usage.InputTokens > JudgmentInputLimit {
				return ErrJudgmentInputExceeded
			}
			if err := checkContextUsage(usage); err != nil {
				return err
			}
			if outputReserve == 0 {
				request["max_tokens"], _ = json.Marshal(usage.OutputTokens)
				body, err = json.Marshal(request)
				if err != nil {
					return err
				}
			}
		}
		req, err := http.NewRequestWithContext(call, http.MethodPost, endpoint+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		client := routerprocess.NewHTTPClient()
		defer client.CloseIdleConnections()
		var requestStarted time.Time
		if diagnostics != nil {
			requestStarted = time.Now()
		}
		if diagnostics != nil {
			measured.Attempted = true
		}
		response, err := client.Do(req)
		if diagnostics != nil {
			measured.RequestHeadersMS = elapsedMS(requestStarted)
		}
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if diagnostics != nil {
			measured.HTTPStatus = response.StatusCode
			consumeStarted := time.Now()
			defer func() { measured.ConsumeMS = elapsedMS(consumeStarted) }()
		}
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return newGenerationHTTPError(response.StatusCode, data, diagnostics != nil)
		}
		return consume(response)
	})
	if diagnostics != nil && !admitted {
		measured.QueueLoadMS = elapsedMS(admissionStarted)
	}
	return err
}

func (m *Manager) Health(ctx context.Context) error {
	m.mu.Lock()
	p := m.proc
	m.mu.Unlock()
	if p == nil {
		return ErrNotInstalled
	}
	err := p.Health(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc == p {
		m.healthy = err == nil
		if err != nil {
			m.state, m.lastError = Fault, "health_failed"
		} else if !m.active && !m.stopping {
			m.state = Ready
			m.lastError = ""
		}
		m.notifyLocked()
	}
	return err
}
