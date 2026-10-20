package routerrequest

import (
	"encoding/json"
	"strings"
)

func responseItem(raw json.RawMessage) (JudgmentMessage, error) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	var typ string
	_ = json.Unmarshal(item["type"], &typ)
	switch typ {
	case "", "message":
		if !keysOnly(item, map[string]bool{"type": true, "role": true, "content": true, "id": true, "status": true, "phase": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var role string
		_ = json.Unmarshal(item["role"], &role)
		if role != "system" && role != "developer" && role != "user" && role != "assistant" {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		text, err := textValue(item["content"])
		if err != nil {
			return JudgmentMessage{}, err
		}
		return JudgmentMessage{Role: role, Text: text}, nil
	case "function_call":
		if !keysOnly(item, map[string]bool{"type": true, "call_id": true, "name": true, "arguments": true, "id": true, "status": true, "namespace": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var id, name string
		_ = json.Unmarshal(item["call_id"], &id)
		_ = json.Unmarshal(item["name"], &name)
		if id == "" || name == "" {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		args, ok := argumentObject(item["arguments"])
		if !ok {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		encoded, _ := json.Marshal(args)
		return JudgmentMessage{Role: "assistant", ToolCalls: []JudgmentToolCall{{ID: id, Name: name, Arguments: cloneRaw(encoded)}}}, nil
	case "function_call_output", "custom_tool_call_output":
		if !keysOnly(item, map[string]bool{"type": true, "call_id": true, "output": true, "id": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var id string
		_ = json.Unmarshal(item["call_id"], &id)
		if id == "" {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		text, err := textValue(item["output"])
		if err != nil {
			return JudgmentMessage{}, err
		}
		result := JudgmentToolResult{ID: id, Text: text, Content: cloneRaw(item["output"])}
		return JudgmentMessage{Role: "tool", ToolResult: &result, ToolResults: []JudgmentToolResult{result}, Parts: []JudgmentPart{{Kind: "tool_result", ToolResult: &result}}}, nil
	case "custom_tool_call":
		if !keysOnly(item, map[string]bool{"type": true, "call_id": true, "name": true, "input": true, "id": true, "namespace": true, "status": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var id, name, input string
		_ = json.Unmarshal(item["call_id"], &id)
		_ = json.Unmarshal(item["name"], &name)
		_ = json.Unmarshal(item["input"], &input)
		if id == "" || name == "" {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		return JudgmentMessage{Role: "assistant", ToolCalls: []JudgmentToolCall{{ID: id, Name: name, Arguments: cloneRaw(mustJSON(input))}}}, nil
	case "reasoning":
		if !keysOnly(item, map[string]bool{"type": true, "id": true, "status": true, "encrypted_content": true, "content": true, "summary": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		return JudgmentMessage{Role: "assistant", Kind: typ}, nil
	case "web_search_call":
		if !keysOnly(item, map[string]bool{"type": true, "id": true, "status": true, "action": true}) {
			return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
		}
		var id string
		_ = json.Unmarshal(item["id"], &id)
		return JudgmentMessage{Role: "tool", Kind: typ, ToolResult: &JudgmentToolResult{ID: id}}, nil
	default:
		return JudgmentMessage{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
}

func chatTool(raw json.RawMessage) (ToolSummary, error) {
	var t struct {
		Type     string `json:"type"`
		Function struct {
			Name        string                     `json:"name"`
			Description string                     `json:"description"`
			Parameters  map[string]json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &t) != nil || t.Function.Name == "" {
		return ToolSummary{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	return summary(t.Type, t.Function.Name, t.Function.Description, t.Function.Parameters)
}

func responseTool(raw json.RawMessage) (ToolSummary, error) {
	var t struct {
		Type        string                     `json:"type"`
		Name        string                     `json:"name"`
		Description string                     `json:"description"`
		Parameters  map[string]json.RawMessage `json:"parameters"`
		InputSchema map[string]json.RawMessage `json:"input_schema"`
		Tools       []json.RawMessage          `json:"tools"`
	}
	if json.Unmarshal(raw, &t) != nil || t.Name == "" {
		return ToolSummary{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	if t.Type == "namespace" {
		return ToolSummary{Type: t.Type, Name: t.Name, Description: t.Description}, nil
	}
	params := t.Parameters
	if params == nil {
		params = t.InputSchema
	}
	return summary(t.Type, t.Name, t.Description, params)
}

func anthropicTool(raw json.RawMessage) (ToolSummary, error) {
	var t struct {
		Type        string                     `json:"type"`
		Name        string                     `json:"name"`
		Description string                     `json:"description"`
		InputSchema map[string]json.RawMessage `json:"input_schema"`
	}
	if json.Unmarshal(raw, &t) != nil || t.Name == "" {
		return ToolSummary{}, normalizeError("unsupported_input", "invalid or unsupported model input")
	}
	return summary(t.Type, t.Name, t.Description, t.InputSchema)
}

func summary(typ, name, description string, schema map[string]json.RawMessage) (ToolSummary, error) {
	s := ToolSummary{Type: typ, Name: name, Description: description}
	if schema == nil {
		return s, nil
	}
	var props map[string]json.RawMessage
	_ = json.Unmarshal(schema["properties"], &props)
	var required []string
	_ = json.Unmarshal(schema["required"], &required)
	for name, raw := range props {
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(raw, &obj)
		var typ string
		_ = json.Unmarshal(obj["type"], &typ)
		needed := false
		for _, r := range required {
			if r == name {
				needed = true
				break
			}
		}
		s.Parameters = append(s.Parameters, ToolParameter{Name: name, Type: typ, Required: needed})
	}
	// JSON object iteration is deliberately unordered; stable order keeps the
	// judgment payload/cache key deterministic.
	for i := 1; i < len(s.Parameters); i++ {
		for j := i; j > 0 && s.Parameters[j].Name < s.Parameters[j-1].Name; j-- {
			s.Parameters[j], s.Parameters[j-1] = s.Parameters[j-1], s.Parameters[j]
		}
	}
	return s, nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), raw...) }
func mustJSON(v string) []byte                     { b, _ := json.Marshal(v); return b }

func cloneJudgment(v JudgmentView) JudgmentView {
	v.System = append([]string(nil), v.System...)
	v.SystemSummaries = append([]BoundedTextSummary(nil), v.SystemSummaries...)
	for i := range v.SystemSummaries {
		v.SystemSummaries[i].Excerpts = append([]string(nil), v.SystemSummaries[i].Excerpts...)
	}
	v.History = append([]JudgmentMessage(nil), v.History...)
	for i := range v.History {
		v.History[i].ToolCalls = append([]JudgmentToolCall(nil), v.History[i].ToolCalls...)
		for j := range v.History[i].ToolCalls {
			v.History[i].ToolCalls[j].Arguments = cloneRaw(v.History[i].ToolCalls[j].Arguments)
		}
		if v.History[i].ToolResult != nil {
			r := *v.History[i].ToolResult
			r.Content = cloneRaw(r.Content)
			v.History[i].ToolResult = &r
		}
		v.History[i].ToolResults = append([]JudgmentToolResult(nil), v.History[i].ToolResults...)
		for j := range v.History[i].ToolResults {
			v.History[i].ToolResults[j].Text = strings.Clone(v.History[i].ToolResults[j].Text)
			v.History[i].ToolResults[j].Content = cloneRaw(v.History[i].ToolResults[j].Content)
		}
		v.History[i].Parts = append([]JudgmentPart(nil), v.History[i].Parts...)
		for j := range v.History[i].Parts {
			if v.History[i].Parts[j].ToolCall != nil {
				c := *v.History[i].Parts[j].ToolCall
				c.Arguments = cloneRaw(c.Arguments)
				v.History[i].Parts[j].ToolCall = &c
			}
			if v.History[i].Parts[j].ToolResult != nil {
				r := *v.History[i].Parts[j].ToolResult
				r.Content = cloneRaw(r.Content)
				v.History[i].Parts[j].ToolResult = &r
			}
		}
	}
	v.Tools = append([]ToolSummary(nil), v.Tools...)
	for i := range v.Tools {
		v.Tools[i].Parameters = append([]ToolParameter(nil), v.Tools[i].Parameters...)
		if v.Tools[i].DescriptionSummary != nil {
			summary := *v.Tools[i].DescriptionSummary
			summary.Excerpts = append([]string(nil), summary.Excerpts...)
			v.Tools[i].DescriptionSummary = &summary
		}
	}
	return v
}
