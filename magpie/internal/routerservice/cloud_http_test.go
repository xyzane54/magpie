package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

const cloudHTTPManagement = "management-only-never-upstream"

func cloudHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	layout, _ := fixture(t)
	return handler(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, routerconfig.New(layout))
}

func cloudHTTPBind(t *testing.T, h http.Handler, base, protocol, app, key string) {
	t.Helper()
	draft := providerInputFor(base + "/v1")
	draft.Protocol, draft.Model, draft.APIKey = protocol, "", &key
	p := providerSaved(t, providerRequest(t, h, "POST", "/api/cloud-providers", cloudHTTPManagement, draft))
	mode := routerconfig.StoredCredential
	if app == "loomy" {
		mode = routerconfig.ForwardAuthorization
	}
	w := providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{ID: app, Name: app, Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "profile-pinned"}, CredentialMode: mode})
	if w.Code != 200 {
		t.Fatalf("bind: %d %s", w.Code, w.Body.String())
	}
}

func cloudHTTPPath(protocol string) string {
	switch protocol {
	case "chat":
		return "chat/completions"
	case "responses":
		return "responses"
	default:
		return "messages"
	}
}

func cloudHTTPInput(protocol string, stream, tools bool) map[string]any {
	raw := map[string]string{
		"chat":      `{"model":"profile-pinned","messages":[{"role":"user","content":"hello"}]}`,
		"responses": `{"model":"profile-pinned","input":"hello"}`,
		"anthropic": `{"model":"profile-pinned","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
	}
	if tools {
		raw = map[string]string{
			"chat":      `{"model":"profile-pinned","messages":[{"role":"user","content":"read file"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_old","type":"function","function":{"name":"run","arguments":"{\"path\":\"old.txt\"}"}}]},{"role":"tool","tool_call_id":"call_old","content":"old file content"},{"role":"user","content":"read next file"}],"tools":[{"type":"function","function":{"name":"run","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`,
			"responses": `{"model":"profile-pinned","input":[{"role":"user","content":"read file"},{"type":"function_call","call_id":"call_old","name":"run","arguments":"{\"path\":\"old.txt\"}"},{"type":"function_call_output","call_id":"call_old","output":"old file content"},{"role":"user","content":"read next file"}],"tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`,
			"anthropic": `{"model":"profile-pinned","max_tokens":64,"messages":[{"role":"user","content":"read file"},{"role":"assistant","content":[{"type":"tool_use","id":"call_old","name":"run","input":{"path":"old.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_old","content":"old file content"},{"type":"text","text":"read next file"}]}],"tools":[{"name":"run","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`,
		}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw[protocol]), &body); err != nil {
		panic(err)
	}
	body["stream"] = stream
	return body
}

func cloudHTTPReply(protocol string, tools bool) string {
	if tools {
		return map[string]string{
			"chat":      `{"id":"chatcmpl-test","model":"profile-pinned","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_new","type":"function","function":{"name":"run","arguments":"{\"path\":\"next.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`,
			"responses": `{"id":"resp_test","object":"response","status":"completed","model":"profile-pinned","output":[{"type":"function_call","id":"fc_test","call_id":"call_new","name":"run","arguments":"{\"path\":\"next.txt\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":3}}`,
			"anthropic": `{"id":"msg_test","type":"message","role":"assistant","model":"profile-pinned","content":[{"type":"tool_use","id":"call_new","name":"run","input":{"path":"next.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":3}}`,
		}[protocol]
	}
	return map[string]string{
		"chat":      `{"id":"chatcmpl-test","object":"chat.completion","model":"profile-pinned","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
		"responses": `{"id":"resp_test","object":"response","status":"completed","model":"profile-pinned","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}]}`,
		"anthropic": `{"id":"msg_test","type":"message","role":"assistant","model":"profile-pinned","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`,
	}[protocol]
}

func cloudHTTPCall(t *testing.T, h http.Handler, ctx context.Context, app, protocol string, body any, auth, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:13145/app/"+app+"/v1/"+cloudHTTPPath(protocol), bytes.NewReader(data)).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	if apiKey != "" {
		r.Header.Set("x-api-key", apiKey)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCloudHTTPProtocolAndToolRoundTrip(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			for _, stream := range []bool{false, true} {
				for _, tools := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s-to-%s/stream-%t/tools-%t", source, target, stream, tools), func(t *testing.T) {
						var calls atomic.Int32
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							b, _ := io.ReadAll(r.Body)
							var body map[string]json.RawMessage
							if json.Unmarshal(b, &body) != nil || string(body["model"]) != `"profile-pinned"` {
								t.Errorf("model escaped Profile: %s", b)
							}
							if r.URL.Path != "/v1/"+cloudHTTPPath(target) {
								t.Errorf("wrong upstream path: %s", r.URL.Path)
							}
							if target == "anthropic" {
								if r.Header.Get("x-api-key") != "bound-provider-key" || r.Header.Get("Authorization") != "" || r.Header.Get("anthropic-version") == "" {
									t.Error("wrong Anthropic credential")
								}
							} else if r.Header.Get("Authorization") != "Bearer bound-provider-key" || r.Header.Get("x-api-key") != "" {
								t.Error("wrong OpenAI credential")
							}
							if tools {
								for _, part := range []string{"call_old", "old.txt", "old file content", "run"} {
									if !bytes.Contains(b, []byte(part)) {
										t.Errorf("tool history missing %s: %s", part, b)
									}
								}
							}
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, cloudHTTPReply(target, tools))
						}))
						defer upstream.Close()
						h := cloudHTTPHandler(t)
						cloudHTTPBind(t, h, upstream.URL, target, "codex", "bound-provider-key")
						w := cloudHTTPCall(t, h, context.Background(), "codex", source, cloudHTTPInput(source, stream, tools), "Bearer client-placeholder", "client-api-key-placeholder")
						if w.Code != 200 || calls.Load() != 1 || w.Header().Get("X-Request-ID") == "" {
							t.Fatalf("entry failed: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
						}
						if stream {
							if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "data:") {
								t.Fatal("missing protocol SSE", w.Body.String())
							}
						} else if !json.Valid(w.Body.Bytes()) {
							t.Fatal("invalid JSON", w.Body.String())
						}
						want := []string{"hello"}
						if tools {
							want = []string{"call_new", "next.txt", "run"}
						}
						for _, part := range want {
							if !strings.Contains(w.Body.String(), part) {
								t.Fatalf("reply lost %s: %s", part, w.Body.String())
							}
						}
						for _, secret := range []string{"bound-provider-key", cloudHTTPManagement, "client-placeholder", "client-api-key-placeholder"} {
							if strings.Contains(w.Body.String(), secret) {
								t.Fatal("secret in output")
							}
						}
					})
				}
			}
		}
	}
}

func TestCloudHTTPBindingAndCredentialBoundaries(t *testing.T) {
	var first, second atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer first-provider-key":
			first.Add(1)
		case "Bearer second-provider-key":
			second.Add(1)
		default:
			t.Error("wrong bound credential")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	h := cloudHTTPHandler(t)
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "first-provider-key")
	cloudHTTPBind(t, h, upstream.URL, "chat", "custom-editor", "second-provider-key")
	for _, app := range []string{"codex", "custom-editor"} {
		w := cloudHTTPCall(t, h, context.Background(), app, "chat", cloudHTTPInput("chat", false, false), "", "")
		if w.Code != 200 {
			t.Fatal("ordinary app requires inbound credential", w.Code, w.Body.String())
		}
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("cross-app provider selection")
	}
	for _, credential := range []struct{ auth, key string }{{"Bearer " + cloudHTTPManagement, ""}, {"", cloudHTTPManagement}} {
		w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), credential.auth, credential.key)
		if w.Code != 403 {
			t.Fatalf("management accepted by model entry: %d %s", w.Code, w.Body.String())
		}
	}
	bad := cloudHTTPInput("chat", false, false)
	bad["model"] = "another-provider-model"
	if w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", bad, "", ""); w.Code != 400 {
		t.Fatalf("model binding escaped: %d %s", w.Code, w.Body.String())
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("rejected request reached upstream")
	}
}

func TestCloudHTTPLoomyDynamicAuthorization(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer dynamic-business-token" || r.Header.Get("x-api-key") != "" {
			t.Error("Loomy dynamic auth was not exclusive")
		}
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("anthropic", false))
	}))
	defer upstream.Close()
	h := cloudHTTPHandler(t)
	cloudHTTPBind(t, h, upstream.URL, "anthropic", "loomy", "saved-key-must-not-win")
	if w := cloudHTTPCall(t, h, context.Background(), "loomy", "anthropic", cloudHTTPInput("anthropic", false, false), "Bearer dynamic-business-token", "placeholder"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := cloudHTTPCall(t, h, context.Background(), "loomy", "anthropic", cloudHTTPInput("anthropic", false, false), "", ""); w.Code != 401 {
		t.Fatal("missing Loomy credential accepted", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatal("invalid Loomy request reached upstream")
	}
}

func TestCloudHTTPModelsAndNativeTokenCounts(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/messages/count_tokens" || r.Header.Get("x-api-key") != "count-provider-key" {
					t.Error("count used generation or wrong credential")
				}
				b, _ := io.ReadAll(r.Body)
				if !bytes.Contains(b, []byte(`"model":"profile-pinned"`)) {
					t.Error("count escaped model")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"input_tokens":17,"debug":"must-not-return"}`)
			}))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, protocol, "codex", "count-provider-key")
			w := providerRequest(t, h, "GET", "/app/codex/v1/models", "", nil)
			var models struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &models) != nil || len(models.Data) != 1 || models.Data[0].ID != "profile-pinned" || calls.Load() != 0 {
				t.Fatalf("model list discovers or escapes Profile: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
			}
			w = providerRequest(t, h, "POST", "/app/codex/v1/messages/count_tokens", "", map[string]any{"model": "profile-pinned", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
			want := 501
			if protocol == "anthropic" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("count: %d %s", w.Code, w.Body.String())
			}
			if protocol == "anthropic" {
				if w.Body.String() != `{"input_tokens":17}` || calls.Load() != 1 {
					t.Fatal("native count not preserved", w.Body.String(), calls.Load())
				}
			} else if calls.Load() != 0 {
				t.Fatal("unsupported count reached generation")
			}
			if w := providerRequest(t, h, "POST", "/app/codex/v1/embeddings", "", map[string]any{"input": "hello"}); w.Code != 404 {
				t.Fatal("unsupported auxiliary operation accepted", w.Code)
			}
		})
	}
}

// These are wire SSE frames, so the matrix also covers a genuinely streaming
// upstream rather than only rendering a whole upstream reply as client SSE.
func cloudHTTPStream(protocol string, tool bool) string {
	frames := []string{}
	switch protocol {
	case "chat":
		delta := `{"content":"hello"}`
		finish := "stop"
		if tool {
			delta = `{"tool_calls":[{"index":0,"id":"call_new","type":"function","function":{"name":"run","arguments":"{\"path\":\"next.txt\"}"}}]}`
			finish = "tool_calls"
		}
		frames = []string{`{"id":"stream","choices":[{"index":0,"delta":` + delta + `}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`, `[DONE]`}
	case "responses":
		frames = []string{`{"type":"response.created","response":{"id":"resp_stream","model":"profile-pinned"}}`}
		if tool {
			frames = append(frames, `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_new","type":"function_call","call_id":"call_new","name":"run","arguments":""}}`, `{"type":"response.function_call_arguments.delta","item_id":"fc_new","output_index":0,"delta":"{\"path\":\"next.txt\"}"}`, `{"type":"response.function_call_arguments.done","item_id":"fc_new","output_index":0,"arguments":"{\"path\":\"next.txt\"}"}`)
		} else {
			frames = append(frames, `{"type":"response.output_text.delta","delta":"hello"}`)
		}
		frames = append(frames, `{"type":"response.completed","response":{"id":"resp_stream","status":"completed","usage":{"input_tokens":7,"output_tokens":2}}}`)
	case "anthropic":
		block := `{"type":"text","text":""}`
		delta := `{"type":"text_delta","text":"hello"}`
		reason := "end_turn"
		if tool {
			block = `{"type":"tool_use","id":"call_new","name":"run","input":{}}`
			delta = `{"type":"input_json_delta","partial_json":"{\"path\":\"next.txt\"}"}`
			reason = "tool_use"
		}
		frames = []string{`{"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"profile-pinned","content":[],"usage":{"input_tokens":7,"output_tokens":0}}}`, `{"type":"content_block_start","index":0,"content_block":` + block + `}`, `{"type":"content_block_delta","index":0,"delta":` + delta + `}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"` + reason + `"},"usage":{"output_tokens":2}}`, `{"type":"message_stop"}`}
	}
	var out strings.Builder
	for _, frame := range frames {
		out.WriteString("data: " + frame + "\n\n")
	}
	return out.String()
}

func TestCloudHTTPUpstreamSSETextAndToolRoundTrip(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			for _, tool := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s/tool-%t", source, target, tool), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, cloudHTTPStream(target, tool))
						w.(http.Flusher).Flush()
					}))
					defer upstream.Close()
					h := cloudHTTPHandler(t)
					cloudHTTPBind(t, h, upstream.URL, target, "codex", "bound-provider-key")
					w := cloudHTTPCall(t, h, context.Background(), "codex", source, cloudHTTPInput(source, true, tool), "", "")
					if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
						t.Fatalf("upstream SSE failed: %d %s", w.Code, w.Body.String())
					}
					want := []string{"hello"}
					if tool {
						want = []string{"call_new", "next.txt", "run"}
					}
					for _, part := range want {
						if !strings.Contains(w.Body.String(), part) {
							t.Fatalf("stream lost %s: %s", part, w.Body.String())
						}
					}
					terminal := map[string]string{"chat": "[DONE]", "responses": "response.completed", "anthropic": "message_stop"}[source]
					if !strings.Contains(w.Body.String(), terminal) {
						t.Fatalf("stream unfinished: %s", w.Body.String())
					}
				})
			}
		}
	}
}

func TestCloudHTTPRealSecondTurnCompletesToolConversation(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			const executionResult = "executed next.txt: file contents"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				data, _ := io.ReadAll(r.Body)
				if n == 2 {
					for _, part := range []string{"call_new", "next.txt", executionResult} {
						if !bytes.Contains(data, []byte(part)) {
							t.Errorf("second upstream turn lost %s: %s", part, data)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, cloudHTTPReply(protocol, n == 1))
			}))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, protocol, "codex", "bound-provider-key")
			first := cloudHTTPCall(t, h, context.Background(), "codex", protocol, cloudHTTPInput(protocol, false, false), "", "")
			if first.Code != 200 {
				t.Fatalf("first turn: %d %s", first.Code, first.Body.String())
			}
			var reply map[string]json.RawMessage
			if err := json.Unmarshal(first.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			var callID, arguments string
			second := cloudHTTPInput(protocol, false, false)
			switch protocol {
			case "chat":
				var choices []struct {
					Message struct {
						Role      string `json:"role"`
						Content   any    `json:"content"`
						ToolCalls []struct {
							ID       string `json:"id"`
							Type     string `json:"type"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"message"`
				}
				if err := json.Unmarshal(reply["choices"], &choices); err != nil || len(choices) != 1 || len(choices[0].Message.ToolCalls) != 1 {
					t.Fatalf("invalid tool reply: %s", first.Body.String())
				}
				message := choices[0].Message
				callID = message.ToolCalls[0].ID
				arguments = message.ToolCalls[0].Function.Arguments
				second["messages"] = []any{map[string]any{"role": "user", "content": "read next file"}, message, map[string]any{"role": "tool", "tool_call_id": callID, "content": executionResult}}
			case "responses":
				var output []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
					Status    string `json:"status"`
				}
				if err := json.Unmarshal(reply["output"], &output); err != nil || len(output) != 1 || output[0].Type != "function_call" {
					t.Fatalf("invalid tool reply: %s", first.Body.String())
				}
				callID = output[0].CallID
				arguments = output[0].Arguments
				second["input"] = []any{map[string]any{"role": "user", "content": "read next file"}, output[0], map[string]any{"type": "function_call_output", "call_id": callID, "output": executionResult}}
			case "anthropic":
				var content []struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}
				if err := json.Unmarshal(reply["content"], &content); err != nil || len(content) != 1 || content[0].Type != "tool_use" {
					t.Fatalf("invalid tool reply: %s", first.Body.String())
				}
				callID = content[0].ID
				arguments = string(content[0].Input)
				second["messages"] = []any{map[string]any{"role": "user", "content": "read next file"}, map[string]any{"role": "assistant", "content": content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": callID, "content": executionResult}}}}
			}
			var args struct {
				Path string `json:"path"`
			}
			if callID != "call_new" || json.Unmarshal([]byte(arguments), &args) != nil || args.Path != "next.txt" {
				t.Fatalf("tool id/arguments were not preserved: id=%s args=%s", callID, arguments)
			}
			final := cloudHTTPCall(t, h, context.Background(), "codex", protocol, second, "", "")
			if final.Code != 200 || calls.Load() != 2 || !strings.Contains(final.Body.String(), "hello") || strings.Contains(final.Body.String(), "call_new") {
				t.Fatalf("tool conversation did not complete: %d calls=%d %s", final.Code, calls.Load(), final.Body.String())
			}
		})
	}
}
