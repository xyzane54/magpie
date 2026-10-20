package cloudcodec

import (
	"bytes"
	"encoding/json"
	"strings"
)

// partsText reads a content that is an array of typed parts: its text,
// and the text of its thinking parts. ok is false for any other content,
// a string or null.
func PartsText(raw json.RawMessage) (text, think string, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return "", "", false
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking json.RawMessage `json:"thinking"`
	}
	if DecodeJSON(raw, &parts) != nil {
		return "", "", false
	}
	var tb, kb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			tb.WriteString(p.Text)
		case "thinking":
			// Mistral's is a list of text parts; a plain string is taken too
			var s string
			if DecodeJSON(p.Thinking, &s) == nil {
				kb.WriteString(s)
				continue
			}
			var inner []struct {
				Text string `json:"text"`
			}
			DecodeJSON(p.Thinking, &inner)
			for _, in := range inner {
				kb.WriteString(in.Text)
			}
		}
	}
	return tb.String(), kb.String(), true
}

// messageReasoning are the fields a Chat client sends an earlier
// assistant turn's thinking back in: reasoning_content (DeepSeek's, which
// OpenCode and most clients send), reasoning (OpenRouter's) and
// reasoning_details (OpenRouter's structured one). DeepSeek, Kimi and the
// like want it back; Mistral turns each away as extra_forbidden (#494).
var MessageReasoning = []string{"reasoning_content", "reasoning", "reasoning_details"}

// reasoningText is the thinking a message carries in messageReasoning:
// reasoning_content, else reasoning, else reasoning_details' text.
func ReasoningText(m map[string]json.RawMessage) string {
	for _, f := range MessageReasoning[:2] {
		var s string
		if DecodeJSON(m[f], &s) == nil && s != "" {
			return s
		}
	}
	var details []struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
	}
	DecodeJSON(m["reasoning_details"], &details)
	var b strings.Builder
	for _, d := range details {
		if d.Text != "" {
			b.WriteString(d.Text)
		} else {
			b.WriteString(d.Summary)
		}
	}
	return b.String()
}

// noReasoning is the reasoning_content a tool call goes to an upstream
// that wants it back (chatReplaysReasoning) with, when the client has none
// for it: a turn another model made, or one whose thinking the client
// didn't keep. DeepSeek turns the whole request away when a tool call of
// the turn being continued has none ("The `reasoning_content` in the
// thinking mode must be passed back to the API", #1462). Its Chat API
// takes an empty one, but a relay that leaves empty fields out would send
// none, and its Responses API turns an empty reasoning_text away, so it is
// a space.
const NoReasoning = " "
