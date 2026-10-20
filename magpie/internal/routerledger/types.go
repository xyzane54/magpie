// Package routerledger persists safe business-request summaries independently of
// Magpie's usage, provider, settings and session lifecycles.
package routerledger

import (
	"errors"
	"time"
)

const SchemaVersion = 2

var (
	ErrUnavailable = errors.New("ledger_unavailable")
	ErrCorrupt     = errors.New("ledger_corrupt")
	ErrSchema      = errors.New("ledger_schema_incompatible")
	ErrInvalid     = errors.New("ledger_invalid_input")
	ErrNotFound    = errors.New("ledger_request_not_found")
	ErrCapacity    = errors.New("ledger_capacity_exhausted")
	ErrConflict    = errors.New("ledger_request_conflict")
)

type Options struct {
	MaxRecords         int
	MaxAge             time.Duration
	MaxCalls           int
	MaxCallsPerRequest int
	MaxCheckCalls      int
}

// Metadata is a request-time snapshot. Callers must provide Router-generated
// identities and configured names, never client headers, URLs or content.
type Metadata struct {
	// AcceptedAt is supplied only by the HTTP owner, before preparation. Zero
	// lets standalone callers use Start's clock. It is not a second wire field.
	AcceptedAt     time.Time       `json:"-"`
	RequestID      string          `json:"requestId"`
	AppID          string          `json:"appId"`
	AppName        string          `json:"appName"`
	Operation      string          `json:"operation"`
	Path           string          `json:"path"`
	ProviderID     string          `json:"providerId"`
	Model          string          `json:"model"`
	ReferencePrice *ReferencePrice `json:"referencePrice"`
}

// ReferencePrice is the immutable user-configured cloud target price at entry.
// It is a reference for estimates, never a statement of actual billed cost.
type ReferencePrice struct {
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
	Input    float64 `json:"input"`
	Output   float64 `json:"output"`
	Version  string  `json:"version"`
	Source   string  `json:"source"`
}

// Usage preserves each field's presence. TotalTokens is populated only when the
// upstream explicitly reported a total; missing values are never computed.
type Usage struct {
	InputTokens  *int64 `json:"inputTokens"`
	OutputTokens *int64 `json:"outputTokens"`
	TotalTokens  *int64 `json:"totalTokens"`
}

type Attempt struct {
	Number          int       `json:"number"`
	StartedAt       time.Time `json:"startedAt"`
	DurationMS      int64     `json:"durationMs"`
	HTTPStatus      int       `json:"httpStatus"`
	ErrorCode       string    `json:"errorCode"`
	OutputCommitted bool      `json:"outputCommitted"`
	RetryDelayMS    int64     `json:"retryDelayMs"`
	RetryWaitMS     int64     `json:"retryWaitMs"`
	RetryErrorCode  string    `json:"retryErrorCode"`
	Retried         bool      `json:"retried"`
	Usage           *Usage    `json:"usage"`
}

type Outcome struct {
	ExecutionKnown         bool            `json:"executionKnown"`
	Status                 string          `json:"status"`
	Destination            string          `json:"destination"`
	LocalModel             string          `json:"localModel"`
	LocalCallID            string          `json:"localCallId"`
	Attempts               int             `json:"attempts"`
	OutputCommitted        bool            `json:"outputCommitted"`
	SafeErrorCode          string          `json:"safeErrorCode"`
	FailureStage           string          `json:"failureStage"`
	Usage                  *Usage          `json:"usage"`
	AttemptDetails         []Attempt       `json:"attemptDetails"`
	Decision               string          `json:"decision,omitempty"`
	JudgmentMS             int64           `json:"judgmentMs,omitempty"`
	CapabilityState        string          `json:"capabilityState,omitempty"`
	ResolvedProviderID     string          `json:"-"`
	ResolvedModel          string          `json:"-"`
	ResolvedReferencePrice *ReferencePrice `json:"-"`
}

type Record struct {
	SchemaVersion int `json:"schemaVersion"`
	Metadata
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt"`
	DurationMS int64      `json:"durationMs"`
	Outcome
}

type Query struct {
	Limit     int
	Offset    int
	Before    string
	AppID     string
	Status    string
	Operation string
	From      time.Time
	To        time.Time
}

type Page struct {
	Records    []Record `json:"records"`
	Total      int      `json:"total"`
	NextCursor string   `json:"nextCursor"`
}

type TokenSummary struct {
	KnownTokens     int64 `json:"knownTokens"`
	KnownRequests   int   `json:"knownRequests"`
	UnknownRequests int   `json:"unknownRequests"`
}

type Summary struct {
	CoreStatistics
	Operation       string       `json:"operation"`
	Total           int          `json:"total"`
	Processing      int          `json:"processing"`
	Succeeded       int          `json:"succeeded"`
	Failed          int          `json:"failed"`
	Canceled        int          `json:"canceled"`
	TimedOut        int          `json:"timedOut"`
	Interrupted     int          `json:"interrupted"`
	Attempts        int          `json:"attempts"`
	InputTokens     TokenSummary `json:"inputTokens"`
	OutputTokens    TokenSummary `json:"outputTokens"`
	TotalTokens     TokenSummary `json:"totalTokens"`
	OldestStartedAt *time.Time   `json:"oldestStartedAt"`
	NewestStartedAt *time.Time   `json:"newestStartedAt"`
}

type Status struct {
	ObservationFailures int64  `json:"observationFailures"`
	FailureCount        int64  `json:"failureCount"`
	LastErrorCode       string `json:"lastErrorCode"`
	Available           bool   `json:"available"`
	ErrorCode           string `json:"errorCode"`
	SchemaVersion       int    `json:"schemaVersion"`
	MaxRecords          int    `json:"maxRecords"`
	MaxAgeSeconds       int64  `json:"maxAgeSeconds"`
	MaxCalls            int    `json:"maxCalls"`
	MaxCallsPerRequest  int    `json:"maxCallsPerRequest"`
	MaxCheckCalls       int    `json:"maxCheckCalls"`
}

// ProgressState is an in-memory projection of actual events. It never contains
// raw execution targets, errors, usage buffers or request/response content.
type ProgressState struct {
	Attempts        int
	OutputCommitted bool
	Destination     string
	FailureStage    string
}
