package cloudcodec

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// This contract describes metadata emitted by the installed WorkBuddy text/tool
// serializers. Validation, routing, conversion and privacy extraction share it
// so that accepting a host field at one boundary cannot leave another behind.
// Object contents are opaque host/business data, never Router instructions.
type hostField struct {
	roles string
	kind  string
}

var chatHostFields = map[string]hostField{
	"agent":                    {"system|user|assistant|tool", "name"},
	"messageId":                {"assistant|tool", "string"},
	"model":                    {"assistant|tool", "string"},
	"requestModelId":           {"assistant|tool", "string"},
	"requestModelName":         {"assistant|tool", "string"},
	"traceId":                  {"assistant|tool", "string"},
	"conversationRequestId":    {"assistant|tool", "string"},
	"reasoning":                {"assistant", "text"},
	"reasoning_details":        {"assistant", "text"},
	"argumentsDisplayText":     {"assistant", "text"},
	"usage":                    {"assistant", "usage"},
	"rawUsage":                 {"assistant", "object"},
	"toolResult":               {"assistant|tool", "object"},
	"extra_fields":             {"assistant", "object"},
	"skipRun":                  {"system|user|assistant|tool", "bool"},
	"truncated":                {"system|user|assistant|tool", "bool"},
	"isMeta":                   {"user", "bool"},
	"queueHold":                {"user", "bool"},
	"isCompactInternal":        {"user", "bool"},
	"isCompacted":              {"user", "bool"},
	"isSummary":                {"user", "bool"},
	"compactType":              {"user", "string"},
	"isPtlRecovery":            {"user", "bool"},
	"isMediaBodyRecovery":      {"user", "bool"},
	"isPtlRetryMarker":         {"user", "bool"},
	"teammateMessage":          {"user", "object"},
	"manual":                   {"assistant", "bool"},
	"isSubAgent":               {"assistant|tool", "bool"},
	"isTeamMember":             {"assistant|tool", "bool"},
	"memberName":               {"assistant|tool", "string"},
	"agentColor":               {"assistant|tool", "string"},
	"queuePosition":            {"assistant|tool", "count"},
	"queueTotal":               {"assistant|tool", "count"},
	"_enqueuedViaQueue":        {"assistant|tool", "bool"},
	"permissionDecisionReason": {"assistant|tool", "string"},
	"hookEvent":                {"assistant", "string"},
	"hookStatus":               {"assistant", "string"},
	"callId":                   {"assistant", "string"},
	"exitCode":                 {"assistant", "integer"},
	"error":                    {"assistant|tool", "object"},
	"isPartialAborted":         {"tool", "bool"},
	"isSessionSeparator":       {"system", "bool"},
	"separatorMessageId":       {"system", "string"},
	"separatorExtra":           {"system", "object"},
}

func ChatHostFieldNames() []string {
	names := make([]string, 0, len(chatHostFields))
	for name := range chatHostFields {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func IsChatHostField(name string) bool { _, ok := chatHostFields[name]; return ok }

func ChatHostTextField(name string) bool { return chatHostFields[name].kind == "text" }

// Chat text is shared with the routing parser as well. Assistant refusal parts
// and opaque vendor text metadata are retained natively; media remains outside
// this text/tool contract.
func ValidChatContent(role string, value any) bool {
	if _, ok := value.(string); ok {
		return true
	}
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		block, ok := part.(map[string]any)
		if !ok {
			return false
		}
		kind, _ := block["type"].(string)
		key := "text"
		if kind == "refusal" && role == "assistant" {
			key = "refusal"
		} else if kind != "text" {
			return false
		}
		if _, ok := block[key].(string); !ok {
			return false
		}
		for field, value := range block {
			switch field {
			case "type", key:
			case "annotations":
				annotations, ok := value.([]any)
				if role != "assistant" || kind != "text" || !ok || len(annotations) != 0 {
					return false
				}
			case "extra_fields":
				if _, ok := value.(map[string]any); role != "assistant" || kind != "text" || !ok {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func ValidChatHostField(role, name string, value any) bool {
	field, ok := chatHostFields[name]
	if !ok || !slices.Contains(strings.Split(field.roles, "|"), role) {
		return false
	}
	switch field.kind {
	case "string", "text":
		_, ok = value.(string)
	case "name":
		s, stringOK := value.(string)
		ok = stringOK && strings.TrimSpace(s) != ""
	case "bool":
		_, ok = value.(bool)
	case "object":
		_, ok = value.(map[string]any)
	case "integer", "count":
		n, numberOK := value.(json.Number)
		i, err := strconv.ParseInt(string(n), 10, 64)
		ok = numberOK && err == nil && (field.kind != "count" || i >= 0)
	case "usage":
		ok = hostUsage(value)
	}
	return ok
}

// These client-generated Chat controls have no lossless portable equivalent.
// store:false is supported; opting into remote completion storage stays outside
// the stateless input contract. Nothing here selects a model or credential.
func ChatNativeOptionNames() []string {
	return []string{"frequency_penalty", "presence_penalty", "seed", "user", "verbosity", "store", "prompt_cache_retention", "promptCacheKey", "reasoningSummary"}
}

func ValidChatNativeOption(name string, value any) bool {
	switch name {
	case "frequency_penalty", "presence_penalty":
		return hostNumber(value)
	case "seed":
		n, ok := value.(json.Number)
		_, err := strconv.ParseInt(string(n), 10, 64)
		return ok && err == nil
	case "user", "promptCacheKey":
		_, ok := value.(string)
		return ok
	case "verbosity":
		return value == "low" || value == "medium" || value == "high"
	case "store":
		return value == false
	case "prompt_cache_retention":
		return value == "in-memory" || value == "24h"
	case "reasoningSummary":
		return value == "auto" || value == "concise" || value == "detailed"
	}
	return false
}

func hostNumber(value any) bool {
	n, ok := value.(json.Number)
	f, err := n.Float64()
	return ok && err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}

func hostNumericMap(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, value := range m {
		if !hostNumber(value) {
			return false
		}
	}
	return true
}

func hostUsageDetails(value any) bool {
	if hostNumericMap(value) {
		return true
	}
	a, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range a {
		if !hostNumericMap(item) {
			return false
		}
	}
	return true
}

func hostUsage(value any) bool {
	m, ok := value.(map[string]any)
	if !ok || !hostNumber(m["inputTokens"]) || !hostNumber(m["outputTokens"]) || !hostNumber(m["totalTokens"]) {
		return false
	}
	for key, value := range m {
		switch key {
		case "inputTokens", "outputTokens", "totalTokens", "requests":
			if !hostNumber(value) {
				return false
			}
		case "inputTokensDetails", "outputTokensDetails":
			if !hostUsageDetails(value) {
				return false
			}
		case "requestUsageEntries":
			entries, ok := value.([]any)
			if !ok {
				return false
			}
			for _, entry := range entries {
				e, ok := entry.(map[string]any)
				if !ok || !hostNumber(e["inputTokens"]) || !hostNumber(e["outputTokens"]) || !hostNumber(e["totalTokens"]) {
					return false
				}
				for key, value := range e {
					switch key {
					case "inputTokens", "outputTokens", "totalTokens":
						if !hostNumber(value) {
							return false
						}
					case "inputTokensDetails", "outputTokensDetails":
						if !hostNumericMap(value) {
							return false
						}
					case "endpoint":
						if _, ok := value.(string); !ok {
							return false
						}
					default:
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return true
}
