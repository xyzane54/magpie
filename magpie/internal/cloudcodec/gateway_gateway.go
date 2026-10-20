package cloudcodec

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

func RequiredAllowlist(proto Protocol, body []byte) bool {
	switch proto {
	case Responses:
		var q struct {
			ToolChoice struct{ Type, Mode string } `json:"tool_choice"`
		}
		return DecodeJSON(body, &q) == nil && q.ToolChoice.Type == "allowed_tools" && q.ToolChoice.Mode == "required"
	case Gemini:
		var q struct {
			ToolConfig struct {
				FunctionCallingConfig struct {
					Mode                 string   `json:"mode"`
					AllowedFunctionNames []string `json:"allowedFunctionNames"`
				} `json:"functionCallingConfig"`
			} `json:"toolConfig"`
		}
		return DecodeJSON(body, &q) == nil && strings.EqualFold(q.ToolConfig.FunctionCallingConfig.Mode, "ANY") && len(q.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) > 0
	}
	return false
}

type StreamEncoder interface {
	Event(Event)
	Finish()
	// keepalive tells the client the reply goes on, as its protocol does
	// with no answer to give: one its idle timeout counts (#436)
	Keepalive()
}

// decodeRequest requires one complete JSON object before fields are rewritten
// or a request is routed. In particular, null is not an empty object.
func DecodeRequest(body []byte, dst any) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return errors.New("invalid request: expected a JSON object")
	}
	if err := DecodeJSON(body, dst); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}
