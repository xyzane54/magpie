package routerledger

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

func identifier(s string, max int, optional bool) bool {
	if s == "" {
		return optional
	}
	if len(s) > max {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func display(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.ContainsAny(s, "?&#@=\\") || strings.Contains(s, "://") {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	lower := strings.ToLower(s)
	for _, forbidden := range []string{"bearer ", "authorization:", "api-key:", "api_key:", "password:", "sk-"} {
		if strings.HasPrefix(lower, forbidden) {
			return false
		}
	}
	return true
}

func validMetadata(m Metadata) bool {
	if !identifier(m.RequestID, 128, false) || !identifier(m.AppID, 64, false) || !identifier(m.ProviderID, 128, true) || !display(m.AppName, 256) || !display(m.Model, 256) || !validReferencePrice(m.ReferencePrice) {
		return false
	}
	if m.Operation != "generate" && m.Operation != "count_tokens" {
		return false
	}
	switch m.Path {
	case "":
	case "/v1/chat/completions", "/v1/responses", "/v1/messages":
		return m.Operation == "generate"
	case "/v1/messages/count_tokens":
		return m.Operation == "count_tokens"
	default:
		return false
	}
	return true
}

func validReferencePrice(p *ReferencePrice) bool {
	if p == nil {
		return true
	}
	if len(p.Currency) != 3 || p.Unit != "million-tokens" || p.Input < 0 || p.Output < 0 || p.Input > 1e9 || p.Output > 1e9 || math.IsInf(p.Input, 0) || math.IsInf(p.Output, 0) || math.IsNaN(p.Input) || math.IsNaN(p.Output) || p.Version == "" || !display(p.Version, 80) || p.Source == "" || !display(p.Source, 120) {
		return false
	}
	for _, c := range p.Currency {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func validStatus(s string, processing bool) bool {
	switch s {
	case "succeeded", "failed", "canceled", "timed_out", "interrupted":
		return true
	case "processing":
		return processing
	}
	return false
}

// SafeCode accepts only the stable error vocabulary of the request and cloud
// boundaries. Unknown strings, including raw errors, collapse to router_error.
func SafeCode(s string) string {
	switch s {
	case "capability_namespace", "capability_custom", "capability_additional_tools", "capability_web_search", "capability_reasoning_history", "capability_message_phase", "capability_item_identity", "capability_reasoning_configuration", "capability_metadata", "capability_protocol_conversion", "cloud_permission_error", "connection_check_no_text", "connection_check_unsupported", "provider_conflict":
		return s
	case "", "router_error", "cloud_error", "cloud_network_error", "cloud_response_error", "cloud_timeout", "cloud_auth_error", "cloud_parameter_error", "cloud_model_not_found", "cloud_rate_limit", "cloud_upstream_error", "request_canceled", "unsupported_input", "unsupported_operation", "redaction_unavailable", "invalid_target", "app_not_registered", "profile_conflict", "profile_unconfigured", "credential_missing", "credential_misuse", "budget_exhausted", "profile_invalid", "target_missing", "configuration_unavailable", "attempt_forbidden", "target_mismatch", "invalid_app_path", "unsupported_endpoint", "invalid_auth_headers", "request_interrupted", "request_too_large", "invalid_request", "invalid_content_type", "unsupported_media_type":
		return s
	default:
		return "router_error"
	}
}

// SafeCapabilityState classifies routing outcomes independently from terminal
// cloud errors: a successful cloud reply can follow a failed local judgment.
// Only server-owned literals are accepted; raw model errors never enter storage.
func SafeCapabilityState(s string) string {
	switch s {
	case "local_http_error":
		return s
	case "", "router_error", "unavailable", "judgment_stage_timeout", "judgment_failed", "invalid_decision", "execution_protocol_mismatch", "execution_unavailable", "execution_failed", "judgment_input_exceeded", "judgment_summary_incomplete", "context_exceeded", "context_limit_exceeded", "token_count_unavailable", "judgment_queue_timeout", "judgment_inference_timeout", "execution_queue_timeout", "execution_timeout", "local_budget_reserved", "local_generation_http_error", "request_budget_exhausted", "request_canceled", "runtime_settings_changed":
		return s
	default:
		return "router_error"
	}
}

func validUsage(u *Usage) bool {
	if u == nil {
		return true
	}
	for _, p := range []*int64{u.InputTokens, u.OutputTokens, u.TotalTokens} {
		if p != nil && *p < 0 {
			return false
		}
	}
	return true
}

func validOutcome(o Outcome, processing bool) bool {
	if !validStatus(o.Status, processing) || o.Attempts < 0 || o.Attempts > 2 || len(o.AttemptDetails) > o.Attempts || !validUsage(o.Usage) || SafeCode(o.SafeErrorCode) != o.SafeErrorCode || SafeCapabilityState(o.CapabilityState) != o.CapabilityState || !display(o.LocalModel, 256) || !identifier(o.LocalCallID, 128, true) || o.Decision != "" && o.Decision != "local" && o.Decision != "cloud" || o.JudgmentMS < 0 {
		return false
	}
	if o.Destination != "" && o.Destination != "none" && o.Destination != "cloud" && o.Destination != "local" {
		return false
	}
	if o.Attempts > 0 && o.Destination != "cloud" {
		return false
	}
	switch o.FailureStage {
	case "", "preflight", "configuration", "validation", "cloud", "retry_wait", "response", "interrupted":
	default:
		return false
	}
	for i, a := range o.AttemptDetails {
		if a.Number != i+1 || a.StartedAt.IsZero() || a.DurationMS < 0 || a.RetryDelayMS < 0 || a.RetryWaitMS < 0 || a.HTTPStatus != 0 && (a.HTTPStatus < 100 || a.HTTPStatus > 599) || SafeCode(a.ErrorCode) != a.ErrorCode || SafeCode(a.RetryErrorCode) != a.RetryErrorCode || !validUsage(a.Usage) {
			return false
		}
		if a.Retried && i+1 >= o.Attempts {
			return false
		}
	}
	return true
}
