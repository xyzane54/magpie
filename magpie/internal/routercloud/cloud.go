// Package routercloud executes one explicitly resolved cloud target. It owns no
// application registry, credential selection, account pool or background jobs.
package routercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
)

// DefaultTimeout covers both attempts, response streaming and retry waiting.
// This initial cloud budget is independent of local judgment/execution budgets.
const DefaultTimeout = 120 * time.Second

// Target must come from the exact application's Profile, resolved by the entry
// owner. Model is the Profile's pinned model, never the incoming model selector.
// Provider and its private maps must be immutable for the duration of Execute.
type Target struct {
	Provider          routerconfig.ResolvedProvider
	Model             string
	SessionID         string
	RedactionRequired bool
	// ResponseCapabilities is the Profile's explicit enhanced native tool
	// allowlist. It never implies provider discovery or model fallback.
	ResponseCapabilities []string
	// Authorization is the exact app-resolved dynamic upstream credential.
	// When supplied it replaces both saved auth headers and the saved API key.
	Authorization string `json:"-"`
}

type Request struct {
	Protocol string
	Body     []byte
	Target   Target
}

// PreparedRequest is an internal W4/W5 handoff. Its fields are private so a
// public caller cannot claim that arbitrary bytes are already prepared or use
// that claim to bypass the redaction gate.
type PreparedRequest struct {
	protocol    string
	sourceBody  []byte
	body        []byte
	target      Target
	countTokens bool
	valid       bool
}

func (p PreparedRequest) Body() []byte       { return append([]byte(nil), p.body...) }
func (p PreparedRequest) SourceBody() []byte { return append([]byte(nil), p.sourceBody...) }
func (p PreparedRequest) WithBody(body []byte) PreparedRequest {
	p.body = append([]byte(nil), body...)
	return p
}

// Result is one business operation, regardless of Attempts. Unknown usage is
// represented by nil; it must not be recorded as zero by the entry's ledger.
type Result struct {
	Attempts        int
	OutputCommitted bool
	Usage           *cloudcodec.Usage
	// ReportedUsage preserves each actually supplied count, including partial
	// usage and explicit zero. Usage above retains the existing IR contract.
	ReportedUsage  *cloudcodec.ReportedUsage
	Operation      string
	Duration       time.Duration
	AttemptDetails []Attempt
	// Routing metadata is safe business telemetry. It contains no body,
	// provider URL or credential and is populated by the W4 orchestrator.
	Destination      string
	Decision         string
	JudgmentDuration time.Duration
	CapabilityState  string
}

// Error is safe for protocol errors and audit metadata. It deliberately omits
// upstream bodies, transport messages, URLs, credentials and original input.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string                        { return e.Message }
func failure(code, message string, status int) *Error { return &Error{code, message, status} }

// Executor is reusable concurrently if its configuration is immutable. A custom
// Transport is a trusted test/integration seam; it must never retry internally.
// Nil uses a fresh per-operation transport with the provider's proxy settings.
type Executor struct {
	Timeout   time.Duration
	Transport http.RoundTripper
	Observe   func(Event)
	// BeforeAttempt gates each actual dispatch; it never implements retries.
	BeforeAttempt func(context.Context) error
}

// ProtocolForPath accepts only the retained model APIs. The entry owner handles
// application identity, method/auth/origin checks and auxiliary routes itself.
func ProtocolForPath(path string) (string, bool) {
	switch path {
	case "/v1/chat/completions":
		return "chat", true
	case "/v1/responses":
		return "responses", true
	case "/v1/messages":
		return "anthropic", true
	}
	return "", false
}

// ExecutePath dispatches a validated app-relative API path. Counting is kept
// distinct from generation so a caller cannot accidentally count by generating.
func (e Executor) ExecutePath(ctx context.Context, w http.ResponseWriter, path string, in Request) (Result, error) {
	if path == "/v1/messages/count_tokens" {
		in.Protocol = "anthropic"
		return e.CountTokens(ctx, w, in)
	}
	protocol, ok := ProtocolForPath(path)
	if !ok {
		trace := newExecutionTrace("unsupported", e.Observe)
		out := Result{}
		err := failure("unsupported_operation", "model operation is not supported", 501)
		if ctx.Err() != nil {
			err = contextFailure(ctx)
		}
		trace.finish(&out, err)
		return out, err
	}
	in.Protocol = protocol
	return e.Execute(ctx, w, in)
}

// Models is a local Profile-constrained list, not a discovery operation.
func Models(target Target) []string {
	if strings.TrimSpace(target.Model) == "" {
		return []string{}
	}
	return []string{target.Model}
}

// Execute writes successful output only. Call WriteError on failure only when
// OutputCommitted is false; after output the caller must terminate the stream.
// A future redaction owner must supply a separate reviewed prepared-request
// contract. Until then redaction-required operations fail closed, even on retry.
func (e Executor) Execute(parent context.Context, w http.ResponseWriter, in Request) (out Result, err error) {
	return e.execute(parent, w, in, false)
}

// ExecutePreparedPath consumes a PreparedRequest created by
// PrepareRequestBody. It is the only executor entry that accepts W5-processed
// bytes and therefore keeps the redaction gate private to this package.
func (e Executor) ExecutePreparedPath(parent context.Context, w http.ResponseWriter, path string, p PreparedRequest) (Result, error) {
	if !p.valid {
		return Result{}, failure("invalid_prepared_request", "prepared request is unavailable", 500)
	}
	protocol, ok := ProtocolForPath(path)
	countTokens := path == "/v1/messages/count_tokens"
	if !ok && !countTokens {
		return e.ExecutePath(parent, w, path, Request{Protocol: p.protocol, Body: p.sourceBody, Target: p.target})
	}
	if countTokens {
		protocol = "anthropic"
	}
	in := Request{Protocol: protocol, Body: p.sourceBody, Target: p.target}
	p.protocol = protocol
	p.countTokens = countTokens
	return e.executeWithPrepared(parent, w, in, countTokens, &p)
}

// CountTokens uses the same resolved target, redaction gate, timeout and attempt
// budget as generation. It requires a native Anthropic counting endpoint; it
// never substitutes generation or a local estimate for an actual token count.
func (e Executor) CountTokens(parent context.Context, w http.ResponseWriter, in Request) (out Result, err error) {
	return e.execute(parent, w, in, true)
}

func (e Executor) execute(parent context.Context, w http.ResponseWriter, in Request, countTokens bool) (out Result, err error) {
	return e.executeWithPrepared(parent, w, in, countTokens, nil)
}

func (e Executor) executeWithPrepared(parent context.Context, w http.ResponseWriter, in Request, countTokens bool, prepared *PreparedRequest) (out Result, err error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	operation := "generate"
	if countTokens {
		operation = "count_tokens"
	}
	trace := newExecutionTrace(operation, e.Observe)
	defer func() { trace.finish(&out, err) }()
	if ctx.Err() != nil {
		return out, contextFailure(ctx)
	}
	if prepared == nil && in.Target.RedactionRequired {
		return out, failure("redaction_unavailable", "required redaction is unavailable", 503)
	}
	if err := ValidateInput(in.Protocol, in.Body); err != nil {
		if ctx.Err() != nil {
			return out, contextFailure(ctx)
		}
		return out, failure("unsupported_input", "invalid or unsupported model input", 400)
	}
	if ctx.Err() != nil {
		return out, contextFailure(ctx)
	}
	p := in.Target.Provider
	if len(in.Target.Authorization) > 16384 || in.Target.Authorization != "" && (strings.TrimSpace(in.Target.Authorization) == "" || strings.IndexFunc(in.Target.Authorization, func(ch rune) bool { return ch < 32 || ch == 127 }) >= 0) {
		return out, failure("invalid_target", "invalid dynamic cloud authorization", 503)
	}
	if strings.ContainsAny(p.APIKey, "\r\n\x00") || strings.ContainsAny(in.Target.SessionID, "\r\n\x00") {
		return out, failure("invalid_target", "invalid cloud credential or session identifier", 503)
	}
	for key, value := range p.Headers {
		if forbiddenHeader(key) || !validHeaderName(key) || strings.ContainsAny(value, "\r\n\x00") {
			return out, failure("invalid_target", "invalid cloud header", 503)
		}
	}
	endpoint, err := targetURL(p.Config.BaseURL, p.Config.Protocol)
	if err != nil || strings.TrimSpace(in.Target.Model) == "" || len(in.Target.Model) > 256 || strings.ContainsAny(in.Target.Model, "\r\n\x00") {
		return out, failure("invalid_target", "cloud target is not configured", 503)
	}
	if countTokens {
		endpoint += "/count_tokens"
	}
	var codec *cloudcodec.CloudCodec
	var body []byte
	if prepared != nil {
		body = append([]byte(nil), prepared.body...)
		if !countTokens {
			codec, err = cloudcodec.NewCloudCodec(in.Protocol, in.Body)
		}
	} else if countTokens {
		body, err = extraBody(in.Body, p.ExtraBody, in.Target.SessionID)
		if err == nil {
			prepared := in
			prepared.Body = body
			body, err = prepareTokenCount(prepared)
		}
	} else {
		codec, err = cloudcodec.NewCloudCodec(in.Protocol, in.Body)
		if err != nil {
			return out, failure("unsupported_input", "invalid or unsupported model input", 400)
		}
		u, _ := url.Parse(endpoint)
		body, err = codec.Request(p.Config.Protocol, in.Target.Model, u.Hostname())
		if err != nil {
			var capability *cloudcodec.CapabilityError
			if errors.As(err, &capability) {
				return out, failure(capability.Code, capability.Message, 400)
			}
			return out, failure("unsupported_input", "input cannot be represented by the configured protocol", 400)
		}
		if err := cloudcodec.ValidateCapabilities(in.Protocol, in.Body, in.Target.ResponseCapabilities); err != nil {
			var capability *cloudcodec.CapabilityError
			if errors.As(err, &capability) {
				return out, failure(capability.Code, capability.Message, 400)
			}
			return out, failure("capability_protocol_conversion", "configured Profile capability is unavailable", 400)
		}
		body, err = extraBody(body, p.ExtraBody, in.Target.SessionID)
	}
	if err != nil {
		return out, err
	}
	if ctx.Err() != nil {
		return out, contextFailure(ctx)
	}
	transport := e.Transport
	if transport == nil {
		// The request context owns the total deadline. A second header timer
		// racing that deadline can misclassify budget expiry as a network error.
		owned := &http.Transport{Proxy: http.ProxyFromEnvironment}
		switch p.Config.ProxyMode {
		case "", "system":
		case "direct":
			owned.Proxy = nil
		case "custom":
			proxy, parseErr := url.Parse(p.ProxyURL)
			if parseErr != nil || proxy.Hostname() == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
				return out, failure("invalid_target", "invalid cloud proxy", 503)
			}
			owned.Proxy = http.ProxyURL(proxy)
		default:
			return out, failure("invalid_target", "invalid cloud proxy mode", 503)
		}
		defer owned.CloseIdleConnections()
		transport = owned
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for attempt := 1; attempt <= 2; attempt++ {
		if ctx.Err() != nil {
			return out, contextFailure(ctx)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return out, failure("invalid_target", "invalid cloud address", 503)
		}
		// Even custom Idempotency-Key headers must not activate net/http's
		// independent replay loop. Only this function owns the second attempt.
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if p.Config.Protocol == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
			if p.APIKey != "" && in.Target.Authorization == "" {
				req.Header.Set("x-api-key", p.APIKey)
			}
		} else if p.APIKey != "" && in.Target.Authorization == "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		for key, value := range p.Headers {
			if in.Target.Authorization != "" && (strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-api-key")) {
				continue
			}
			req.Header.Set(key, strings.ReplaceAll(value, "${sessionId}", in.Target.SessionID))
		}
		if in.Target.Authorization != "" {
			req.Header.Set("Authorization", in.Target.Authorization)
		}
		// No incoming header, including the management token, is copied here.
		if e.BeforeAttempt != nil {
			if gateErr := e.BeforeAttempt(ctx); gateErr != nil {
				return out, gateErr
			}
		}
		out.Attempts++
		trace.beginAttempt(attempt)
		response, sendErr := client.Do(req)
		if ctx.Err() != nil {
			if response != nil {
				response.Body.Close()
			}
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			trace.endAttempt(status, contextFailure(ctx), false)
			return out, contextFailure(ctx)
		}
		if sendErr != nil {
			if response != nil {
				response.Body.Close()
			}
			trace.endAttempt(0, failure("cloud_network_error", "cloud connection failed", 502), false)
			if attempt == 1 && temporaryNetwork(sendErr) {
				if err := trace.waitRetry(ctx, time.Second); err != nil {
					return out, err
				}
				continue
			}
			return out, failure("cloud_network_error", "cloud connection failed", 502)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			status := response.StatusCode
			delay, allowed := retryDelay(response.Header.Get("Retry-After"), time.Now())
			upstreamErr := upstreamFailure(status, response.Body)
			response.Body.Close()
			if ctx.Err() != nil {
				upstreamErr = contextFailure(ctx)
				trace.endAttempt(status, upstreamErr, false)
				return out, upstreamErr
			}
			trace.endAttempt(status, upstreamErr, false)
			if attempt == 1 && retryStatus(status) && allowed {
				if err := trace.waitRetry(ctx, delay); err != nil {
					return out, err
				}
				continue
			}
			return out, upstreamErr
		}
		observed := &responseReader{Reader: response.Body}
		var reply cloudcodec.CloudReply
		var replyErr error
		if countTokens {
			reply.OutputCommitted, replyErr = replyTokenCount(w, response.Header.Get("Content-Type"), observed)
		} else {
			reply, replyErr = codec.Reply(w, p.Config.Protocol, response.Header.Get("Content-Type"), observed)
		}
		response.Body.Close()
		out.OutputCommitted = reply.OutputCommitted
		out.ReportedUsage = reply.ReportedUsage
		if reply.UsageKnown {
			usage := reply.Usage
			out.Usage = &usage
		}
		if ctx.Err() != nil {
			trace.endAttempt(response.StatusCode, contextFailure(ctx), out.OutputCommitted)
			return out, contextFailure(ctx)
		}
		if replyErr != nil {
			trace.endAttempt(response.StatusCode, failure("cloud_response_error", "cloud response ended unsuccessfully", 502), out.OutputCommitted)
			if !out.OutputCommitted && attempt == 1 && temporaryNetwork(observed.failure) {
				if err := trace.waitRetry(ctx, time.Second); err != nil {
					return out, err
				}
				out.Usage = nil
				out.ReportedUsage = nil
				continue
			}
			return out, failure("cloud_response_error", "cloud response ended unsuccessfully", 502)
		}
		trace.endAttempt(response.StatusCode, nil, out.OutputCommitted)
		return out, nil
	}
	return out, failure("cloud_error", "cloud request failed", 502)
}

type responseReader struct {
	io.Reader
	failure error
	bytes   int
}

func (r *responseReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	r.bytes += n
	if err != nil && (err != io.EOF || r.bytes == 0) {
		r.failure = err
	}
	return n, err
}

func targetURL(base, protocol string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(base, "?#") {
		return "", errors.New("invalid target")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	switch protocol {
	case "chat":
		path += "/chat/completions"
	case "responses":
		path += "/responses"
	case "anthropic":
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		path += "/messages"
	default:
		return "", errors.New("invalid protocol")
	}
	u.Path, err = url.PathUnescape(path)
	if err != nil {
		return "", errors.New("invalid target")
	}
	u.RawPath = path
	return u.String(), nil
}

// PrepareRequestBody performs protocol conversion and provider option/session
// expansion exactly once. W4 calls it before W5's redaction gate, so the body
// handed to a Redactor is the final target-protocol request. The returned type
// can only be consumed through ExecutePreparedPath.
func PrepareRequestBody(in Request, countTokens bool) (PreparedRequest, error) {
	if len(in.Body) == 0 || strings.TrimSpace(in.Target.Model) == "" {
		return PreparedRequest{}, failure("invalid_target", "cloud target is not configured", 503)
	}
	if _, err := targetURL(in.Target.Provider.Config.BaseURL, in.Target.Provider.Config.Protocol); err != nil {
		return PreparedRequest{}, failure("invalid_target", "cloud target is not configured", 503)
	}
	source := append([]byte(nil), in.Body...)
	var body []byte
	var err error
	if countTokens {
		body, err = extraBody(source, in.Target.Provider.ExtraBody, in.Target.SessionID)
		if err == nil {
			prepared := in
			prepared.Body = body
			body, err = prepareTokenCount(prepared)
		}
	} else {
		codec, codecErr := cloudcodec.NewCloudCodec(in.Protocol, source)
		if codecErr != nil {
			return PreparedRequest{}, failure("unsupported_input", "invalid or unsupported model input", 400)
		}
		u, _ := url.Parse(in.Target.Provider.Config.BaseURL)
		body, err = codec.Request(in.Target.Provider.Config.Protocol, in.Target.Model, u.Hostname())
		if err == nil {
			err = cloudcodec.ValidateCapabilities(in.Protocol, source, in.Target.ResponseCapabilities)
		}
		if err == nil {
			body, err = extraBody(body, in.Target.Provider.ExtraBody, in.Target.SessionID)
		}
	}
	if err != nil {
		var capability *cloudcodec.CapabilityError
		if errors.As(err, &capability) {
			return PreparedRequest{}, failure(capability.Code, capability.Message, 400)
		}
		if countTokens {
			return PreparedRequest{}, err
		}
		return PreparedRequest{}, failure("unsupported_input", "input cannot be represented by the configured protocol", 400)
	}
	return PreparedRequest{protocol: in.Protocol, sourceBody: source, body: body, target: in.Target, countTokens: countTokens, valid: true}, nil
}

func forbiddenHeader(key string) bool {
	switch strings.ToLower(key) {
	case "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-connection", "cookie", "set-cookie", "trailer", "te", "content-type", "accept-encoding":
		return true
	}
	return false
}

func validHeaderName(key string) bool {
	if key == "" {
		return false
	}
	for _, ch := range key {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch) {
			continue
		}
		return false
	}
	return true
}

func extraBody(body []byte, extra map[string]json.RawMessage, session string) ([]byte, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil, failure("invalid_target", "invalid cloud body", 503)
	}
	for key, raw := range extra {
		// These fields select a target or change which conversation is sent.
		switch strings.ToLower(strings.TrimPrefix(key, "!")) {
		case "model", "models", "stream", "messages", "input", "contents", "instructions", "system", "systeminstruction", "system_instruction", "tools", "tool_choice", "functions", "function_call", "previous_response_id", "conversation", "background", "store", "modalities", "audio", "image", "images", "video", "web_search_options", "web_search", "plugins", "mcp_servers", "prompt", "tool_resources", "data_sources", "attachments", "file_ids", "provider", "route", "fallback", "fallbacks", "routing":
			return nil, failure("invalid_target", "cloud options cannot override conversation or target", 503)
		}
		if strings.HasPrefix(key, "!") || !json.Valid(raw) {
			return nil, failure("invalid_target", "invalid cloud options", 503)
		}
		// Expand only decoded string values; never substitute inside raw JSON.
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return nil, failure("invalid_target", "invalid cloud options", 503)
		}
		object[key], _ = json.Marshal(expandSession(value, session))
	}
	return json.Marshal(object)
}

func expandSession(value any, session string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "${sessionId}", session)
	case []any:
		for i := range v {
			v[i] = expandSession(v[i], session)
		}
		return v
	case map[string]any:
		for k := range v {
			v[k] = expandSession(v[k], session)
		}
		return v
	}
	return value
}

func temporaryNetwork(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var network net.Error
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || (errors.As(err, &network) && (network.Timeout() || network.Temporary()))
}
func retryStatus(status int) bool {
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}
func retryDelay(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return time.Second, true
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds < 0 || seconds > 3 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	} else if errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, delay <= 3*time.Second
	}
	return time.Second, true
}
func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextFailure(ctx)
	case <-timer.C:
		if ctx.Err() != nil {
			return contextFailure(ctx)
		}
		return nil
	}
}
func contextFailure(ctx context.Context) *Error {
	// A socket deadline can cancel the HTTP parent before the context timer
	// fires. Expiry still describes a timeout, not a client cancellation.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return failure("cloud_timeout", "cloud request budget exhausted", 504)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return failure("request_canceled", "request canceled", 499)
	}
	return failure("cloud_timeout", "cloud request budget exhausted", 504)
}
func statusFailure(status int) *Error {
	switch status {
	case 401:
		return failure("cloud_auth_error", "cloud authentication failed; check the key and its service address", status)
	case 403:
		return failure("cloud_permission_error", "cloud denied generation; check model access and key/service compatibility", status)
	case 400, 422:
		return failure("cloud_parameter_error", "cloud rejected the selected model or request parameters; verify this model before saving", status)
	case 404:
		return failure("cloud_model_not_found", "configured cloud model or endpoint was not found", 404)
	case 429:
		return failure("cloud_rate_limit", "cloud rate limit reached", 429)
	default:
		return failure("cloud_upstream_error", "cloud upstream refused the request", 502)
	}
}

// WriteError is used only before any output is committed. It never writes an
// internal Go error string or an upstream body into a client response.
func WriteError(w http.ResponseWriter, protocol string, err error) {
	safe := failure("router_error", "model request failed", 500)
	var supplied *Error
	if errors.As(err, &supplied) {
		safe = supplied
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(safe.Status)
	if protocol == "anthropic" {
		kind := "api_error"
		switch safe.Status {
		case 400, 415, 422:
			kind = "invalid_request_error"
		case 401:
			kind = "authentication_error"
		case 403:
			kind = "permission_error"
		case 404:
			kind = "not_found_error"
		case 413:
			kind = "request_too_large"
		case 429:
			kind = "rate_limit_error"
		case 503:
			kind = "overloaded_error"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": safe.Message}})
		return
	}
	kind := "server_error"
	switch safe.Status {
	case 400, 404, 413, 415, 422:
		kind = "invalid_request_error"
	case 401:
		kind = "authentication_error"
	case 403:
		kind = "permission_error"
	case 429:
		kind = "rate_limit_error"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": kind, "code": safe.Code, "message": safe.Message}})
}
