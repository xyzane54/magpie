package routercloud

func anthropicInput(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"model": inputName, "system": anthropicText,
		"messages": inputArray(anthropicMessage, true), "max_tokens": inputPositiveInt,
		"temperature": inputNumber, "top_p": inputNumber, "stop_sequences": inputStrings,
		"stream": inputBool, "tools": inputArray(anthropicTool, false), "tool_choice": anthropicChoice,
		"thinking":      anthropicThinking,
		"output_config": anthropicOutputConfig,
		"cache_control": anthropicCache,
		"metadata": func(v any) bool {
			return inputObject(v, map[string]inputCheck{"user_id": inputString})
		},
		"context_management": func(v any) bool {
			return inputObject(v, map[string]inputCheck{
				"edits": inputArray(func(v any) bool {
					return inputObject(v, map[string]inputCheck{
						"type": inputEnum("clear_thinking_20251015"), "keep": inputEnum("all"),
					}, "type", "keep")
				}, false),
			}, "edits")
		},
	}, "messages")
}
func anthropicOutputConfig(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"effort": inputEffort,
		"format": func(v any) bool {
			return inputObject(v, map[string]inputCheck{"type": inputEnum("json_schema"), "schema": inputMap}, "type", "schema")
		},
	})
}
func anthropicCache(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"type": inputEnum("ephemeral"), "ttl": inputEnum("5m", "1h"),
	}, "type")
}
func anthropicThinking(v any) bool {
	if !inputObject(v, map[string]inputCheck{
		"type": inputEnum("enabled", "adaptive", "disabled", "between_tools"), "budget_tokens": inputPositiveInt,
		"display": inputEnum("omitted", "summarized", "updates"),
	}, "type") {
		return false
	}
	o := v.(map[string]any)
	_, budget := o["budget_tokens"]
	return (o["type"] == "enabled") == budget
}
func anthropicMessage(v any) bool {
	if !inputObject(v, map[string]inputCheck{
		"role":          inputEnum("user", "assistant", "system"),
		"content":       func(v any) bool { return inputString(v) || inputArray(anthropicBlock, true)(v) },
		"output_config": anthropicOutputConfig,
	}, "role", "content") {
		return false
	}
	o := v.(map[string]any)
	if blocks, ok := o["content"].([]any); ok {
		for _, v := range blocks {
			block := v.(map[string]any)
			if (block["type"] == "tool_use" || block["type"] == "thinking" || block["type"] == "redacted_thinking") && o["role"] != "assistant" || block["type"] == "tool_result" && o["role"] != "user" {
				return false
			}
		}
	}
	return true
}
func anthropicText(v any) bool {
	return inputString(v) || inputArray(func(v any) bool {
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("text"), "text": inputString, "cache_control": anthropicCache,
		}, "type", "text")
	}, true)(v)
}
func anthropicBlock(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch o["type"] {
	case "text":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("text"), "text": inputString, "cache_control": anthropicCache,
		}, "type", "text")
	case "tool_use":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("tool_use"), "id": inputName, "name": inputName, "input": inputMap,
			"cache_control": anthropicCache,
		}, "type", "id", "name", "input")
	case "tool_result":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("tool_result"), "tool_use_id": inputName,
			"content": anthropicText, "is_error": inputBool,
			"cache_control": anthropicCache,
		}, "type", "tool_use_id", "content")
	case "thinking":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("thinking"), "thinking": inputString, "signature": inputName,
		}, "type", "thinking", "signature")
	case "redacted_thinking":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("redacted_thinking"), "data": inputName,
		}, "type", "data")
	default:
		return false
	}
}
func anthropicTool(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"type": inputEnum("custom"), "name": inputName, "description": inputString, "input_schema": inputMap,
		"cache_control": anthropicCache,
	}, "name", "input_schema")
}
func anthropicChoice(v any) bool {
	if !inputObject(v, map[string]inputCheck{
		"type": inputEnum("auto", "none", "any", "tool"), "name": inputName, "disable_parallel_tool_use": inputBool,
	}, "type") {
		return false
	}
	o := v.(map[string]any)
	_, name := o["name"]
	return (o["type"] == "tool") == name
}
