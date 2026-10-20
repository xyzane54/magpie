package cloudcodec

import "encoding/json"

// CapabilityError names a wire capability, never request data or target details.
// Callers may expose Code and Message without reflecting private input.
type CapabilityError struct {
	Code    string
	Message string
}

func (e *CapabilityError) Error() string { return e.Message }

func capabilityError(feature string) *CapabilityError {
	return &CapabilityError{
		Code:    "capability_" + feature,
		Message: "the configured Profile protocol cannot preserve " + feature + "; select a compatible target using the request's native protocol",
	}
}

// ResponseCapabilities inspects only Responses protocol containers. Tool
// schemas, arguments, outputs and text are data, not capability declarations.
// Its stable order makes errors deterministic when several features occur.
func ResponseCapabilities(body []byte) []string {
	var request Obj
	json.Unmarshal(body, &request)
	used := map[string]bool{}
	var visitTools func(json.RawMessage)
	visitTools = func(raw json.RawMessage) {
		var tools []Obj
		json.Unmarshal(raw, &tools)
		for _, tool := range tools {
			switch Str(tool, "type") {
			case "namespace":
				used["namespace"] = true
				visitTools(tool["tools"])
			case "custom":
				used["custom"] = true
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26":
				used["web_search"] = true
			}
		}
	}
	visitTools(request["tools"])
	var choice Obj
	json.Unmarshal(request["tool_choice"], &choice)
	switch Str(choice, "type") {
	case "custom":
		used["custom"] = true
	case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26":
		used["web_search"] = true
	}
	if Str(choice, "namespace") != "" {
		used["namespace"] = true
	}
	var items []Obj
	json.Unmarshal(request["input"], &items)
	for _, item := range items {
		switch Str(item, "type") {
		case "additional_tools":
			used["additional_tools"] = true
			visitTools(item["tools"])
		case "custom_tool_call", "custom_tool_call_output":
			used["custom"] = true
		case "web_search_call":
			used["web_search"] = true
		}
		if Str(item, "namespace") != "" {
			used["namespace"] = true
		}
		for _, field := range []string{"content", "output"} {
			var parts []Obj
			json.Unmarshal(item[field], &parts)
			for _, part := range parts {
				var annotations []Obj
				json.Unmarshal(part["annotations"], &annotations)
				for _, annotation := range annotations {
					if Str(annotation, "type") == "url_citation" {
						used["web_search"] = true
					}
				}
			}
		}
	}
	var result []string
	for _, feature := range []string{"namespace", "custom", "additional_tools", "web_search"} {
		if used[feature] {
			result = append(result, feature)
		}
	}
	return result
}

// ValidateCapabilities binds enhanced Responses tools and their history to an
// explicit Profile capability allowlist. It performs no upstream discovery.
func ValidateCapabilities(protocol string, body []byte, allowed []string) error {
	if protocol != string(Responses) {
		return nil
	}
	granted := map[string]bool{}
	for _, feature := range allowed {
		granted[feature] = true
	}
	for _, feature := range ResponseCapabilities(body) {
		if !granted[feature] {
			return &CapabilityError{Code: "capability_" + feature, Message: "the configured Profile has not enabled " + feature + "; bind a target known to support this native Responses capability and enable it in the Profile"}
		}
	}
	return nil
}

func (c *CloudCodec) translationCapability() error {
	var input Obj
	json.Unmarshal(c.body, &input)
	if c.protocol == Chat {
		var messages []Obj
		json.Unmarshal(input["messages"], &messages)
		for _, message := range messages {
			// WorkBuddy's Agent.name is message identity metadata. The IR has
			// no equivalent; only native Chat can preserve its exact meaning.
			if _, present := message["agent"]; present {
				return capabilityError("message_agent")
			}
			if _, present := message["reasoning"]; present {
				return capabilityError("reasoning_history")
			}
			if _, present := message["reasoning_details"]; present {
				return capabilityError("reasoning_history")
			}
			for _, field := range ChatHostFieldNames() {
				if _, present := message[field]; present {
					return capabilityError("message_metadata")
				}
			}
			var parts []Obj
			json.Unmarshal(message["content"], &parts)
			for _, part := range parts {
				if Str(part, "type") == "refusal" {
					return capabilityError("content_refusal")
				}
				if _, present := part["extra_fields"]; present {
					return capabilityError("content_metadata")
				}
				if _, present := part["annotations"]; present {
					return capabilityError("content_annotations")
				}
			}
		}
		for _, field := range ChatNativeOptionNames() {
			if _, present := input[field]; present {
				return capabilityError("request_options")
			}
		}
	}
	if _, present := input["$comment"]; present {
		return capabilityError("request_comment")
	}
	if c.protocol == Responses {
		if features := ResponseCapabilities(c.body); len(features) > 0 {
			return capabilityError(features[0])
		}
		var items []Obj
		json.Unmarshal(input["input"], &items)
		for _, item := range items {
			if Str(item, "type") == "reasoning" {
				return capabilityError("reasoning_history")
			}
			if _, present := item["phase"]; present {
				return capabilityError("message_phase")
			}
			// Chat's tool_call_id represents the call relationship, not the
			// independent Responses item ID or item lifecycle status.
			for _, key := range []string{"id", "status"} {
				if _, present := item[key]; present {
					return capabilityError("item_identity")
				}
			}
		}
	}
	for _, key := range []string{"reasoning_effort", "thinking", "reasoning"} {
		if _, present := input[key]; present {
			return capabilityError("reasoning_configuration")
		}
	}
	for _, key := range []string{"client_metadata", "metadata", "include", "context_management", "cache_control"} {
		if _, present := input[key]; present {
			return capabilityError("metadata")
		}
	}
	return nil
}
