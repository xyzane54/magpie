package routerrequest

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/cloudcodec"
	"io"
	"slices"
	"strings"
)

// rejectRemoteHistory rejects references whose records are held by a remote
// provider. Keeping a reference in the execution body would make the model
// see an incomplete conversation; silently dropping it would be worse.
func rejectRemoteHistory(protocol string, body []byte) error {
	var root map[string]json.RawMessage
	if err := decodeObject(body, &root); err != nil {
		return normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	for _, key := range []string{"previous_response_id", "conversation", "conversation_id", "history_id"} {
		if raw, ok := root[key]; ok && !isJSONNull(raw) {
			return normalizeCause("remote_history_unsupported", "remote conversation history is not supported", ErrRemoteHistory)
		}
	}
	if protocol == "responses" {
		var items []json.RawMessage
		if raw, ok := root["input"]; ok && json.Unmarshal(raw, &items) == nil {
			for _, item := range items {
				var obj map[string]json.RawMessage
				if json.Unmarshal(item, &obj) != nil {
					continue
				}
				var typ string
				_ = json.Unmarshal(obj["type"], &typ)
				if typ == "item_reference" || typ == "conversation_reference" {
					return normalizeCause("remote_history_unsupported", "remote conversation history is not supported", ErrRemoteHistory)
				}
			}
		}
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func decodeObject(body []byte, out *map[string]json.RawMessage) error {
	d := json.NewDecoder(bytes.NewReader(body))
	if err := d.Decode(out); err != nil || *out == nil {
		return ErrInvalidNormalization
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidNormalization
	}
	return nil
}

func makeJudgment(in Input, body []byte) (JudgmentView, error) {
	view := JudgmentView{RequestID: in.RequestID, AppID: in.AppID, Protocol: in.Protocol, Operation: in.Operation, Model: in.Model, PromptVersion: in.PromptVersion}
	var root map[string]json.RawMessage
	if err := decodeObject(body, &root); err != nil {
		return view, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	if err := validateEnvelope(in.Protocol, root); err != nil {
		return view, err
	}
	switch in.Protocol {
	case "chat":
		return parseChat(view, root)
	case "responses":
		return parseResponses(view, root)
	case "anthropic":
		return parseAnthropic(view, root)
	default:
		return view, normalizeError("invalid_request", "routing protocol is invalid")
	}
}

func validateEnvelope(protocol string, root map[string]json.RawMessage) error {
	allowed := map[string]map[string]bool{
		"chat": {
			"model": true, "messages": true, "tools": true, "tool_choice": true, "max_tokens": true, "max_completion_tokens": true,
			"temperature": true, "top_p": true, "stop": true, "stream": true, "parallel_tool_calls": true, "reasoning_effort": true,
			"service_tier": true, "prompt_cache_key": true, "response_format": true, "metadata": true, "$comment": true, "stream_options": true,
		},
		"responses": {
			"model": true, "instructions": true, "input": true, "tools": true, "tool_choice": true, "max_output_tokens": true, "temperature": true,
			"top_p": true, "stream": true, "parallel_tool_calls": true, "service_tier": true, "prompt_cache_key": true,
			"previous_response_id": true, "client_metadata": true, "metadata": true, "include": true, "store": true, "background": true,
			"reasoning": true, "text": true,
		},
		"anthropic": {
			"model": true, "system": true, "messages": true, "max_tokens": true, "temperature": true, "top_p": true, "stop_sequences": true,
			"stream": true, "tools": true, "tool_choice": true, "thinking": true, "output_config": true, "cache_control": true,
			"metadata": true, "context_management": true,
		},
	}
	if protocol == "chat" {
		for _, name := range cloudcodec.ChatNativeOptionNames() {
			allowed["chat"][name] = true
			if raw, present := root[name]; present && !validChatValue(raw, func(value any) bool { return cloudcodec.ValidChatNativeOption(name, value) }) {
				return normalizeError("unsupported_input", "invalid or unsupported model input")
			}
		}
	}
	for key := range root {
		if !allowed[protocol][key] {
			return normalizeError("unsupported_input", "invalid or unsupported model input")
		}
	}
	return nil
}

func parseChat(view JudgmentView, root map[string]json.RawMessage) (JudgmentView, error) {
	var messages []json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil {
		return view, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	for _, raw := range messages {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if !validChatRole(role) {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		for key, raw := range m {
			if cloudcodec.IsChatHostField(key) {
				if !validChatValue(raw, func(value any) bool { return cloudcodec.ValidChatHostField(role, key, value) }) {
					return view, normalizeError("unsupported_input", "invalid or unsupported model input")
				}
			} else if !slices.Contains([]string{"role", "content", "tool_calls", "tool_call_id", "reasoning_content"}, key) {
				return view, normalizeError("unsupported_input", "invalid or unsupported model input")
			}
		}
		if raw, present := m["content"]; present && !isJSONNull(raw) && !validChatValue(raw, func(value any) bool { return cloudcodec.ValidChatContent(role, value) }) {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		text, err := chatJudgmentText(m["content"], role)
		if err != nil {
			return view, err
		}
		if role == "system" || role == "developer" {
			if text != "" {
				view.System = append(view.System, text)
			}
			continue
		}
		jm := JudgmentMessage{Role: role, Text: text}
		var textBlocks []map[string]json.RawMessage
		if role == "assistant" && json.Unmarshal(m["content"], &textBlocks) == nil {
			for _, block := range textBlocks {
				if string(block["type"]) == `"refusal"` {
					jm.Kind = "refusal"
				}
			}
		}
		if text != "" {
			kind := "text"
			if jm.Kind == "refusal" {
				kind = "refusal"
			}
			jm.Parts = append(jm.Parts, JudgmentPart{Kind: kind, Text: text})
		}
		if role == "assistant" {
			var calls []json.RawMessage
			if raw, ok := m["tool_calls"]; ok && json.Unmarshal(raw, &calls) == nil {
				for _, call := range calls {
					c, err := chatCall(call)
					if err != nil {
						return view, err
					}
					jm.ToolCalls = append(jm.ToolCalls, c)
					cc := c
					jm.Parts = append(jm.Parts, JudgmentPart{Kind: "tool_call", ToolCall: &cc})
				}
			}
		}
		if role == "tool" {
			var id string
			_ = json.Unmarshal(m["tool_call_id"], &id)
			result := JudgmentToolResult{ID: id, Text: text, Content: cloneRaw(m["content"])}
			jm.ToolResult = &result
			jm.ToolResults = append(jm.ToolResults, result)
			jm.Parts = append(jm.Parts, JudgmentPart{Kind: "tool_result", ToolResult: &result})
		}
		_, calls := m["tool_calls"]
		_, toolID := m["tool_call_id"]
		_, content := m["content"]
		_, reasoning := m["reasoning_content"]
		if (role != "assistant" && calls) || (role != "tool" && toolID) || (role == "tool" && !toolID) || (!content && !calls) || (content && isJSONNull(m["content"]) && !calls) || (role != "assistant" && reasoning) {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		view.History = append(view.History, jm)
	}
	var tools []json.RawMessage
	if raw, ok := root["tools"]; ok && json.Unmarshal(raw, &tools) == nil {
		for _, raw := range tools {
			t, err := chatTool(raw)
			if err != nil {
				return view, err
			}
			view.Tools = append(view.Tools, t)
		}
	}
	return view, nil
}

func validChatValue(raw json.RawMessage, check func(any) bool) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	return d.Decode(&value) == nil && check(value)
}

func chatJudgmentText(raw json.RawMessage, role string) (string, error) {
	if role != "assistant" {
		return textValue(raw)
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || blocks == nil {
		return textValue(raw)
	}
	var text strings.Builder
	for _, block := range blocks {
		var kind, value string
		_ = json.Unmarshal(block["type"], &kind)
		key := "text"
		if kind == "refusal" {
			key = "refusal"
		} else if kind != "text" {
			return "", normalizeError("unsupported_input", "non-text content is not supported")
		}
		if json.Unmarshal(block[key], &value) != nil {
			return "", normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		text.WriteString(value)
	}
	return text.String(), nil
}
