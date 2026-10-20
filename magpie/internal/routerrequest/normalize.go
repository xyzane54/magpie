package routerrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"strings"
)

var (
	ErrInvalidNormalization = errors.New("invalid routing request")
	ErrRemoteHistory        = errors.New("remote history reference is unsupported")
)

// NormalizeError is safe to return to a protocol adapter and to the request
// ledger. It never includes a body fragment, tool argument, credential or
// provider URL.
type NormalizeError struct {
	Code    string
	Message string
	cause   error
}

func (e *NormalizeError) Error() string { return e.Message }
func (e *NormalizeError) Unwrap() error { return e.cause }

func normalizeError(code, message string) *NormalizeError {
	return &NormalizeError{Code: code, Message: message}
}

func normalizeCause(code, message string, cause error) *NormalizeError {
	return &NormalizeError{Code: code, Message: message, cause: cause}
}

// Input is the immutable request envelope shared by W4 and W5. Context is the
// caller's cancellation scope; Normalize never extends or replaces it.
type Input struct {
	RequestID     string
	AppID         string
	Protocol      string
	Operation     string
	SessionID     string
	Model         string
	PromptVersion string
	OriginalBody  []byte
	Context       context.Context
}

// Prepared contains the exact execution bytes and an intentionally smaller
// judgment representation. Methods return copies so callers cannot mutate a
// request that another stage may still be consuming.
type Prepared struct {
	requestID     string
	appID         string
	protocol      string
	operation     string
	sessionID     string
	model         string
	promptVersion string
	context       context.Context
	original      []byte
	execution     []byte
	judgment      JudgmentView
}

func (p *Prepared) RequestID() string        { return p.requestID }
func (p *Prepared) AppID() string            { return p.appID }
func (p *Prepared) Protocol() string         { return p.protocol }
func (p *Prepared) Operation() string        { return p.operation }
func (p *Prepared) SessionID() string        { return p.sessionID }
func (p *Prepared) Model() string            { return p.model }
func (p *Prepared) PromptVersion() string    { return p.promptVersion }
func (p *Prepared) Context() context.Context { return p.context }

func (p *Prepared) OriginalBody() []byte   { return append([]byte(nil), p.original...) }
func (p *Prepared) ExecutionBody() []byte  { return append([]byte(nil), p.execution...) }
func (p *Prepared) Judgment() JudgmentView { return cloneJudgment(p.judgment) }

// JudgmentView carries complete conversational text and tool interactions,
// while replacing large fixed system/tool schemas with compact summaries.
// Execution never consumes this value.
type JudgmentView struct {
	RequestID           string               `json:"request_id"`
	AppID               string               `json:"app_id"`
	Protocol            string               `json:"protocol"`
	Operation           string               `json:"operation"`
	Model               string               `json:"model,omitempty"`
	PromptVersion       string               `json:"prompt_version,omitempty"`
	System              []string             `json:"system,omitempty"`
	History             []JudgmentMessage    `json:"history"`
	Tools               []ToolSummary        `json:"tools,omitempty"`
	SummaryPolicy       string               `json:"summary_policy,omitempty"`
	SystemSummaries     []BoundedTextSummary `json:"system_summaries,omitempty"`
	RestrictionsOmitted bool                 `json:"restrictions_omitted,omitempty"`
}

// JudgmentMessage is intentionally protocol-neutral. Tool arguments and
// results are copied as JSON values, preserving their object/array/string
// types and call IDs without exposing the original envelope.
type JudgmentMessage struct {
	Role       string              `json:"role"`
	Text       string              `json:"text,omitempty"`
	ToolCalls  []JudgmentToolCall  `json:"tool_calls,omitempty"`
	ToolResult *JudgmentToolResult `json:"tool_result,omitempty"`
	// ToolResults retains every result in a protocol message. ToolResult is
	// kept as a compatibility shortcut for callers that only understand the
	// historical single-result shape; it always points at the first result.
	ToolResults []JudgmentToolResult `json:"tool_results,omitempty"`
	// Parts records the order of mixed text, calls and results. Text remains
	// available in Text for compact judgment consumers, while Parts prevents a
	// same-turn tool result or follow-up sentence from being collapsed away.
	Parts []JudgmentPart `json:"parts,omitempty"`
	Kind  string         `json:"kind,omitempty"`
}

type JudgmentPart struct {
	Kind       string              `json:"kind"`
	Text       string              `json:"text,omitempty"`
	ToolCall   *JudgmentToolCall   `json:"tool_call,omitempty"`
	ToolResult *JudgmentToolResult `json:"tool_result,omitempty"`
}

type JudgmentToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type JudgmentToolResult struct {
	ID      string          `json:"id"`
	Text    string          `json:"text,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

type ToolSummary struct {
	Type               string              `json:"type,omitempty"`
	Name               string              `json:"name"`
	Description        string              `json:"description,omitempty"`
	Parameters         []ToolParameter     `json:"parameters,omitempty"`
	DescriptionSummary *BoundedTextSummary `json:"description_summary,omitempty"`
}

type ToolParameter struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Normalize validates and snapshots one generated model request. It does not
// call a model, read application state or perform an upstream request.
func Normalize(in Input) (*Prepared, error) {
	if in.Context == nil {
		in.Context = context.Background()
	}
	if strings.TrimSpace(in.RequestID) == "" || !routerconfig.ValidAppID(in.AppID) ||
		!validProtocol(in.Protocol) || (in.Operation != "generate" && in.Operation != "count_tokens") ||
		len(in.OriginalBody) == 0 || len(in.OriginalBody) > 8<<20 {
		return nil, normalizeError("invalid_request", "routing request envelope is invalid")
	}
	if err := in.Context.Err(); err != nil {
		return nil, err
	}
	original := append([]byte(nil), in.OriginalBody...)
	if !strictJSON(original) {
		return nil, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	if err := rejectRemoteHistory(in.Protocol, original); err != nil {
		return nil, err
	}
	view, err := makeJudgment(in, original)
	if err != nil {
		return nil, err
	}
	return &Prepared{
		requestID: in.RequestID, appID: in.AppID, protocol: in.Protocol,
		operation: in.Operation, sessionID: in.SessionID, model: in.Model,
		promptVersion: in.PromptVersion, context: in.Context,
		original: original, execution: append([]byte(nil), original...),
		judgment: view,
	}, nil
}

// strictJSON rejects duplicate object keys at every protocol envelope level.
// A parser that keeps only the last key could otherwise validate one request
// and send a different one to the local or cloud path.
func strictJSON(body []byte) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if strictValue(d) != nil {
		return false
	}
	var extra any
	return errors.Is(d.Decode(&extra), io.EOF)
}

func strictValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrInvalidNormalization
			}
			seen[name] = true
			if err := strictValue(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := strictValue(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	default:
		return nil
	}
}

func validProtocol(s string) bool {
	switch s {
	case "chat", "responses", "anthropic":
		return true
	default:
		return false
	}
}
