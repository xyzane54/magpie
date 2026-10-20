package routercloud

import "github.com/yetone/magpie/internal/cloudcodec"

func chatInput(v any) bool {
	fields := map[string]inputCheck{
		"model": inputName, "messages": inputArray(chatMessage, true),
		"tools": inputArray(chatTool, false), "tool_choice": chatChoice,
		"max_tokens": inputPositiveInt, "max_completion_tokens": inputPositiveInt,
		"temperature": inputNumber, "top_p": inputNumber, "stop": inputStop,
		"stream": inputBool, "parallel_tool_calls": inputBool,
		"reasoning_effort": inputEffort, "service_tier": inputName,
		"prompt_cache_key": inputString, "response_format": chatFormat,
		"metadata": inputStringMap,
		// Installed Loomy Agent options include opaque string request metadata.
		// Native Chat preserves it; conversion requires an explicit boundary.
		"$comment": inputString,
		"stream_options": func(v any) bool {
			return inputObject(v, map[string]inputCheck{"include_usage": inputBool}, "include_usage")
		},
	}
	for _, name := range cloudcodec.ChatNativeOptionNames() {
		fields[name] = func(value any) bool { return cloudcodec.ValidChatNativeOption(name, value) }
	}
	return inputObject(v, fields, "messages")
}

func chatMessage(v any) bool {
	message, ok := v.(map[string]any)
	if !ok {
		return false
	}
	role, _ := message["role"].(string)
	fields := map[string]inputCheck{
		"role":       inputEnum("system", "developer", "user", "assistant", "tool"),
		"content":    func(v any) bool { return v == nil || chatText(v, role == "assistant") },
		"tool_calls": inputArray(chatCall, true), "tool_call_id": inputName,
		"reasoning_content": inputString,
	}
	for _, name := range cloudcodec.ChatHostFieldNames() {
		fields[name] = func(value any) bool { return cloudcodec.ValidChatHostField(role, name, value) }
	}
	if !inputObject(v, fields, "role") {
		return false
	}
	_, calls := message["tool_calls"]
	_, id := message["tool_call_id"]
	_, think := message["reasoning_content"]
	if role != "assistant" && (calls || think) || role != "tool" && id {
		return false
	}
	if role == "tool" && !id {
		return false
	}
	content, exists := message["content"]
	return (exists && content != nil) || (role == "assistant" && calls)
}

func chatText(value any, assistant bool) bool {
	role := "user"
	if assistant {
		role = "assistant"
	}
	return cloudcodec.ValidChatContent(role, value)
}
func chatCall(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"id": inputName, "type": inputEnum("function"),
		"function": func(v any) bool {
			return inputObject(v, map[string]inputCheck{"name": inputName, "arguments": inputArgs}, "name", "arguments")
		},
	}, "id", "type", "function")
}
func chatTool(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"type": inputEnum("function"), "function": func(v any) bool {
			return inputObject(v, map[string]inputCheck{
				"name": inputName, "description": inputString, "parameters": inputMap, "strict": inputBool,
			}, "name")
		},
	}, "type", "function")
}
func chatChoice(v any) bool {
	return inputEnum("auto", "none", "required")(v) || inputObject(v, map[string]inputCheck{
		"type": inputEnum("function"), "function": func(v any) bool {
			return inputObject(v, map[string]inputCheck{"name": inputName}, "name")
		},
	}, "type", "function")
}

func chatFormat(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if o["type"] == "json_schema" {
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("json_schema"), "json_schema": func(v any) bool {
				return inputObject(v, map[string]inputCheck{
					"name": inputName, "description": inputString, "schema": inputMap, "strict": inputBool,
				}, "name", "schema")
			},
		}, "type", "json_schema")
	}
	return inputObject(v, map[string]inputCheck{"type": inputEnum("text", "json_object")}, "type")
}

func responsesInput(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"model": inputName, "instructions": inputString, "input": responseItems,
		"tools": inputArray(responseTool, false), "tool_choice": responseChoice,
		"max_output_tokens": inputPositiveInt, "temperature": inputNumber, "top_p": inputNumber,
		"stream": inputBool, "parallel_tool_calls": inputBool,
		"service_tier": inputName, "prompt_cache_key": inputString,
		// Codex encodes even structured turn metadata as string values. Keep
		// those strings opaque, but reject new value shapes until their wire
		// contract is checked rather than accepting arbitrary nested fields.
		"previous_response_id": inputNull, "client_metadata": inputStringMap,
		"metadata": inputStringMap,
		"include":  inputArray(inputEnum("reasoning.encrypted_content", "web_search_call.action.sources"), false),
		// False explicitly requests the stateless, foreground behavior.
		"store": inputFalse, "background": inputFalse,
		"reasoning": func(v any) bool {
			return inputObject(v, map[string]inputCheck{
				"effort":  inputEffort,
				"summary": func(v any) bool { return v == nil || inputEnum("auto", "concise", "detailed")(v) },
				"context": inputEnum("all_turns", "current_turn"), "mode": inputEnum("standard"),
			})
		},
		"text": func(v any) bool {
			return inputObject(v, map[string]inputCheck{
				"format": responseFormat, "verbosity": inputEnum("low", "medium", "high"),
			})
		},
	}, "input")
}
func responseItems(v any) bool {
	return inputString(v) || inputArray(responseItem, true)(v)
}
func responseItem(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch o["type"] {
	case nil, "message":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("message"), "role": inputEnum("system", "developer", "user", "assistant"),
			"content": responseText, "id": inputName, "status": responseItemStatus,
			"phase": func(v any) bool { return v == nil || inputEnum("commentary", "final_answer")(v) },
		}, "role", "content") && (o["role"] == "assistant" || !responseHasField(o, "phase"))
	case "function_call":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("function_call"), "call_id": inputName, "name": inputName, "arguments": inputArgs,
			"id": inputName, "status": responseItemStatus, "namespace": inputName,
		}, "type", "call_id", "name", "arguments")
	case "function_call_output":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("function_call_output"), "call_id": inputName, "output": responseText,
			"id": inputName,
		}, "type", "call_id", "output")
	case "reasoning":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("reasoning"), "id": inputName, "status": responseItemStatus,
			"encrypted_content": func(v any) bool { return v == nil || inputString(v) },
			"content":           responseReasoningContent,
			"summary": inputArray(func(v any) bool {
				return inputObject(v, map[string]inputCheck{"type": inputEnum("summary_text"), "text": inputString}, "type", "text")
			}, false),
		}, "type", "summary")
	case "additional_tools":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("additional_tools"), "id": func(v any) bool { return v == nil || inputName(v) },
			"tools": inputArray(responseTool, false), "role": inputEnum("developer"),
		}, "type", "tools")
	case "custom_tool_call":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("custom_tool_call"), "call_id": inputName, "name": inputName, "input": inputString,
			"id": inputName, "namespace": inputName, "status": responseItemStatus,
		}, "type", "call_id", "name", "input")
	case "custom_tool_call_output":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("custom_tool_call_output"), "call_id": inputName, "output": responseText, "id": inputName,
		}, "type", "call_id", "output")
	case "web_search_call":
		return responseWebSearchCall(v)
	default:
		return false
	}
}
func responseText(v any) bool {
	return inputString(v) || inputArray(func(v any) bool {
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("input_text", "output_text"), "text": inputString,
			"annotations": inputArray(responseAnnotation, false), "logprobs": inputEmptyArray,
		}, "type", "text")
	}, true)(v)
}
func responseFormat(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if o["type"] == "json_schema" {
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("json_schema"), "name": inputName, "description": inputString,
			"schema": inputMap, "strict": inputBool,
		}, "type", "name", "schema")
	}
	return inputObject(v, map[string]inputCheck{"type": inputEnum("text", "json_object")}, "type")
}
