package routercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestWorkBuddyWebSearchToolResultHistoryJSONAndSSE(t *testing.T) {
	fixture := workBuddyCapturedChat(t, "workbuddy-websearch-tool-result.request.json")
	for _, stream := range []bool{false, true} {
		var want map[string]any
		if err := json.Unmarshal(fixture, &want); err != nil {
			t.Fatal(err)
		}
		want["stream"] = stream
		body, _ := json.Marshal(want)
		calls := 0
		e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			wire, _ := io.ReadAll(r.Body)
			var got map[string]any
			_ = json.Unmarshal(wire, &got)
			want["model"] = "pinned"
			if !reflect.DeepEqual(got, want) {
				t.Error("native Chat changed saved WebSearch history or toolResult")
			}
			return response(200, whole("chat", false)), nil
		})}
		w := httptest.NewRecorder()
		result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", "chat")})
		if err != nil || calls != 1 || result.Attempts != 1 || !result.OutputCommitted || w.Body.Len() == 0 {
			t.Fatalf("WebSearch result continuation stream=%v rejected: calls=%d result=%+v error=%v", stream, calls, result, err)
		}
	}
}

func TestWorkBuddyToolResultNativeOrZeroOutbound(t *testing.T) {
	// The installed processor also attaches toolResult to ExitPlanMode calls.
	// Its object contains tool-specific business data, including custom keys.
	for _, role := range []string{"assistant", "tool"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			m := map[string]any{"role": role, "content": "result", "toolResult": map[string]any{"content": []any{map[string]any{"text": "fixture", "image": "business value"}}, "renderer": map[string]any{"type": "text"}, "custom": true}}
			if role == "tool" {
				m["tool_call_id"] = "call_a"
			}
			body, _ := json.Marshal(map[string]any{"messages": []any{
				map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_a", "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}}}}, m,
			}})
			calls := 0
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wire, _ := io.ReadAll(r.Body)
				var got, want map[string]any
				_ = json.Unmarshal(wire, &got)
				_ = json.Unmarshal(body, &want)
				want["model"] = "pinned"
				if !reflect.DeepEqual(got, want) {
					t.Error("native Chat lost tool result metadata")
				}
				return response(200, whole("chat", false)), nil
			})}
			w := httptest.NewRecorder()
			result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			if target == "chat" {
				if err != nil || calls != 1 || result.Attempts != 1 {
					t.Fatalf("%s toolResult rejected: calls=%d error=%v", role, calls, err)
				}
			} else {
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_message_metadata" || calls != 0 || result.Attempts != 0 || w.Body.Len() != 0 {
					t.Fatalf("%s toolResult lost converting to %s: calls=%d error=%v", role, target, calls, err)
				}
			}
		}
	}
}

func TestWorkBuddyToolResultInputStaysStrict(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `"text"`, `true`, `42`} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result","toolResult":`+value+`}]}`))
	}
	for _, role := range []string{"system", "developer", "user"} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"`+role+`","content":"text","toolResult":{}}]}`))
	}
	for _, body := range []string{
		`{"toolResult":{},"messages":[{"role":"user","content":"text"}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result","toolResult":{},"unknown":true}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result","toolResult":{},"toolResult":{}}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result","toolResult":{"content":"a","content":"b"}}]}`,
	} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(body))
	}
}
