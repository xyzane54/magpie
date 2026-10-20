package routercloud

import (
	"context"
	"errors"
	"time"
)

// Attempt describes one dispatched request, including a failed request whose
// response never arrived. It contains no URL, credentials, session or content.
type Attempt struct {
	Number          int           `json:"number"`
	StartedAt       time.Time     `json:"startedAt"`
	Duration        time.Duration `json:"durationNs"`
	HTTPStatus      int           `json:"httpStatus,omitempty"`
	ErrorCode       string        `json:"errorCode,omitempty"`
	OutputCommitted bool          `json:"outputCommitted"`
	RetryDelay      time.Duration `json:"retryDelayNs"`
	RetryWait       time.Duration `json:"retryWaitNs"`
	RetryErrorCode  string        `json:"retryErrorCode,omitempty"`
	Retried         bool          `json:"retried"`
}

// Event is a synchronous per-operation notification. The entry can close over
// its request ID to correlate these events with its ledger. Observers must
// return promptly and synchronize their own state across concurrent operations.
type Event struct {
	Kind            string        `json:"kind"`
	Operation       string        `json:"operation"`
	Attempt         int           `json:"attempt"`
	At              time.Time     `json:"at"`
	Duration        time.Duration `json:"durationNs"`
	HTTPStatus      int           `json:"httpStatus,omitempty"`
	ErrorCode       string        `json:"errorCode,omitempty"`
	OutputCommitted bool          `json:"outputCommitted"`
	RetryDelay      time.Duration `json:"retryDelayNs"`
}

type executionTrace struct {
	operation string
	started   time.Time
	observe   func(Event)
	attempts  []Attempt
}

func newExecutionTrace(operation string, observe func(Event)) *executionTrace {
	t := &executionTrace{operation: operation, started: time.Now(), observe: observe}
	t.emit(Event{Kind: "request_started", At: t.started})
	return t
}
func (t *executionTrace) emit(event Event) {
	if t.observe == nil {
		return
	}
	event.Operation = t.operation
	if event.At.IsZero() {
		event.At = time.Now()
	}
	t.observe(event)
}
func (t *executionTrace) beginAttempt(number int) {
	if len(t.attempts) > 0 {
		t.attempts[len(t.attempts)-1].Retried = true
	}
	started := time.Now()
	t.attempts = append(t.attempts, Attempt{Number: number, StartedAt: started})
	t.emit(Event{Kind: "attempt_started", Attempt: number, At: started})
}
func (t *executionTrace) endAttempt(status int, err error, committed bool) {
	a := &t.attempts[len(t.attempts)-1]
	a.Duration = time.Since(a.StartedAt)
	a.HTTPStatus, a.ErrorCode, a.OutputCommitted = status, safeErrorCode(err), committed
	t.emit(Event{Kind: "attempt_finished", Attempt: a.Number, Duration: a.Duration, HTTPStatus: status, ErrorCode: a.ErrorCode, OutputCommitted: committed})
}
func (t *executionTrace) waitRetry(ctx context.Context, delay time.Duration) error {
	a := &t.attempts[len(t.attempts)-1]
	a.RetryDelay = delay
	started := time.Now()
	t.emit(Event{Kind: "retry_wait_started", Attempt: a.Number, RetryDelay: delay})
	err := waitRetry(ctx, delay)
	a.RetryWait = time.Since(started)
	a.RetryErrorCode = safeErrorCode(err)
	t.emit(Event{Kind: "retry_wait_finished", Attempt: a.Number, Duration: a.RetryWait, RetryDelay: delay, ErrorCode: safeErrorCode(err)})
	return err
}
func (t *executionTrace) finish(out *Result, err error) {
	out.Operation, out.Duration = t.operation, time.Since(t.started)
	out.AttemptDetails = append([]Attempt(nil), t.attempts...)
	t.emit(Event{Kind: "request_finished", Attempt: out.Attempts, Duration: out.Duration, ErrorCode: safeErrorCode(err), OutputCommitted: out.OutputCommitted})
}
func safeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var known *Error
	if errors.As(err, &known) {
		return known.Code
	}
	return "router_error"
}
