package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// upstreamOf is the provider an aggregator's reply (a JSON body, or one
// event of its stream) says answered behind it, "" when it says none.
// OpenRouter names it in a top-level "provider" — a Chat body and every
// chunk of its stream, an Anthropic Messages body — and in a Messages
// stream's message_start, as message.provider (seen 2026-10-05: DeepInfra,
// StreamLake). Its Responses replies name none.
func upstreamOf(b []byte) string {
	if !bytes.Contains(b, []byte(`"provider"`)) {
		return ""
	}
	var v struct {
		Provider json.RawMessage `json:"provider"`
		Message  struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"message"`
		Response struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{v.Provider, v.Message.Provider, v.Response.Provider} {
		var s string // a request's provider options echoed back are an object
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" && len(s) <= 64 {
				return s
			}
		}
	}
	return ""
}

// replyEnds says whether an upstream's event ends the reply, read as the
// protocol it was asked in, stop being what upstreamStop read of it: an
// Anthropic message_stop or error, or a message_delta's stop_reason; a
// Responses response.completed, response.incomplete, response.failed or
// error; a Gemini chunk's finishReason (geminiEnds); a Chat
// finish_reason. Another protocol's end, or Anthropic's events with their
// type left out, end nothing: the decoder reads nothing of them, and a
// stream told whole on one became a completed reply that said nothing,
// which the agent took as its turn's end and didn't ask again (#1933).
func replyEnds(proto provider.Protocol, data, stop string) bool {
	switch {
	case proto == provider.Anthropic:
		t := eventType(data)
		return t == "message_stop" || t == "error" || t == "message_delta" && stop != ""
	case proto == provider.Responses:
		t := eventType(data)
		return t == "error" || strings.HasPrefix(t, "response.") && (lastEvent(t) || stop != "")
	case geminiKin(proto):
		return stop != "" || geminiEnds([]byte(data))
	}
	return stop != ""
}
