package routercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestWorkBuddyUsageFullToolHistoryFixtureJSONAndSSE(t *testing.T) {
	fixture, err := os.ReadFile("testdata/workbuddy/workbuddy-usage-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		var original map[string]any
		if err := json.Unmarshal(fixture, &original); err != nil {
			t.Fatal(err)
		}
		original["stream"] = stream
		body, _ := json.Marshal(original)
		calls := 0
		e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			wire, _ := io.ReadAll(r.Body)
			var actual map[string]any
			_ = json.Unmarshal(wire, &actual)
			original["model"] = "pinned"
			if !reflect.DeepEqual(actual, original) {
				t.Error("tool-call assistant usage or original history changed")
			}
			if stream {
				rsp := response(200, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				rsp.Header.Set("Content-Type", "text/event-stream")
				return rsp, nil
			}
			return response(200, whole("chat", true)), nil
		})}
		w := httptest.NewRecorder()
		out, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", "chat")})
		if err != nil || calls != 1 || out.Attempts != 1 || !out.OutputCommitted || w.Body.Len() == 0 {
			t.Fatalf("full fixture stream=%v failed: calls=%d out=%+v err=%v", stream, calls, out, err)
		}
	}
}

func TestWorkBuddyCompletedTurnUsagePreservedOrRejectedBeforeConversion(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"fixture first turn"},{"role":"assistant","content":[{"type":"text","text":"fixture completed","annotations":[]}],"usage":{"inputTokens":20,"outputTokens":5,"totalTokens":25,"requests":1,"inputTokensDetails":[{"cachedTokens":3}],"outputTokensDetails":{"reasoningTokens":2},"requestUsageEntries":[{"inputTokens":20,"outputTokens":5,"totalTokens":25,"endpoint":"fixture","inputTokensDetails":{"cachedTokens":3}}]},"rawUsage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25,"completion_tokens_details":{"reasoning_tokens":2},"vendor_future_counter":{"opaque":true}}},{"role":"user","content":"fixture follow-up"}],"stream":false}`)
	for _, target := range []string{"chat", "responses", "anthropic"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wire, _ := io.ReadAll(r.Body)
				var got, want map[string]any
				_ = json.Unmarshal(wire, &got)
				_ = json.Unmarshal(body, &want)
				want["model"] = "pinned"
				if !reflect.DeepEqual(got, want) {
					t.Error("native Chat changed completed-turn usage metadata")
				}
				return response(200, whole(target, false)), nil
			})}
			w := httptest.NewRecorder()
			result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			if target == "chat" {
				if err != nil || calls != 1 || result.Attempts != 1 {
					t.Fatalf("completed turn rejected: calls=%d error=%v", calls, err)
				}
			} else {
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_message_metadata" || calls != 0 || result.Attempts != 0 || w.Body.Len() != 0 {
					t.Fatalf("usage lost across protocol: calls=%d result=%+v error=%v", calls, result, err)
				}
			}
		})
	}
}

func TestWorkBuddyUsageShapeAndRoleBoundaries(t *testing.T) {
	valid := `{"inputTokens":0,"outputTokens":0,"totalTokens":0}`
	for _, usage := range []string{`null`, `[]`, `"value"`, `{"inputTokens":0}`, `{"inputTokens":"0","outputTokens":0,"totalTokens":0}`, `{"inputTokens":0,"outputTokens":0,"totalTokens":0,"unknown":1}`, `{"inputTokens":0,"outputTokens":0,"totalTokens":0,"inputTokensDetails":{"cached":"0"}}`, `{"inputTokens":0,"outputTokens":0,"totalTokens":0,"requestUsageEntries":[{"inputTokens":0,"outputTokens":0,"totalTokens":0,"endpoint":42}]}`} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":"done","usage":`+usage+`}]}`))
	}
	for _, role := range []string{"system", "developer", "user", "tool"} {
		extra := ""
		if role == "tool" {
			extra = `,"tool_call_id":"fixture"`
		}
		for _, field := range []string{`"usage":` + valid, `"rawUsage":{}`} {
			workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"`+role+`","content":"fixture"`+extra+`,`+field+`}]}`))
		}
	}
	for _, raw := range []string{`null`, `[]`, `42`, `"value"`} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":"done","rawUsage":`+raw+`}]}`))
	}
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":"done","rawUsage":{},"rawUsage":{}}]}`))
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":"done","rawUsage":{},"unknown":1}]}`))
	for _, field := range []string{`"usage":` + valid, `"rawUsage":{}`} {
		for _, target := range []string{"responses", "anthropic"} {
			body := []byte(`{"messages":[{"role":"assistant","content":"done",` + field + `}]}`)
			calls := 0
			e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, whole(target, false)), nil })}
			out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			failure, ok := err.(*Error)
			if !ok || failure.Code != "capability_message_metadata" || calls != 0 || out.Attempts != 0 {
				t.Fatalf("isolated usage field lost for %s: %v", target, err)
			}
		}
	}
}

func TestWorkBuddyUsageOptionalSDKFieldsAndOpaqueBusinessData(t *testing.T) {
	for _, extra := range []string{
		`"usage":{"inputTokens":0,"outputTokens":0,"totalTokens":0}`,
		`"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"inputTokensDetails":{},"outputTokensDetails":[],"requestUsageEntries":[{"inputTokens":1,"outputTokens":2,"totalTokens":3}]}`,
		`"rawUsage":{}`,
	} {
		body := []byte(`{"messages":[{"role":"assistant","content":"fixture",` + extra + `}]}`)
		if err := ValidateInput("chat", body); err != nil {
			t.Fatalf("installed optional schema rejected: %v", err)
		}
	}
	body := []byte(`{"messages":[{"role":"user","content":"usage and rawUsage are business text"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"usage":{"type":"object"},"rawUsage":{"type":"string"}}}}}]}`)
	for _, target := range []string{"chat", "responses", "anthropic"} {
		calls := 0
		e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, whole(target, false)), nil })}
		_, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", body, configured("http://fixture.invalid/v1", target)})
		if err != nil || calls != 1 {
			t.Fatalf("business usage key misclassified for %s: %v", target, err)
		}
	}
}
