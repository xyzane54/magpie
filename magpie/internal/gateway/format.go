package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// formatRefused is how unfit remembers a provider turning away the format
// field for a model (a relay or model without structured output: DeepSeek's
// "This response_format type is unavailable now"), so it is told the
// format in the system prompt from then on.
func formatRefused(model string) string { return "response_format:" + model }

// formatRefusal is an upstream's 400 naming the field structured output is
// asked for in, in any API's words.
var formatRefusal = regexp.MustCompile(`(?i)response_format|json_schema|json_object|output_config\.format|text\.format|structured.output|response_?mime_?type|response_?json_?schema|response_?schema`)

// refusesFormat is whether an upstream's answer turned the request away
// over its format.
func refusesFormat(status int, body []byte) bool {
	return (status == 400 || status == 422) && formatRefusal.Match(body)
}

// schemaRefused is how unfit remembers a provider turning away a schema
// while it takes any JSON object (Xiaomi MiMo's Token Plan: "text.format
// type 'json_schema' is not supported, only 'text' and 'json_object' are
// allowed", #2126), so it is asked for a JSON object, the schema told in
// the system prompt, from then on.
func schemaRefused(model string) string { return "json_schema:" + model }

// bodyFormat is the format a body passed through as the client sent it
// asks for, nil when it asks for none: Responses' text.format, Chat's
// response_format, Anthropic's output_config.format.
func bodyFormat(proto provider.Protocol, body []byte) *Format {
	switch proto {
	case provider.Responses:
		return openAIFormat(json.RawMessage(gjson.GetBytes(body, "text.format").Raw))
	case provider.Chat:
		return openAIFormat(json.RawMessage(gjson.GetBytes(body, "response_format").Raw))
	case provider.Anthropic:
		if f := gjson.GetBytes(body, "output_config.format"); f.Get("type").String() == "json_schema" && f.Get("schema").IsObject() {
			return &Format{Type: "json_schema", Name: formatName, Schema: json.RawMessage(f.Get("schema").Raw)}
		}
	}
	return nil
}

// formatInBody is a passed-through body asking for its format less, the
// rest told in the system prompt: with object, a schema as any JSON object
// (asObject); without, no format field at all (inSystem). Anthropic's
// output_config.format takes a schema only, so it is never asked for an
// object. The body and false when there is nothing to change.
func formatInBody(proto provider.Protocol, body []byte, object bool) ([]byte, bool) {
	f := bodyFormat(proto, body)
	if f == nil || object && (proto == provider.Anthropic || f.Type != "json_schema") {
		return body, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body, false
	}
	line := f.Instruction()
	switch proto {
	case provider.Responses:
		text, _ := m["text"].(map[string]any)
		if text == nil {
			return body, false
		}
		if object {
			text["format"] = map[string]any{"type": "json_object"}
		} else if delete(text, "format"); len(text) == 0 {
			delete(m, "text")
		}
		s, _ := m["instructions"].(string)
		m["instructions"] = withLine(s, line)
	case provider.Chat:
		if object {
			m["response_format"] = map[string]any{"type": "json_object"}
		} else {
			delete(m, "response_format")
		}
		m["messages"] = withSystemLine(m["messages"], line)
	case provider.Anthropic:
		oc, _ := m["output_config"].(map[string]any)
		if delete(oc, "format"); len(oc) == 0 {
			delete(m, "output_config")
		}
		switch s := m["system"].(type) {
		case []any:
			m["system"] = append(s, map[string]any{"type": "text", "text": line})
		case string:
			m["system"] = withLine(s, line)
		default:
			m["system"] = line
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(m) != nil {
		return body, false
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), true
}

// withSystemLine is a chat's messages with s said at the end of its first
// system (or developer) message, or in one of its own ahead of the rest
// when it has none.
func withSystemLine(v any, s string) []any {
	msgs, _ := v.([]any)
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "system" && msg["role"] != "developer" {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			msg["content"] = withLine(c, s)
			return msgs
		case []any:
			msg["content"] = append(c, map[string]any{"type": "text", "text": s})
			return msgs
		}
	}
	return append([]any{map[string]any{"role": "system", "content": s}}, msgs...)
}
