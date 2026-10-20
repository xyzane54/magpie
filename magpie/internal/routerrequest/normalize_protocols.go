package routerrequest

import (
	"bytes"
	"encoding/json"
	"strings"
)

func validChatRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func keysOnly(m map[string]json.RawMessage, allowed map[string]bool) bool {
	for key := range m {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func parseResponses(view JudgmentView, root map[string]json.RawMessage) (JudgmentView, error) {
	if raw, ok := root["instructions"]; ok {
		text, err := textValue(raw)
		if err != nil {
			return view, err
		}
		if text != "" {
			view.System = append(view.System, text)
		}
	}
	var input string
	if raw, ok := root["input"]; ok && json.Unmarshal(raw, &input) == nil {
		view.History = append(view.History, JudgmentMessage{Role: "user", Text: input})
	} else if raw, ok := root["input"]; ok {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		for _, item := range items {
			jm, err := responseItem(item)
			if err != nil {
				return view, err
			}
			if jm.Role == "system" {
				if jm.Text != "" {
					view.System = append(view.System, jm.Text)
				}
				continue
			}
			view.History = append(view.History, jm)
		}
	}
	var tools []json.RawMessage
	if raw, ok := root["tools"]; ok && json.Unmarshal(raw, &tools) == nil {
		for _, raw := range tools {
			t, err := responseTool(raw)
			if err != nil {
				return view, err
			}
			view.Tools = append(view.Tools, t)
		}
	}
	return view, nil
}

func parseAnthropic(view JudgmentView, root map[string]json.RawMessage) (JudgmentView, error) {
	if raw, ok := root["system"]; ok {
		text, err := textValue(raw)
		if err != nil {
			return view, err
		}
		if text != "" {
			view.System = append(view.System, text)
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal(root["messages"], &messages) != nil {
		return view, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	for _, raw := range messages {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role != "user" && role != "assistant" || !keysOnly(m, map[string]bool{"role": true, "content": true, "output_config": true}) {
			return view, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var blocks []json.RawMessage
		if json.Unmarshal(m["content"], &blocks) != nil {
			text, err := textValue(m["content"])
			if err != nil {
				return view, err
			}
			block := append([]byte(`{"type":"text","text":`), mustJSON(text)...)
			block = append(block, '}')
			blocks = []json.RawMessage{json.RawMessage(block)}
		}
		jm := JudgmentMessage{Role: role}
		for _, block := range blocks {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				return view, normalizeError("unsupported_input", "invalid or unsupported model input")
			}
			var typ string
			_ = json.Unmarshal(b["type"], &typ)
			switch typ {
			case "text":
				if !keysOnly(b, map[string]bool{"type": true, "text": true, "cache_control": true}) {
					return view, normalizeError("unsupported_input", "invalid or unsupported model input")
				}
				text, err := textValue(b["text"])
				if err != nil {
					return view, err
				}
				jm.Text += text
				if text != "" {
					jm.Parts = append(jm.Parts, JudgmentPart{Kind: "text", Text: text})
				}
			case "tool_use":
				if !keysOnly(b, map[string]bool{"type": true, "id": true, "name": true, "input": true, "cache_control": true}) {
					return view, normalizeError("unsupported_input", "invalid or unsupported model input")
				}
				var id, name string
				_ = json.Unmarshal(b["id"], &id)
				_ = json.Unmarshal(b["name"], &name)
				call := JudgmentToolCall{ID: id, Name: name, Arguments: cloneRaw(b["input"])}
				jm.ToolCalls = append(jm.ToolCalls, call)
				cc := call
				jm.Parts = append(jm.Parts, JudgmentPart{Kind: "tool_call", ToolCall: &cc})
			case "tool_result":
				if !keysOnly(b, map[string]bool{"type": true, "tool_use_id": true, "content": true, "is_error": true, "cache_control": true}) {
					return view, normalizeError("unsupported_input", "invalid or unsupported model input")
				}
				var id string
				_ = json.Unmarshal(b["tool_use_id"], &id)
				text, err := textValue(b["content"])
				if err != nil {
					return view, err
				}
				var isError bool
				_ = json.Unmarshal(b["is_error"], &isError)
				result := JudgmentToolResult{ID: id, Text: text, Content: cloneRaw(b["content"]), IsError: isError}
				if jm.ToolResult == nil {
					first := result
					jm.ToolResult = &first
				}
				jm.ToolResults = append(jm.ToolResults, result)
				rr := result
				jm.Parts = append(jm.Parts, JudgmentPart{Kind: "tool_result", ToolResult: &rr})
			case "thinking", "redacted_thinking":
				if typ == "thinking" && !keysOnly(b, map[string]bool{"type": true, "thinking": true, "signature": true}) || typ == "redacted_thinking" && !keysOnly(b, map[string]bool{"type": true, "data": true}) {
					return view, normalizeError("unsupported_input", "invalid or unsupported model input")
				}
				jm.Kind = typ
				jm.Parts = append(jm.Parts, JudgmentPart{Kind: typ})
			default:
				return view, normalizeError("unsupported_input", "invalid or unsupported model input")
			}
		}
		if role == "assistant" || role == "user" {
			view.History = append(view.History, jm)
		}
	}
	var tools []json.RawMessage
	if raw, ok := root["tools"]; ok && json.Unmarshal(raw, &tools) == nil {
		for _, raw := range tools {
			t, err := anthropicTool(raw)
			if err != nil {
				return view, err
			}
			view.Tools = append(view.Tools, t)
		}
	}
	return view, nil
}

func textValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	var out strings.Builder
	for _, block := range blocks {
		var typ string
		_ = json.Unmarshal(block["type"], &typ)
		if typ != "text" && typ != "input_text" && typ != "output_text" {
			return "", normalizeError("unsupported_input", "non-text content is not supported")
		}
		var text string
		if json.Unmarshal(block["text"], &text) != nil {
			return "", normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		out.WriteString(text)
	}
	return out.String(), nil
}

func chatCall(raw json.RawMessage) (JudgmentToolCall, error) {
	var c struct {
		ID       string `json:"id"`
		Function struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.Function.Name == "" {
		return JudgmentToolCall{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	args, ok := argumentObject(c.Function.Args)
	if !ok {
		return JudgmentToolCall{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	encoded, _ := json.Marshal(args)
	return JudgmentToolCall{ID: c.ID, Name: c.Function.Name, Arguments: cloneRaw(encoded)}, nil
}

func argumentObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, false
	}
	if !strictJSON([]byte(encoded)) {
		return nil, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(encoded), &args) != nil || args == nil {
		return nil, false
	}
	return args, true
}
