package routercloud

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

// prepareTokenCount accepts only the native Anthropic text/function contract.
// It does not create a message, estimate usage, or select another target.
// Fields follow https://platform.claude.com/docs/en/api/messages/count_tokens.
// Shared execution owns target validation, redaction, authentication and retries.
func prepareTokenCount(in Request) ([]byte, error) {
	if in.Protocol != "anthropic" || in.Target.Provider.Config.Protocol != "anthropic" {
		return nil, failure("unsupported_operation", "token counting requires an Anthropic target", http.StatusNotImplemented)
	}
	if err := ValidateInput(in.Protocol, in.Body); err != nil {
		return nil, failure("unsupported_input", "invalid or unsupported token count input", http.StatusBadRequest)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(in.Body, &body) != nil {
		return nil, failure("unsupported_input", "invalid or unsupported token count input", http.StatusBadRequest)
	}
	for key := range body {
		switch key {
		case "model", "messages", "system", "tools", "tool_choice", "thinking", "cache_control", "output_config":
		default:
			// In particular, generation-only controls must not be sent to
			// count_tokens, even when injected by configured extra options.
			return nil, failure("unsupported_input", "invalid or unsupported token count input", http.StatusBadRequest)
		}
	}
	body["model"], _ = json.Marshal(in.Target.Model)
	return json.Marshal(body)
}

const tokenCountReplyLimit = 1 << 20

// replyTokenCount reports only the upstream's actual input_tokens. The count
// endpoint is auxiliary: its result must not become generation token usage.
// Parse and I/O errors never expose upstream data or transport messages.
func replyTokenCount(w http.ResponseWriter, contentType string, body io.Reader) (committed bool, err error) {
	badReply := func() *Error {
		return failure("cloud_response_error", "cloud token count response is invalid or interrupted", http.StatusBadGateway)
	}
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || mediaType != "application/json" {
		return false, badReply()
	}
	b, readErr := io.ReadAll(io.LimitReader(body, tokenCountReplyLimit+1))
	if readErr != nil || len(b) > tokenCountReplyLimit || !utf8.Valid(b) {
		return false, badReply()
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	value, parseErr := inputValue(d)
	if parseErr != nil {
		return false, badReply()
	}
	if _, parseErr = d.Token(); parseErr != io.EOF {
		return false, badReply()
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false, badReply()
	}
	if upstreamError, exists := object["error"]; exists && upstreamError != nil {
		return false, badReply()
	}
	number, ok := object["input_tokens"].(json.Number)
	if !ok {
		return false, badReply()
	}
	// Check the integer spelling without floating-point conversion or a
	// machine-sized integer bound. This preserves all reported digits.
	count := number.String()
	if count == "-0" {
		count = "0"
	}
	for _, digit := range count {
		if digit < '0' || digit > '9' {
			return false, badReply()
		}
	}
	encoded, encodeErr := json.Marshal(map[string]json.Number{"input_tokens": json.Number(count)})
	if encodeErr != nil {
		return false, badReply()
	}
	w.Header().Set("Content-Type", "application/json")
	// A Write can commit headers even when it returns zero bytes and an
	// error. Conservatively prevent retries after any attempted client write.
	n, writeErr := w.Write(encoded)
	if writeErr != nil || n != len(encoded) {
		return true, badReply()
	}
	return true, nil
}
