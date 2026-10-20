// Package routerobserve projects cloud execution into the API-router ledger.
// It accepts safe metadata, never a request body, credential or execution Target.
package routerobserve

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

// Sink is implemented by routerledger.Store. Persistence happens only in Begin
// and Finish, outside the observer lock and never inside the synchronous callback.
type Sink interface {
	Start(routerledger.Metadata) error
	Finish(string, routerledger.Outcome) error
}

// progressSink is optional. Store.Progress uses a separate bounded memory lock,
// never persistence or queries. A generic Sink need only implement finalization.
type progressSink interface {
	Progress(string, routerledger.ProgressState) error
}

// Request owns one Router-generated ID and its immutable metadata snapshot.
// Calls to Observe and Finish are safe concurrently. The first end wins; all
// callers receive the same persistence result, including a failed write.
type Request struct {
	mu           sync.Mutex
	sink         Sink
	metadata     routerledger.Metadata
	attempts     []routerledger.Attempt
	committed    bool
	retryWaiting bool
	phase        string
	progressErr  error
	finishing    bool
	done         chan struct{}
	finishErr    error
	localCallID     string
	localModel      string
	localUsage      *routerledger.Usage
	cloudProviderID string
	cloudModel      string
	cloudPrice      *routerledger.ReferencePrice
}

// SetCloudTarget records the actual resolved cloud provider/model for requests
// that entered via a routing group or secondary model.
func (r *Request) SetCloudTarget(providerID, model string, price *routerledger.ReferencePrice) {
	if r == nil || (providerID == "" && model == "") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishing {
		return
	}
	r.cloudProviderID = providerID
	r.cloudModel = model
	r.cloudPrice = price
}

// ObserveLocalCall captures the execution's safe actual result in memory. The
// final routercloud.Result remains authoritative whenever it reports usage.
func (r *Request) ObserveLocalCall(v routerruntime.LocalCallObservation) {
	if r == nil || v.Check || v.RequestID != r.metadata.RequestID || v.Purpose != routerruntime.LocalCallExecution || !v.Attempted {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishing || r.localCallID != "" {
		return
	}
	r.localCallID = v.CallID
	r.localModel = v.ModelID
	if v.Usage != nil {
		r.localUsage = &routerledger.Usage{InputTokens: copyCount(v.Usage.InputTokens), OutputTokens: copyCount(v.Usage.OutputTokens), TotalTokens: copyCount(v.Usage.TotalTokens)}
	}
}

// Begin writes the initial processing record before body reading or execution.
// Metadata must come from the URL-bound application's stored profile, using the
// Router's generated ID. It must not be assembled from incoming headers/body.
func Begin(sink Sink, metadata routerledger.Metadata) (*Request, error) {
	if sink == nil {
		return nil, errors.New("request ledger unavailable")
	}
	if err := sink.Start(metadata); err != nil {
		return nil, err
	}
	return &Request{sink: sink, metadata: metadata, done: make(chan struct{})}, nil
}

// Observe keeps at most two attempts in memory and performs no I/O. Result's
// final summaries are authoritative; these events also cover an early End.
func (r *Request) Observe(e routercloud.Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	changed := false
	defer func() {
		progress := routerledger.ProgressState{Attempts: len(r.attempts), OutputCommitted: r.committed, Destination: "none", FailureStage: r.phase}
		if progress.Attempts > 0 {
			progress.Destination = "cloud"
		}
		r.mu.Unlock()
		if sink, ok := r.sink.(progressSink); changed && ok {
			if err := sink.Progress(r.metadata.RequestID, progress); err != nil && !errors.Is(err, routerledger.ErrNotFound) {
				r.mu.Lock()
				if r.progressErr == nil {
					r.progressErr = err
				}
				r.mu.Unlock()
			}
		}
	}()
	if r.finishing {
		return
	}
	// The product writer reports commitment synchronously, before attempting
	// the first client write. Keep this even before attempt_finished arrives.
	if e.Kind == "output_committed" {
		r.committed = true
		r.phase = "response"
		if e.Attempt > 0 && e.Attempt <= len(r.attempts) {
			r.attempts[e.Attempt-1].OutputCommitted = true
		}
		changed = true
		return
	}
	if e.Attempt < 1 || e.Attempt > 2 {
		return
	}
	switch e.Kind {
	case "attempt_started":
		if e.Attempt != len(r.attempts)+1 {
			return
		}
		if len(r.attempts) > 0 {
			r.attempts[len(r.attempts)-1].Retried = true
		}
		r.attempts = append(r.attempts, routerledger.Attempt{Number: e.Attempt, StartedAt: e.At})
		r.retryWaiting = false
		r.phase = "cloud"
		changed = true
	case "attempt_finished", "retry_wait_started", "retry_wait_finished":
		if e.Attempt > len(r.attempts) {
			return
		}
		a := &r.attempts[e.Attempt-1]
		changed = true
		switch e.Kind {
		case "attempt_finished":
			a.DurationMS = nonnegative(e.Duration.Milliseconds())
			a.HTTPStatus = safeStatus(e.HTTPStatus)
			a.ErrorCode = routerledger.SafeCode(e.ErrorCode)
			a.OutputCommitted = a.OutputCommitted || e.OutputCommitted
			r.committed = r.committed || e.OutputCommitted
			if r.committed {
				r.phase = "response"
			}
		case "retry_wait_started":
			a.RetryDelayMS = nonnegative(e.RetryDelay.Milliseconds())
			r.retryWaiting = true
			r.phase = "retry_wait"
		case "retry_wait_finished":
			a.RetryDelayMS = nonnegative(e.RetryDelay.Milliseconds())
			a.RetryWaitMS = nonnegative(e.Duration.Milliseconds())
			a.RetryErrorCode = routerledger.SafeCode(e.ErrorCode)
			// Keep this flag until a real second attempt starts, including a
			// cancellation after the wait returned but before dispatch.
		}
	}
}

// Finish ends execution once, keeping partial reported usage. It never derives
// totals or copies a final attempt's usage onto previous failed attempts.
func (r *Request) Finish(result routercloud.Result, err error) error {
	return r.finish(result, err, "", "", "")
}

// Reject records a pre-execution error, including cancellation.
func (r *Request) Reject(err error) error { return r.Finish(routercloud.Result{}, err) }

// End lets the HTTP owner end a request with a fixed safe classification before
// execution, or after an interruption. Raw text is never retained as an error.
func (r *Request) End(status, code, stage string) error {
	return r.finish(routercloud.Result{}, nil, status, code, stage)
}

func (r *Request) finish(result routercloud.Result, err error, status, code, stage string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.finishing {
		done := r.done
		r.mu.Unlock()
		<-done
		return r.finishErr
	}
	r.finishing = true
	attempts := append([]routerledger.Attempt(nil), r.attempts...)
	retryWaiting := r.retryWaiting
	committed := r.committed || result.OutputCommitted
	progressErr := r.progressErr
	localCallID, localModel, localUsage := r.localCallID, r.localModel, cloneUsage(r.localUsage)
	cloudProviderID, cloudModel, cloudPrice := r.cloudProviderID, r.cloudModel, r.cloudPrice
	r.mu.Unlock()

	if len(result.AttemptDetails) > 0 {
		observedAttempts := attempts
		attempts = nil
		for _, a := range result.AttemptDetails {
			if len(attempts) == 2 {
				break
			}
			attemptCommitted := a.OutputCommitted
			if len(attempts) < len(observedAttempts) {
				attemptCommitted = attemptCommitted || observedAttempts[len(attempts)].OutputCommitted
			}
			attempts = append(attempts, routerledger.Attempt{Number: len(attempts) + 1, StartedAt: a.StartedAt, DurationMS: nonnegative(a.Duration.Milliseconds()), HTTPStatus: safeStatus(a.HTTPStatus), ErrorCode: routerledger.SafeCode(a.ErrorCode), OutputCommitted: attemptCommitted, RetryDelayMS: nonnegative(a.RetryDelay.Milliseconds()), RetryWaitMS: nonnegative(a.RetryWait.Milliseconds()), RetryErrorCode: routerledger.SafeCode(a.RetryErrorCode), Retried: a.Retried})
		}
	}
	count := len(attempts)
	// The public Result count is actual dispatch count. Event fallback is useful
	// to an HTTP owner ending while it has no returned Result yet.
	if result.Attempts > count {
		count = min(result.Attempts, 2)
	}
	code = routerledger.SafeCode(code)
	if code == "" {
		code = errorCode(err)
	}
	if status == "" {
		status = "succeeded"
		if code != "" {
			status = "failed"
		}
		if code == "request_canceled" {
			status = "canceled"
		}
		if code == "cloud_timeout" || code == "budget_exhausted" {
			status = "timed_out"
		}
	}
	if status != "succeeded" && status != "failed" && status != "canceled" && status != "timed_out" && status != "interrupted" {
		status = "failed"
		code = "router_error"
	}
	if status == "succeeded" && code != "" {
		status = "failed"
	}
	if status != "succeeded" && code == "" {
		code = "router_error"
	}
	if stage == "" && code != "" {
		stage = "preflight"
		if count > 0 {
			stage = "cloud"
		}
		if committed {
			stage = "response"
		}
		if retryWaiting && !committed {
			stage = "retry_wait"
		}
		if code == "unsupported_input" || code == "target_mismatch" || strings.HasPrefix(code, "capability_") {
			stage = "validation"
		}
		if code == "invalid_target" || code == "configuration_unavailable" || code == "profile_unconfigured" || code == "credential_missing" || code == "target_missing" {
			stage = "configuration"
		}
	}
	if !safeStage(stage) {
		stage = "preflight"
	}
	destination := "none"
	if result.Destination == "local" || result.Destination == "cloud" {
		destination = result.Destination
	} else if count > 0 {
		destination = "cloud"
	}
	outcome := routerledger.Outcome{Status: status, Destination: destination, Attempts: count, OutputCommitted: committed, SafeErrorCode: code, FailureStage: stage, AttemptDetails: attempts, Decision: result.Decision, JudgmentMS: nonnegative(result.JudgmentDuration.Milliseconds()), CapabilityState: routerledger.SafeCapabilityState(result.CapabilityState), ResolvedProviderID: cloudProviderID, ResolvedModel: cloudModel, ResolvedReferencePrice: cloudPrice}
	outcome.LocalModel = localModel
	if r.metadata.Operation == "generate" && destination == "local" {
		outcome.Usage = localUsage
		outcome.LocalCallID = localCallID
	}
	if r.metadata.Operation == "generate" && (count > 0 || destination == "local") && result.ReportedUsage != nil {
		u := result.ReportedUsage
		outcome.Usage = &routerledger.Usage{InputTokens: copyCount(u.InputTokens), OutputTokens: copyCount(u.OutputTokens), TotalTokens: copyCount(u.TotalTokens)}
		if destination == "cloud" && len(attempts) > 0 {
			attempts[len(attempts)-1].Usage = outcome.Usage
		}
	}
	finishErr := r.sink.Finish(r.metadata.RequestID, outcome)
	if finishErr == nil {
		finishErr = progressErr
	}
	r.mu.Lock()
	r.finishErr = finishErr
	close(r.done)
	r.mu.Unlock()
	return finishErr
}

func copyCount(v *int64) *int64 {
	if v == nil || *v < 0 {
		return nil
	}
	n := *v
	return &n
}
func nonnegative(n int64) int64 { return max(n, 0) }
func safeStatus(n int) int {
	if n < 100 || n > 599 {
		return 0
	}
	return n
}
func safeStage(stage string) bool {
	switch stage {
	case "", "preflight", "configuration", "validation", "cloud", "retry_wait", "response", "interrupted":
		return true
	}
	return false
}
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "budget_exhausted"
	}
	var cloud *routercloud.Error
	if errors.As(err, &cloud) {
		return safeErrorCode(cloud.Code)
	}
	var request *routerrequest.Error
	if errors.As(err, &request) {
		return safeErrorCode(request.Code)
	}
	return "router_error"
}
func safeErrorCode(code string) string {
	if code = routerledger.SafeCode(code); code != "" {
		return code
	}
	return "router_error"
}
