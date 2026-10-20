package routerservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func contractHTTPRaw(h http.Handler, protocol, sub, body string, headers http.Header) *httptest.ResponseRecorder {
	if sub == "" {
		sub = cloudHTTPPath(protocol)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:13145/app/workbuddy/v1/"+sub, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:2345"
	r.Header = headers.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestProtocolContractRejectsLossyConversion(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			if source == target {
				continue
			}
			cases := map[string]string{}
			if source == "chat" {
				cases["competing-token-limits"] = `{"messages":[{"role":"user","content":"hi"}],"max_tokens":8,"max_completion_tokens":16}`
				cases["empty-system-message"] = `{"messages":[{"role":"system","content":""},{"role":"user","content":"hi"}]}`
			} else if source == "responses" {
				cases["empty-system-message"] = `{"input":[{"role":"system","content":""},{"role":"user","content":"hi"}]}`
			}
			for _, role := range []string{"user", "assistant"} {
				field := "messages"
				if source == "responses" {
					field = "input"
				}
				cases["empty-"+role+"-message"] = fmt.Sprintf(`{%q:[{"role":%q,"content":""},{"role":"user","content":"hi"}]}`, field, role)
			}
			if target == "anthropic" {
				for _, composition := range []string{"anyOf", "oneOf", "allOf"} {
					schema := fmt.Sprintf(`{"type":"object",%q:[{"properties":{"left":{"type":"string"}},"required":["left"]},{"properties":{"right":{"type":"string"}},"required":["right"]}]}`, composition)
					body := fmt.Sprintf(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"run","parameters":%s}}]}`, schema)
					if source == "responses" {
						body = fmt.Sprintf(`{"input":"hi","tools":[{"type":"function","name":"run","parameters":%s}]}`, schema)
					}
					cases["root-"+composition] = body
				}
				if source == "chat" {
					cases["json-mode-is-not-a-system-prompt"] = `{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`
				} else {
					cases["json-mode-is-not-a-system-prompt"] = `{"input":"hi","text":{"format":{"type":"json_object"}}}`
				}
			}
			for name, body := range cases {
				t.Run(source+"-to-"+target+"/"+name, func(t *testing.T) {
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, cloudHTTPReply(target, false))
					}))
					defer upstream.Close()
					var done ModelCompletion
					h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
					cloudHTTPBind(t, h, upstream.URL, target, "workbuddy", "bound-key")
					w := contractHTTPRaw(h, source, "", body, nil)
					if w.Code != 400 || calls.Load() != 0 || done.Result.Attempts != 0 || done.ErrorCode != "capability_protocol_conversion" {
						t.Fatalf("lossy request executed: status=%d calls=%d completion=%+v body=%s", w.Code, calls.Load(), done, w.Body.String())
					}
				})
			}
		}
	}
}

func TestProtocolContractRejectsAmbiguousToolArguments(t *testing.T) {
	for _, source := range []string{"chat", "responses"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			t.Run(source+"-to-"+target, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply(target, false))
				}))
				defer upstream.Close()
				var done ModelCompletion
				h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
				cloudHTTPBind(t, h, upstream.URL, target, "workbuddy", "bound-key")
				body := cloudHTTPInput(source, false, true)
				raw, _ := json.Marshal(body)
				// arguments is itself encoded JSON, so its duplicate member must be
				// checked at that boundary, including nested objects and escaped keys.
				for _, args := range []string{`{"path":"first","path":"last"}`, `{"nested":[{"image":1,"\u0069mage":2}]}`} {
					var input map[string]any
					_ = json.Unmarshal(raw, &input)
					if source == "chat" {
						input["messages"].([]any)[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"] = args
					} else {
						input["input"].([]any)[1].(map[string]any)["arguments"] = args
					}
					data, _ := json.Marshal(input)
					w := contractHTTPRaw(h, source, "", string(data), nil)
					if w.Code != 400 || calls.Load() != 0 || done.ErrorCode != "unsupported_input" || done.Result.Attempts != 0 {
						t.Fatalf("ambiguous arguments executed: status=%d calls=%d completion=%+v", w.Code, calls.Load(), done)
					}
				}
			})
		}
	}
}

func TestProtocolContractRejectsSemanticHeaders(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, sub := range []string{"", "messages/count_tokens"} {
			if sub != "" && source != "anthropic" {
				continue
			}
			t.Run(source+"/"+sub, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if sub != "" {
						io.WriteString(w, `{"input_tokens":4}`)
					} else {
						io.WriteString(w, cloudHTTPReply(source, false))
					}
				}))
				defer upstream.Close()
				var done ModelCompletion
				h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
				cloudHTTPBind(t, h, upstream.URL, source, "workbuddy", "bound-key")
				input := cloudHTTPInput(source, false, false)
				delete(input, "max_tokens")
				if sub != "" {
					delete(input, "stream")
				}
				body, _ := json.Marshal(input)
				for _, headers := range []http.Header{
					// Literal shape from the committed fixed Claude capture; values
					// choose features that the public executor cannot forward.
					{"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05,effort-2025-11-24"}},
					{"Anthropic-Beta": {""}},
					{"Openai-Beta": {"unsupported-feature"}},
					{"Anthropic-Version": {"unknown-version"}},
					{"Anthropic-Version": {"2023-06-01", "2023-06-01"}},
				} {
					w := contractHTTPRaw(h, source, sub, string(body), headers)
					if w.Code != 400 || calls.Load() != 0 || done.ErrorCode != "unsupported_input" || done.Result.Attempts != 0 || done.Result.OutputCommitted || done.RequestID != w.Header().Get("X-Request-ID") {
						t.Fatalf("semantic header ignored: status=%d calls=%d completion=%+v", w.Code, calls.Load(), done)
					}
					for _, private := range []string{"unsupported-feature", "claude-code-20250219", "unknown-version", "bound-key"} {
						if strings.Contains(w.Body.String(), private) {
							t.Fatal("private input reflected in error")
						}
					}
				}
				if sub != "" {
					w := contractHTTPRaw(h, source, sub, string(body), http.Header{"Anthropic-Version": {"2023-06-01"}})
					if w.Code != 200 || calls.Load() != 1 || done.Result.Attempts != 1 || done.Result.Operation != "count_tokens" {
						t.Fatal("supported counting version rejected", w.Code, done)
					}
				}
			})
		}
	}
}

func TestProtocolContractNativeFieldsRemainIntact(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			body := cloudHTTPInput(protocol, false, true)
			if protocol == "chat" {
				body["max_tokens"], body["max_completion_tokens"] = 8, 16
			}
			field := "messages"
			if protocol == "responses" {
				field = "input"
			}
			body[field] = append([]any{map[string]any{"role": "user", "content": ""}, map[string]any{"role": "assistant", "content": ""}}, body[field].([]any)...)
			if protocol == "anthropic" {
				body["system"] = ""
			} else {
				body[field] = append([]any{map[string]any{"role": "system", "content": ""}}, body[field].([]any)...)
			}
			for _, composition := range []string{"anyOf", "oneOf", "allOf"} {
				schema := map[string]any{"type": "object", composition: []any{map[string]any{"properties": map[string]any{"value": map[string]any{"type": "string"}}}}}
				tool := map[string]any{"type": "function", "name": composition, "parameters": schema}
				if protocol == "chat" {
					tool = map[string]any{"type": "function", "function": map[string]any{"name": composition, "parameters": schema}}
				} else if protocol == "anthropic" {
					tool = map[string]any{"name": composition, "input_schema": schema}
				}
				body["tools"] = append(body["tools"].([]any), tool)
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var got map[string]json.RawMessage
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &got)
				for key, value := range body {
					want, _ := json.Marshal(value)
					var actual, expected any
					if json.Unmarshal(got[key], &actual) != nil || json.Unmarshal(want, &expected) != nil {
						t.Errorf("missing native field %s", key)
					} else if !reflect.DeepEqual(actual, expected) {
						t.Errorf("native field changed: %s", key)
					}
				}
				if !bytes.Contains(data, []byte(`"model":"profile-pinned"`)) {
					t.Error("native model escaped Profile")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, cloudHTTPReply(protocol, true))
			}))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, protocol, "workbuddy", "bound-key")
			headers := http.Header{}
			if protocol == "anthropic" {
				headers.Set("Anthropic-Version", "2023-06-01")
			}
			data, _ := json.Marshal(body)
			w := contractHTTPRaw(h, protocol, "", string(data), headers)
			if w.Code != 200 || calls.Load() != 1 {
				t.Fatalf("native contract failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
