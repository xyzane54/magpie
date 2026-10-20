package cloudcodec

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestChatMessageAgentNativeOrExplicitConversionError(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hello","agent":"private-fixture-agent"}]}`)
	codec, err := NewCloudCodec("chat", input)
	if err != nil {
		t.Fatal(err)
	}
	native, err := codec.Request("chat", "pinned", "fixture.invalid")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if json.Unmarshal(native, &got) != nil || json.Unmarshal(input, &want) != nil {
		t.Fatal("invalid native request")
	}
	want["model"] = "pinned"
	if !reflect.DeepEqual(got, want) {
		t.Fatal("native Chat lost message Agent.name metadata")
	}
	for _, target := range []string{"responses", "anthropic"} {
		_, err := codec.Request(target, "pinned", "fixture.invalid")
		var capability *CapabilityError
		if !errors.As(err, &capability) || capability.Code != "capability_message_agent" {
			t.Fatalf("Chat Agent.name metadata silently discarded for %s: %v", target, err)
		}
	}
}

func TestChatMessageAgentGuardDoesNotInspectBusinessData(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"agent is business text"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"f","arguments":"{\"agent\":\"ordinary business value\"}"}}]},{"role":"tool","tool_call_id":"call_a","content":"{\"agent\":\"ordinary result\"}"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"agent":{"type":"string"}}}}}]}`)
	codec, err := NewCloudCodec("chat", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"chat", "responses", "anthropic"} {
		if _, err := codec.Request(target, "pinned", "fixture.invalid"); err != nil {
			t.Fatalf("business property named agent incorrectly requires a native target: %s %v", target, err)
		}
	}
}

func TestChatHistoryMetadataNativeOrExplicitConversionError(t *testing.T) {
	for _, field := range []string{"messageId", "model", "requestModelId", "requestModelName", "traceId", "conversationRequestId", "argumentsDisplayText", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			message := map[string]any{"role": "assistant", "content": "answer", field: "private-fixture-value"}
			input, _ := json.Marshal(map[string]any{"messages": []any{message}})
			codec, err := NewCloudCodec("chat", input)
			if err != nil {
				t.Fatal(err)
			}
			native, err := codec.Request("chat", "pinned", "fixture.invalid")
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if json.Unmarshal(native, &got) != nil || json.Unmarshal(input, &want) != nil {
				t.Fatal("invalid native history")
			}
			want["model"] = "pinned"
			if !reflect.DeepEqual(got, want) {
				t.Fatal("native Chat changed history metadata or reasoning")
			}
			code := "capability_message_metadata"
			if field == "reasoning" {
				code = "capability_reasoning_history"
			}
			for _, target := range []string{"responses", "anthropic"} {
				_, err := codec.Request(target, "pinned", "fixture.invalid")
				var capability *CapabilityError
				if !errors.As(err, &capability) || capability.Code != code {
					t.Fatalf("history metadata or reasoning lost converting to %s: %v", target, err)
				}
			}
		})
	}
}

func TestChatHistoryMetadataGuardDoesNotInspectBusinessProperties(t *testing.T) {
	fields := []string{"annotations", "agent", "messageId", "model", "requestModelId", "requestModelName", "traceId", "conversationRequestId", "argumentsDisplayText", "reasoning", "toolResult"}
	properties, arguments := map[string]any{}, map[string]any{}
	for _, field := range fields {
		properties[field] = map[string]any{"type": "string"}
		arguments[field] = "business value"
	}
	encodedArgs, _ := json.Marshal(arguments)
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": string(encodedArgs)},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_a", "type": "function", "function": map[string]any{"name": "f", "arguments": string(encodedArgs)}}}},
			map[string]any{"role": "tool", "tool_call_id": "call_a", "content": string(encodedArgs)},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "f", "parameters": map[string]any{"type": "object", "properties": properties}}}},
	}
	input, _ := json.Marshal(body)
	codec, err := NewCloudCodec("chat", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"chat", "responses", "anthropic"} {
		if _, err := codec.Request(target, "pinned", "fixture.invalid"); err != nil {
			t.Fatalf("ordinary business properties mistaken for message metadata: %s %v", target, err)
		}
	}
}
