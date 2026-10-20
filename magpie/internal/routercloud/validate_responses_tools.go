package routercloud

import (
	"encoding/json"
	"strconv"
)

func responseHasField(o map[string]any, key string) bool {
	_, exists := o[key]
	return exists
}

func responseItemStatus(v any) bool {
	return inputEnum("in_progress", "completed", "incomplete")(v)
}

func responseReasoningContent(v any) bool {
	return v == nil || inputArray(func(v any) bool {
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("reasoning_text"), "text": inputString,
		}, "type", "text")
	}, false)(v)
}

// Namespace members are function/custom declarations, never recursive
// namespaces or hosted tools. Their names and freeform inputs stay opaque.
func responseTool(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch o["type"] {
	case "namespace":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("namespace"), "name": inputName, "description": inputString,
			"tools": inputArray(responseClientTool, false),
		}, "type", "name", "tools")
	case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26":
		return responseWebSearchTool(v)
	default:
		return responseClientTool(v)
	}
}

func responseClientTool(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch o["type"] {
	case "function":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("function"), "name": inputName, "description": inputString,
			"parameters": inputMap, "strict": inputBool,
		}, "type", "name")
	case "custom":
		return inputObject(v, map[string]inputCheck{
			"type": inputEnum("custom"), "name": inputName, "description": inputString,
			"format": func(v any) bool {
				return inputObject(v, map[string]inputCheck{"type": inputEnum("text")}, "type") ||
					inputObject(v, map[string]inputCheck{
						"type": inputEnum("grammar"), "syntax": inputEnum("lark", "regex"), "definition": inputString,
					}, "type", "syntax", "definition")
			},
		}, "type", "name")
	default:
		return false
	}
}

func responseChoice(v any) bool {
	return inputEnum("auto", "none", "required")(v) || inputObject(v, map[string]inputCheck{
		"type": inputEnum("function", "custom"), "name": inputName, "namespace": inputName,
	}, "type", "name") || inputObject(v, map[string]inputCheck{
		"type": inputEnum("web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26"),
	}, "type")
}

func responseWebSearchTool(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if o["type"] == "web_search_preview" || o["type"] == "web_search_preview_2025_03_11" {
		// Preview lacks these full search controls; accepting them would let
		// an upstream silently ignore restrictions such as offline access.
		for _, key := range []string{"filters", "return_token_budget", "external_web_access"} {
			if responseHasField(o, key) {
				return false
			}
		}
	}
	return inputObject(v, map[string]inputCheck{
		"type":                inputEnum("web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26"),
		"external_web_access": inputBool, "search_context_size": inputEnum("low", "medium", "high"),
		"return_token_budget": inputEnum("default", "unlimited"),
		"filters": func(v any) bool {
			return v == nil || inputObject(v, map[string]inputCheck{
				"allowed_domains": inputStrings, "blocked_domains": inputStrings,
			})
		},
		"user_location": func(v any) bool {
			return v == nil || inputObject(v, map[string]inputCheck{
				"type": inputEnum("approximate"), "city": inputString, "country": inputString,
				"region": inputString, "timezone": inputString,
			}, "type")
		},
	}, "type")
}

func responseWebSearchCall(v any) bool {
	return inputObject(v, map[string]inputCheck{
		"type": inputEnum("web_search_call"), "id": inputName,
		"status": inputEnum("in_progress", "searching", "completed", "failed"),
		"action": func(v any) bool {
			return inputObject(v, map[string]inputCheck{
				"type": inputEnum("search"), "query": inputString, "queries": inputStrings,
				"sources": inputArray(func(v any) bool {
					return inputObject(v, map[string]inputCheck{
						"type": inputEnum("url"), "url": inputString,
					}, "type", "url")
				}, false),
			}, "type") || inputObject(v, map[string]inputCheck{
				"type": inputEnum("open_page"), "url": inputString,
			}, "type", "url") || inputObject(v, map[string]inputCheck{
				"type": inputEnum("find", "find_in_page"), "url": inputString, "pattern": inputString,
			}, "type", "url", "pattern")
		},
	}, "type", "id", "status", "action")
}

func responseAnnotation(v any) bool {
	if !inputObject(v, map[string]inputCheck{
		"type": inputEnum("url_citation"), "url": inputString, "title": inputString,
		"start_index": responseNonnegativeInt, "end_index": responseNonnegativeInt,
	}, "type", "url", "title", "start_index", "end_index") {
		return false
	}
	o := v.(map[string]any)
	start, _ := strconv.ParseInt(string(o["start_index"].(json.Number)), 10, 64)
	end, _ := strconv.ParseInt(string(o["end_index"].(json.Number)), 10, 64)
	return end >= start
}

func responseNonnegativeInt(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil && i >= 0
}
