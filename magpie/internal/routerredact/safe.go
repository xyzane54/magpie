package routerredact

// SafeFailureCode is an exact allowlist for management/ledger adapters. Unknown
// strings (including arbitrary upstream errors) are never displayed or stored.
func SafeFailureCode(code string) string {
	switch code {
	case "redaction_failed", "redaction_canceled", "redaction_closed", "redaction_extraction_failed", "redaction_input_limit", "redaction_invalid_input", "redaction_invalid_response", "redaction_invalid_result", "redaction_entity_conflict", "redaction_mapping_limit", "redaction_placeholder_limit", "redaction_released", "redaction_response_limit", "redaction_specification_failed", "redaction_timeout", "redaction_unavailable",
		"wire_invalid", "wire_unsupported", "wire_response_limit", "stream_after_terminal", "stream_canceled", "stream_invalid_channel", "stream_invalid_event", "stream_invalid_json", "stream_limit", "stream_opaque_content", "stream_read_failed", "stream_truncated", "stream_unsupported", "stream_upstream_error", "stream_write_failed", "unsupported_protocol":
		return code
	}
	return "redaction_failed"
}
