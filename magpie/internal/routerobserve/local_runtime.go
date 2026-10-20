package routerobserve

import (
	"context"
	"math"

	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerruntime"
)

var _ routerruntime.LocalCallObserver = (*Calls)(nil)

// ObserveLocalCall accepts A's shared contract directly. Validation failures
// remain visible in ledger health; they never change the model's result.
func (c *Calls) ObserveLocalCall(v routerruntime.LocalCallObservation) {
	if c == nil || c.sink == nil {
		return
	}
	call := projectLocalCall(v)
	if !routerledger.CallSafe(call) {
		c.sink.ReportObservationFailure()
		return
	}
	c.Emit(call)
}

func projectLocalCall(v routerruntime.LocalCallObservation) routerledger.Call {
	scope := "business"
	if v.Check {
		scope = "check"
	}
	call := routerledger.Call{
		RequestID: v.RequestID, CallID: v.CallID, Scope: scope, Purpose: string(v.Purpose),
		ModelID: v.ModelID, Fingerprint: v.ModelFingerprint, RuntimeID: v.RuntimeID, RuntimeFingerprint: v.RuntimeFingerprint,
		MaterialVersion: v.MaterialVersion, PromptVersion: v.PromptVersion, Suite: v.Suite, Mode: string(v.Mode),
		StartedAt: v.StartedAt, EndedAt: v.EndedAt, Result: string(v.Outcome),
		SafeErrorCode: routerledger.SafeCallCode(v.ErrorCode), InferenceStarted: v.Attempted,
		SettingsFingerprint: v.SettingsFingerprint, Settings: v.Settings, HTTPStatus: v.HTTPStatus,
		Timings: routerledger.CallTimings{QueueLoadMS: wholeMS(v.QueueLoadMS), RequestHeadersMS: wholeMS(v.RequestHeadersMS), ConsumeMS: wholeMS(v.ConsumeMS), TotalMS: wholeMS(v.TotalMS)},
	}
	if u := v.ContextUsage; u != nil {
		call.ContextUsage = &routerledger.CallContextUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ContextTokens: u.ContextTokens}
	}
	if v.Usage != nil {
		call.Usage = &routerledger.Usage{InputTokens: copyCount(v.Usage.InputTokens), OutputTokens: copyCount(v.Usage.OutputTokens), TotalTokens: copyCount(v.Usage.TotalTokens)}
	}
	return call
}

// The persisted timing unit is whole milliseconds. No combined queue/load
// measurement is split into made-up individual phases.
func wholeMS(v *float64) *int64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v >= math.Exp2(63) {
		return nil
	}
	n := int64(*v)
	return &n
}

// WithLocalObserver binds one existing Router request at the HTTP boundary.
// A producer may retrieve LocalObserver and report a completion; no invocation,
// retry, response parsing or model ownership is introduced by this consumer.
func WithLocalObserver(ctx context.Context, calls *Calls, requestID string, check bool, request *Request) context.Context {
	var observer routerruntime.LocalCallObserver = routerruntime.LocalCallObserverFunc(func(v routerruntime.LocalCallObservation) {
		if check {
			if v.RequestID != "" {
				calls.sink.ReportObservationFailure()
				return
			}
			v.Check = true
		} else {
			if v.Check || v.RequestID != "" && v.RequestID != requestID {
				calls.sink.ReportObservationFailure()
				return
			}
			v.RequestID = requestID
		}
		if !routerledger.CallSafe(projectLocalCall(v)) {
			calls.sink.ReportObservationFailure()
			return
		}
		if !check {
			if request == nil {
				calls.sink.ReportObservationFailure()
				return
			}
			request.ObserveLocalCall(v)
		}
		calls.ObserveLocalCall(v)
	})
	if calls == nil || calls.sink == nil {
		return ctx
	}
	return routerruntime.WithLocalCallObserver(ctx, observer)
}

func LocalObserver(ctx context.Context) routerruntime.LocalCallObserver {
	return routerruntime.LocalCallObserverFromContext(ctx)
}
