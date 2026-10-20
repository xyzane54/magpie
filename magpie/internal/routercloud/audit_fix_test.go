package routercloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func countInput() []byte {
	return []byte(`{"model":"caller-model","messages":[{"role":"user","content":"count this"}],"tools":[{"name":"run","input_schema":{"type":"object"}}]}`)
}

func TestCountTokensUsesPinnedNativeEndpointAndSharedRetry(t *testing.T) {
	target := configured("http://upstream.test/v1", "anthropic")
	target.Authorization = "Bearer dynamic-count-authorization"
	var bodies []string
	var events []Event
	e := Executor{Observe: func(event Event) { events = append(events, event) }, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages/count_tokens" || r.Header.Get("Authorization") != target.Authorization || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") == "" {
			t.Fatalf("invalid count dispatch path/auth: %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		var body map[string]json.RawMessage
		_ = json.Unmarshal(b, &body)
		if string(body["model"]) != `"pinned"` || body["stream"] != nil || body["max_tokens"] != nil || body["tools"] == nil {
			t.Fatalf("invalid count body %s", b)
		}
		if len(bodies) == 1 {
			rsp := response(429, "")
			rsp.Header.Set("Retry-After", "0")
			return rsp, nil
		}
		return response(200, `{"input_tokens":17,"upstream_private_extra":"never reflect"}`), nil
	})}
	w := httptest.NewRecorder()
	out, err := e.ExecutePath(context.Background(), w, "/v1/messages/count_tokens", Request{"chat", countInput(), target})
	if err != nil || out.Operation != "count_tokens" || out.Attempts != 2 || out.Usage != nil || !out.OutputCommitted || len(bodies) != 2 || bodies[0] != bodies[1] || len(out.AttemptDetails) != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if w.Body.String() != `{"input_tokens":17}` || events[len(events)-1].Operation != "count_tokens" {
		t.Fatalf("count reply or observation %s %+v", w.Body.String(), events)
	}
}

func TestPreparedCountTokensUsesNativeCountEndpoint(t *testing.T) {
	target := configured("http://upstream.test/v1", "anthropic")
	path := ""
	e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		return response(http.StatusOK, `{"input_tokens":3}`), nil
	})}
	prepared, err := PrepareRequestBody(Request{Protocol: "anthropic", Body: countInput(), Target: target}, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.ExecutePreparedPath(context.Background(), httptest.NewRecorder(), "/v1/messages/count_tokens", prepared)
	if err != nil || out.Operation != "count_tokens" || path != "/v1/messages/count_tokens" {
		t.Fatalf("prepared count dispatch out=%+v path=%q err=%v", out, path, err)
	}
}

func TestCountTokensCannotBypassRedactionOrInputScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		body     []byte
		redact   bool
		extra    map[string]json.RawMessage
		want     string
	}{
		{"redaction", "anthropic", countInput(), true, nil, "redaction_unavailable"},
		{"unsupported-upstream", "chat", countInput(), false, nil, "unsupported_operation"},
		{"image", "anthropic", []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"secret"}}]}]}`), false, nil, "unsupported_input"},
		{"generation-control", "anthropic", []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`), false, nil, "unsupported_input"},
		{"configured-generation-control", "anthropic", countInput(), false, map[string]json.RawMessage{"temperature": json.RawMessage("0.5")}, "unsupported_input"},
		{"configured-search", "anthropic", countInput(), false, map[string]json.RawMessage{"mcp_servers": json.RawMessage("[]")}, "invalid_target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := configured("http://upstream.test/v1", tc.protocol)
			target.RedactionRequired = tc.redact
			target.Provider.ExtraBody = tc.extra
			calls := 0
			e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must never dispatch") })}
			out, err := e.CountTokens(context.Background(), httptest.NewRecorder(), Request{"anthropic", tc.body, target})
			if safeErrorCode(err) != tc.want || calls != 0 || out.Attempts != 0 || out.Operation != "count_tokens" {
				t.Fatalf("out=%+v calls=%d err=%v", out, calls, err)
			}
		})
	}
}

func TestRedactionGateCoversEveryCloudDispatchPath(t *testing.T) {
	for _, tc := range []struct {
		name, path, protocol string
		body                 []byte
	}{
		{name: "chat", path: "/v1/chat/completions", protocol: "chat", body: input("chat", false)},
		{name: "responses", path: "/v1/responses", protocol: "responses", body: input("responses", false)},
		{name: "anthropic", path: "/v1/messages", protocol: "anthropic", body: input("anthropic", false)},
		{name: "count-tokens", path: "/v1/messages/count_tokens", protocol: "anthropic", body: countInput()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			target := configured("http://upstream.test/v1", tc.protocol)
			target.RedactionRequired = true
			e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(http.StatusOK, whole(tc.protocol, true)), nil
			})}
			out, err := e.ExecutePath(context.Background(), httptest.NewRecorder(), tc.path, Request{
				Protocol: tc.protocol,
				Body:     tc.body,
				Target:   target,
			})
			if safeErrorCode(err) != "redaction_unavailable" || calls != 0 || out.Attempts != 0 {
				t.Fatalf("redaction gate bypassed for %s: out=%+v calls=%d err=%v", tc.name, out, calls, err)
			}
		})
	}
}

func TestExecutePathKeepsAuxiliaryAndGenerationOperationsSeparate(t *testing.T) {
	var path string
	e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		return response(200, whole("chat", true)), nil
	})}
	out, err := e.ExecutePath(context.Background(), httptest.NewRecorder(), "/v1/chat/completions", Request{"anthropic", input("chat", false), configured("http://upstream.test/v1", "chat")})
	if err != nil || out.Operation != "generate" || path != "/v1/chat/completions" {
		t.Fatalf("out=%+v path=%s err=%v", out, path, err)
	}
	path = ""
	var events []Event
	e.Observe = func(event Event) { events = append(events, event) }
	_, err = e.ExecutePath(context.Background(), httptest.NewRecorder(), "/v1/responses/arbitrary-id", Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
	if safeErrorCode(err) != "unsupported_operation" || path != "" {
		t.Fatalf("unsupported path dispatched: %s %v", path, err)
	}
	if len(events) != 2 || events[0].Kind != "request_started" || events[1].Kind != "request_finished" || events[1].Operation != "unsupported" || events[1].ErrorCode != "unsupported_operation" {
		t.Fatalf("unknown path omitted observation: %+v", events)
	}
}

func TestStatelessResponsesNullPreviousIDAndNativeHistory(t *testing.T) {
	body := []byte(`{"model":"client-model","input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-history"},{"role":"user","content":"continue"}],"previous_response_id":null,"metadata":{"task":"task-1"},"include":["reasoning.encrypted_content"],"client_metadata":{"session_id":"session-1"}}`)
	for _, protocol := range []string{"responses", "chat"} {
		calls := 0
		e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			b, _ := io.ReadAll(r.Body)
			for _, field := range []string{"opaque-history", "metadata", "client_metadata", "previous_response_id"} {
				if !strings.Contains(string(b), field) {
					t.Errorf("native field %s lost", field)
				}
			}
			return response(200, whole("responses", true)), nil
		})}
		out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"responses", body, configured("http://upstream.test/v1", protocol)})
		if protocol == "responses" {
			if err != nil || out.Attempts != 1 || calls != 1 {
				t.Fatalf("native history rejected: %+v %v", out, err)
			}
		} else if safeErrorCode(err) != "capability_reasoning_history" || out.Attempts != 0 || calls != 0 {
			t.Fatalf("lossy history dispatched: %+v %v", out, err)
		}
	}
	// Null denotes no remote history; text-only conversion remains stateless.
	plain := []byte(`{"input":"hello","previous_response_id":null}`)
	e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return response(200, whole("chat", true)), nil })}
	if _, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"responses", plain, configured("http://upstream.test/v1", "chat")}); err != nil {
		t.Fatalf("stateless null reference rejected: %v", err)
	}
}
