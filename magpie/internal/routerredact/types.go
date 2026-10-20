// Package routerredact prepares private cloud requests and restores their replies.
// It does not decide routing or claim sensitive-entity recognition quality.
package routerredact

import (
	"context"
	"time"
)

type Protocol string

const (
	Chat      Protocol = "chat"
	Responses Protocol = "responses"
	Anthropic Protocol = "anthropic"
)

// Versions freezes the local runtime/model/template fingerprint, the redaction
// prompt/schema and the redaction configuration. Cloud or judgment versions
// cannot substitute for these independent capability/cache dimensions.
type Versions struct{ Model, Prompt, Config string }

// Input describes the final outbound body after target binding, conversion and
// all provider option/session expansion. The caller keeps the client response
// protocol separately and must not append original options after Prepare.
type Input struct {
	RequestID       string
	AppID           string
	Protocol        Protocol
	Operation       string
	SessionID       string
	SessionVerified bool   // Server-verified session source; a Profile ID is insufficient.
	SeedSessionID   string // Optional server-derived seed key written after first-turn extraction without enabling first-turn cache lookup.
	Versions        Versions
	Body            []byte
}
type Extraction struct {
	Versions Versions
	Texts    []string
}

// Entity offsets are UTF-8 byte boundaries in one exact supplied text value.
type Entity struct {
	Field          int
	Start, End     int
	Text, Category string
}
type Result struct {
	Complete bool
	Entities []Entity
}
type Extractor interface {
	Extract(context.Context, Extraction) (Result, error)
}
type ExtractorFunc func(context.Context, Extraction) (Result, error)

func (f ExtractorFunc) Extract(c context.Context, in Extraction) (Result, error) {
	if f == nil {
		return Result{}, fail("redaction_unavailable")
	}
	return f(c, in)
}

// Failure never contains source text, upstream errors, IDs, or a private mapping.
type Failure struct{ Code string }

func (f *Failure) Error() string { return f.Code }
func (f *Failure) Unwrap() error {
	if f.Code == "redaction_canceled" {
		return context.Canceled
	}
	if f.Code == "redaction_timeout" {
		return context.DeadlineExceeded
	}
	return nil
}
func fail(code string) error { return &Failure{Code: code} }

type Options struct {
	Extractor    Extractor
	Budget       time.Duration
	Experimental bool // Explicit trial opt-in; raises only the budget ceiling to 30 seconds.
	CacheEntries int
	CacheBytes   int
	CacheTTL     time.Duration
	Now          func() time.Time
}
