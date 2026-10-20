package routercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func workBuddyCapturedChat(t *testing.T, fixture string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "workbuddy", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The complete sanitized capture retains Agent.name metadata, text blocks,
// every tool/schema field and streaming controls; no rejected field is removed.
func TestWorkBuddyCapturedChatMessageAgentNativeOrZeroOutbound(t *testing.T) {
	for _, fixture := range []string{"workbuddy-5.3.8-real-task1.request.json", "workbuddy-5.3.8-real-tool-history.request.json", "workbuddy-5.3.8-real-completed-tools.request.json", "workbuddy-5.3.8-real-cancel.request.json"} {
		t.Run(fixture, func(t *testing.T) { workBuddyAssertCaptureNativeOrZeroOutbound(t, fixture) })
	}
}

func workBuddyAssertCaptureNativeOrZeroOutbound(t *testing.T, fixture string) {
	t.Helper()
	body := workBuddyCapturedChat(t, fixture)
	for _, target := range []string{"chat", "responses", "anthropic"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			executor := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wire, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var got, want map[string]any
				if json.Unmarshal(wire, &got) != nil || json.Unmarshal(body, &want) != nil {
					t.Fatal("invalid native request or capture")
				}
				want["model"] = "pinned"
				if !reflect.DeepEqual(got, want) {
					t.Error("native forwarding changed WorkBuddy Agent.name metadata, instructions, tools or streaming controls")
				}
				return response(200, whole(target, false)), nil
			})}
			w := httptest.NewRecorder()
			result, err := executor.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			if target == "chat" {
				if err != nil || calls != 1 || result.Attempts != 1 || !result.OutputCommitted {
					t.Fatalf("native WorkBuddy capture rejected: calls=%d error=%v", calls, err)
				}
			} else {
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_message_agent" || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
					t.Fatalf("WorkBuddy metadata escaped explicit conversion boundary: calls=%d result=%+v error=%v", calls, result, err)
				}
			}
		})
	}
}

func TestWorkBuddyMessageAgentInputStaysStrict(t *testing.T) {
	for _, role := range []string{"system", "user", "assistant"} {
		body := []byte(`{"messages":[{"role":"` + role + `","content":"hello","agent":"custom-agent-name"}]}`)
		if ValidateInput("chat", body) != nil {
			t.Fatalf("installed %s Agent.name metadata rejected", role)
		}
	}
	if ValidateInput("chat", []byte(`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"result","agent":"custom-agent-name"}]}`)) != nil {
		t.Fatal("installed tool result Agent.name metadata rejected")
	}
	for _, message := range []string{
		`{"role":"user","content":"hello","agent":""}`,
		`{"role":"user","content":"hello","agent":"  "}`,
		`{"role":"user","content":"hello","agent":null}`,
		`{"role":"user","content":"hello","agent":42}`,
		`{"role":"user","content":"hello","agent":true}`,
		`{"role":"user","content":"hello","agent":{}}`,
		`{"role":"user","content":"hello","agent":[]}`,
		`{"role":"user","content":"hello","agent":"a","agent":"b"}`,
		`{"role":"user","content":"hello","agent":"valid","unknown_host_field":true}`,
		`{"role":"user","content":[{"type":"text","text":"hello","agent":"nested"}]}`,
		`{"role":"developer","content":"hello","agent":"unverified-role"}`,
	} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			calls := 0
			executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, whole(target, false)), nil
			})}
			body := []byte(`{"messages":[` + message + `]}`)
			w := httptest.NewRecorder()
			result, err := executor.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			failure, ok := err.(*Error)
			if !ok || failure.Code != "unsupported_input" || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
				t.Fatalf("invalid Agent.name field or unrelated unknown field reached outbound: calls=%d error=%v", calls, err)
			}
		}
	}
	if ValidateInput("chat", []byte(`{"agent":"root","messages":[{"role":"user","content":"hello"}]}`)) == nil {
		t.Fatal("message Agent.name support broadened the root field contract")
	}
}

func TestWorkBuddyHistoryMetadataInputStaysRoleAwareAndStrict(t *testing.T) {
	shared := []string{"messageId", "model", "requestModelId", "requestModelName", "traceId", "conversationRequestId"}
	for _, role := range []string{"assistant", "tool"} {
		fields := append([]string(nil), shared...)
		if role == "assistant" {
			fields = append(fields, "reasoning", "argumentsDisplayText")
		}
		for _, field := range fields {
			message := map[string]any{"role": role, "content": "hello", field: "fixture-value"}
			if role == "tool" {
				message["tool_call_id"] = "call_a"
			}
			body, _ := json.Marshal(map[string]any{"messages": []any{message}})
			if ValidateInput("chat", body) != nil {
				t.Fatalf("installed %s string metadata %s rejected", role, field)
			}
			// Installed providerData defaults some identity/display fields to
			// empty strings; they are opaque values, not required selectors.
			message[field] = ""
			body, _ = json.Marshal(map[string]any{"messages": []any{message}})
			if ValidateInput("chat", body) != nil {
				t.Fatalf("empty %s string metadata %s rejected", role, field)
			}
			for _, value := range []any{nil, 42, true, []any{}, map[string]any{}} {
				message[field] = value
				body, _ = json.Marshal(map[string]any{"messages": []any{message}})
				workBuddyAssertInvalidMetadataZeroOutbound(t, body)
			}
			message[field] = "fixture-value"
			message["unknown_host_field"] = "still rejected"
			body, _ = json.Marshal(map[string]any{"messages": []any{message}})
			workBuddyAssertInvalidMetadataZeroOutbound(t, body)
			duplicate := `{"messages":[{"role":"` + role + `","content":"hello",` + `"` + field + `":"a","` + field + `":"b"`
			if role == "tool" {
				duplicate += `,"tool_call_id":"call_a"`
			}
			workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(duplicate+`}]}`))
		}
	}
	for _, role := range []string{"system", "user", "developer"} {
		for _, field := range append(shared, "reasoning", "argumentsDisplayText") {
			body, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": role, "content": "hello", field: "unverified-role"}}})
			workBuddyAssertInvalidMetadataZeroOutbound(t, body)
		}
	}
	for _, field := range []string{"reasoning", "argumentsDisplayText"} {
		body, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "tool", "tool_call_id": "call_a", "content": "result", field: "unverified-role"}}})
		workBuddyAssertInvalidMetadataZeroOutbound(t, body)
	}
}

func workBuddyAssertInvalidMetadataZeroOutbound(t *testing.T, body []byte) {
	t.Helper()
	for _, target := range []string{"chat", "responses", "anthropic"} {
		calls := 0
		executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(200, whole(target, false)), nil
		})}
		w := httptest.NewRecorder()
		result, err := executor.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
		failure, ok := err.(*Error)
		if !ok || failure.Code != "unsupported_input" || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
			t.Fatalf("invalid history metadata escaped role/type/duplicate boundary: calls=%d result=%+v error=%v", calls, result, err)
		}
	}
}
