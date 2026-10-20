package cloudcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var cloudProtocols = []Protocol{Chat, Responses, Anthropic}

func cloudRequestFixture(t *testing.T, p Protocol, stream bool) []byte {
	t.Helper()
	r := &Request{Model: "client-model", Stream: stream, MaxTokens: 100,
		System: "system instructions", Tools: []Tool{{Name: "run", Schema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)}},
		Messages: []Message{
			{Role: "user", Parts: []Part{{Kind: Text, Text: "use the tool"}}},
			{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "call_first", Name: "run", Args: json.RawMessage(`{"n":1}`)}, {Kind: ToolCall, ID: "call_second", Name: "run", Args: json.RawMessage(`{"n":2}`)}}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "call_first", Text: "one"}, {Kind: ToolResult, CallID: "call_second", Text: "two"}, {Kind: Text, Text: "continue"}}},
		}}
	b, err := cloudBuild(p, r, r.Model, "example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if p == Anthropic {
		// This matrix exercises the portable subset. The shared builder adds
		// native cache hints automatically; explicit cache preservation has
		// separate tests and must not be mistaken for portable semantics.
		var body map[string]any
		json.Unmarshal(b, &body)
		for _, key := range []string{"system", "tools"} {
			for _, block := range body[key].([]any) {
				delete(block.(map[string]any), "cache_control")
			}
		}
		for _, message := range body["messages"].([]any) {
			for _, block := range message.(map[string]any)["content"].([]any) {
				delete(block.(map[string]any), "cache_control")
			}
		}
		b = Raw(body)
	}
	return b
}

func cloudResponseFixture(t *testing.T, p Protocol) []byte {
	t.Helper()
	r := Result{ID: "reply", Model: "served-model", Stop: "tool", Usage: Usage{Input: 8, Output: 5},
		Parts: []Part{{Kind: Text, Text: "calling tools"}, {Kind: ToolCall, ID: "call_first", Name: "run", Args: json.RawMessage(`{"n":1}`)}, {Kind: ToolCall, ID: "call_second", Name: "run", Args: json.RawMessage(`{"n":2}`)}}}
	return cloudRender(p, r, &Request{Model: "client-model"})
}

func cloudSSEFixture(t *testing.T, p Protocol, b []byte) string {
	t.Helper()
	w := httptest.NewRecorder()
	sw := NewSSEWriter(w)
	for _, e := range WholeEvents(p, b) {
		sw.Event(e.Name, e.Data)
	}
	return w.Body.String()
}

func cloudReadResult(t *testing.T, p Protocol, stream bool, b []byte) Result {
	t.Helper()
	var col Collector
	decode := cloudDecoder(p)
	if stream {
		if err := ReadSSE(bytes.NewReader(b), func(_, data string) error { return decode(data, col.Add) }); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, e := range WholeEvents(p, b) {
			data, ok := e.Data.(string)
			if !ok {
				j, _ := json.Marshal(e.Data)
				data = string(j)
			}
			if err := decode(data, col.Add); err != nil {
				t.Fatal(err)
			}
		}
	}
	if col.Err != "" {
		t.Fatal(col.Err)
	}
	return col.Finish()
}

func TestCloudCodecMultiTurnThreeByThree(t *testing.T) {
	for _, from := range cloudProtocols {
		for _, to := range cloudProtocols {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				c, err := NewCloudCodec(string(from), cloudRequestFixture(t, from, true))
				if err != nil {
					t.Fatal(err)
				}
				body, err := c.Request(string(to), "cloud-model", "example.test")
				if err != nil {
					t.Fatal(err)
				}
				r, err := cloudParse(to, body)
				if err != nil || r.Model != "cloud-model" || !r.Stream || len(r.Tools) != 1 {
					t.Fatalf("request = %+v, %v", r, err)
				}
				calls, results := map[string]string{}, map[string]string{}
				for _, m := range r.Messages {
					for _, p := range m.Parts {
						switch p.Kind {
						case ToolCall:
							calls[p.ID] = string(p.Args)
						case ToolResult:
							results[p.CallID] = p.Text
						}
					}
				}
				if calls["call_first"] != `{"n":1}` || calls["call_second"] != `{"n":2}` || results["call_first"] != "one" || results["call_second"] != "two" {
					t.Fatalf("tool history corrupted: calls=%v results=%v", calls, results)
				}
			})
		}
	}
}

func TestCloudCodecReplyThreeByThree(t *testing.T) {
	for _, client := range cloudProtocols {
		for _, upstream := range cloudProtocols {
			for _, stream := range []bool{false, true} {
				for _, upstreamStream := range []bool{false, true} {
					name := string(client) + "/" + string(upstream)
					t.Run(name, func(t *testing.T) {
						c, err := NewCloudCodec(string(client), cloudRequestFixture(t, client, stream))
						if err != nil {
							t.Fatal(err)
						}
						b := cloudResponseFixture(t, upstream)
						contentType, input := "application/json", string(b)
						if upstreamStream {
							contentType, input = "text/event-stream", cloudSSEFixture(t, upstream, b)
						}
						w := httptest.NewRecorder()
						reply, err := c.Reply(w, string(upstream), contentType, strings.NewReader(input))
						if err != nil || !reply.OutputCommitted || !reply.UsageKnown || reply.Usage.Input != 8 || reply.Usage.Output != 5 {
							t.Fatalf("reply = %+v, %v; output=%s", reply, err, w.Body.String())
						}
						res := cloudReadResult(t, client, stream, w.Body.Bytes())
						calls := map[string]string{}
						for _, p := range res.Parts {
							if p.Kind == ToolCall {
								calls[RawCallID("magpie_", p.ID)] = string(p.Args)
							}
						}
						if len(calls) != 2 || calls["call_first"] != `{"n":1}` || calls["call_second"] != `{"n":2}` || res.Stop != "tool" {
							t.Fatalf("tool response corrupted: %+v; output=%s", res, w.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestCloudCodecNativePreservesRequestAndResponse(t *testing.T) {
	c, err := NewCloudCodec("chat", []byte(`{"model":"client","stream":false,"messages":[{"role":"user","content":"hi"}],"custom":{"opaque":[1,2]},"temperature":0}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Request("chat", "chosen", "example.test")
	if err != nil || !bytes.Contains(b, []byte(`"custom":{"opaque":[1,2]}`)) || !bytes.Contains(b, []byte(`"temperature":0`)) {
		t.Fatalf("native request = %s, %v", b, err)
	}
	response := `{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"opaque":{"vendor":true}}`
	w := httptest.NewRecorder()
	r, err := c.Reply(w, "chat", "application/json", strings.NewReader(response))
	if err != nil || r.UsageKnown || w.Body.String() != response {
		t.Fatalf("native response=%s metadata=%+v error=%v", w.Body.String(), r, err)
	}
}

type cloudFailReader struct {
	io.Reader
	err error
}

func (r *cloudFailReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if err == io.EOF {
		return 0, r.err
	}
	return n, err
}

type cloudFailWriter struct{ header http.Header }

func (w *cloudFailWriter) Header() http.Header { return w.header }
func (*cloudFailWriter) WriteHeader(int)       {}
func (*cloudFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("write https://example.test/?key=secret")
}

func TestCloudCodecErrorsAreSafeAndNeverComplete(t *testing.T) {
	for _, client := range cloudProtocols {
		for _, upstream := range cloudProtocols {
			c, err := NewCloudCodec(string(client), cloudRequestFixture(t, client, true))
			if err != nil {
				t.Fatal(err)
			}
			valid := cloudSSEFixture(t, upstream, cloudResponseFixture(t, upstream))
			// Remove the final SSE event entirely. Previous text and calls may
			// already be sent; there must be no generated success terminator.
			chunks := strings.Split(strings.TrimSuffix(valid, "\n\n"), "\n\n")
			cut := strings.Join(chunks[:len(chunks)-1], "\n\n") + "\n\n"
			for _, input := range []string{cut, "data: {malformed\n\n", `data: {"error":{"message":"https://example.test/?key=secret"}}` + "\n\n"} {
				w := httptest.NewRecorder()
				_, err := c.Reply(w, string(upstream), "text/event-stream", strings.NewReader(input))
				if err == nil || strings.Contains(err.Error()+w.Body.String(), "secret") {
					t.Fatalf("unsafe error: %v %s", err, w.Body.String())
				}
				out := w.Body.String()
				if strings.Contains(out, "data: [DONE]") || strings.Contains(out, `"type":"message_stop"`) || strings.Contains(out, `"type":"response.completed"`) {
					t.Fatalf("truncated response completed: %s", out)
				}
			}
			w := httptest.NewRecorder()
			_, err = c.Reply(w, string(upstream), "text/event-stream", &cloudFailReader{Reader: strings.NewReader(valid), err: errors.New("key=secret")})
			if err == nil || strings.Contains(err.Error()+w.Body.String(), "secret") {
				t.Fatalf("reader error leaked or succeeded: %v", err)
			}
			failed := &cloudFailWriter{header: http.Header{}}
			r, err := c.Reply(failed, string(upstream), "text/event-stream", strings.NewReader(valid))
			if err == nil || !r.OutputCommitted || strings.Contains(err.Error(), "secret") {
				t.Fatalf("writer failure = %+v %v", r, err)
			}
		}
	}
}

func TestCloudCodecInvalidInputsAndWholeFailures(t *testing.T) {
	for _, b := range []string{"null", "[]", `{`, `{}` + `{}`} {
		if _, err := NewCloudCodec("chat", []byte(b)); err == nil {
			t.Fatalf("accepted invalid request %q", b)
		}
	}
	if _, err := NewCloudCodec("unknown", []byte(`{}`)); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	c, _ := NewCloudCodec("chat", cloudRequestFixture(t, Chat, false))
	if _, err := c.Request("unknown", "model", "host"); err == nil {
		t.Fatal("unknown target protocol accepted")
	}
	for _, b := range []string{`{"choices":[{"message":{"content":"partial"}}]}`, `{"error":{"message":"key=secret"}}`, `{"choices":`} {
		w := httptest.NewRecorder()
		r, err := c.Reply(w, "chat", "application/json", strings.NewReader(b))
		if err == nil || r.OutputCommitted || strings.Contains(err.Error()+w.Body.String(), "secret") {
			t.Fatalf("whole failure = %+v %v %s", r, err, w.Body.String())
		}
	}
}

func TestCloudCodecCapturedNativeHostRequests(t *testing.T) {
	claude := fixture(t, "claude-code-2.1.296.json")
	codex := fixture(t, "codex-0.162.0.json")
	// The full Codex capture has native search and namespaced tools outside
	// routercloud's accepted subset. Keep only its plain function definitions;
	// metadata and other host options remain the captured values.
	var filtered Obj
	json.Unmarshal(codex, &filtered)
	var tools []Obj
	json.Unmarshal(filtered["tools"], &tools)
	var functions []Obj
	for _, tool := range tools {
		if Str(tool, "type") == "function" {
			functions = append(functions, tool)
		}
	}
	filtered["tools"] = Raw(functions)
	for _, tc := range []struct {
		protocol string
		body     []byte
	}{
		{"anthropic", claude}, {"responses", Raw(filtered)},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			codec, err := NewCloudCodec(tc.protocol, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			out, err := codec.Request(tc.protocol, "profile-pinned", "fixture.invalid")
			if err != nil {
				t.Fatal(err)
			}
			var expected, got Obj
			json.Unmarshal(tc.body, &expected)
			json.Unmarshal(out, &got)
			expected["model"] = Raw("profile-pinned")
			// Compare JSON values, without logging the captured long prompts.
			var expectedValue, gotValue any
			json.Unmarshal(Raw(expected), &expectedValue)
			json.Unmarshal(Raw(got), &gotValue)
			if !bytes.Equal(Raw(expectedValue), Raw(gotValue)) {
				t.Fatal("native capture changed fields other than the pinned model")
			}
			for _, destination := range cloudProtocols {
				if string(destination) != tc.protocol {
					if _, err := codec.Request(string(destination), "profile-pinned", "fixture.invalid"); err == nil {
						t.Fatalf("captured native host options were silently converted to %s", destination)
					}
				}
			}
		})
	}
}

func TestCloudCodecNativeHostFieldsCannotBeLost(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"chat", `{"metadata":{"task":"task-1"},"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", `{"metadata":{"task":"task-1"},"previous_response_id":null,"input":"hi"}`},
		{"responses", `{"input":"hi","client_metadata":{"session":"fixture"}}`},
		{"responses", `{"input":"hi","include":["reasoning.encrypted_content"]}`},
		{"responses", `{"input":"hi","text":{"verbosity":"low"}}`},
		{"responses", `{"input":[{"type":"reasoning","summary":[]},{"role":"user","content":"hi"}]}`},
		{"responses", `{"input":[{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"opaque"},{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"fixture"}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"cache_control":{"type":"ephemeral","ttl":"1h"}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[]}}`},
		{"anthropic", `{"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"tools":[{"name":"f","input_schema":{},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{},"cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok","cache_control":{"type":"ephemeral"}}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"ok","cache_control":{"type":"ephemeral"}}]}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":"reminder"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi","output_config":{"effort":"high"}}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed"}]},{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]},{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.protocol+tc.body, func(t *testing.T) {
			codec, err := NewCloudCodec(tc.protocol, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			out, err := codec.Request(tc.protocol, "pinned", "fixture.invalid")
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			json.Unmarshal(withModel([]byte(tc.body), "pinned"), &want)
			json.Unmarshal(out, &got)
			if !bytes.Equal(Raw(want), Raw(got)) {
				t.Fatal("native host options changed")
			}
			for _, destination := range cloudProtocols {
				if string(destination) != tc.protocol {
					if _, err := codec.Request(string(destination), "pinned", "fixture.invalid"); err == nil {
						t.Errorf("native host field lost converting to %s", destination)
					}
				}
			}
		})
	}
}

func TestCloudCodecNullPreviousResponseIsStateless(t *testing.T) {
	codec, err := NewCloudCodec("responses", []byte(`{"previous_response_id":null,"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range cloudProtocols {
		if _, err := codec.Request(string(destination), "pinned", "fixture.invalid"); err != nil {
			t.Errorf("null response reference should not require server state for %s: %v", destination, err)
		}
	}
	stateful, _ := NewCloudCodec("responses", []byte(`{"previous_response_id":"resp_state","input":"hi"}`))
	if _, err := stateful.Request("chat", "pinned", "fixture.invalid"); err == nil {
		t.Fatal("actual response reference silently discarded")
	}
}

func TestCloudCodecHostFieldGuardDoesNotInspectBusinessData(t *testing.T) {
	codec, err := NewCloudCodec("anthropic", []byte(`{"tools":[{"name":"f","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{"role":"system","output_config":{},"cache_control":"ordinary data"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"chat", "responses"} {
		if _, err := codec.Request(destination, "pinned", "fixture.invalid"); err != nil {
			t.Errorf("business field names mistaken for protocol extension: %v", err)
		}
	}
}

func cloudInterleavedFixture(t *testing.T, p Protocol) string {
	t.Helper()
	var frames []any
	switch p {
	case Chat:
		frames = []any{
			map[string]any{"id": "x", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "thinking", "tool_calls": []any{
				map[string]any{"index": 0, "id": "call_first", "function": map[string]any{"name": "run", "arguments": "{\"n\":"}},
				map[string]any{"index": 1, "id": "call_second", "function": map[string]any{"name": "run", "arguments": "{\"n\":"}},
			}}}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": "1}"}},
				map[string]any{"index": 1, "function": map[string]any{"arguments": "2}"}},
			}}}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}},
			"[DONE]",
		}
	case Anthropic:
		frames = []any{
			map[string]any{"type": "message_start", "message": map[string]any{"id": "x", "type": "message", "model": "m"}},
			map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call_first", "name": "run", "input": map[string]any{}}},
			map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "call_second", "name": "run", "input": map[string]any{}}},
			map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"n\":"}},
			map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"n\":"}},
			map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "1}"}},
			map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "2}"}},
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{"type": "content_block_stop", "index": 1},
			map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}},
			map[string]any{"type": "message_stop"},
		}
	case Responses:
		frames = []any{
			map[string]any{"type": "response.created", "response": map[string]any{"id": "x", "model": "m"}},
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "fc_a", "type": "function_call", "call_id": "call_first", "name": "run", "arguments": ""}},
			map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_b", "type": "function_call", "call_id": "call_second", "name": "run", "arguments": ""}},
			map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_a", "output_index": 0, "delta": "{\"n\":"},
			map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_b", "output_index": 1, "delta": "{\"n\":"},
			map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_a", "output_index": 0, "delta": "1}"},
			map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_b", "output_index": 1, "delta": "2}"},
			map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_a", "output_index": 0, "arguments": `{"n":1}`},
			map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_b", "output_index": 1, "arguments": `{"n":2}`},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "x", "status": "completed", "output": []any{}}},
		}
	}
	var b strings.Builder
	for _, frame := range frames {
		b.WriteString("data: ")
		if ProtocolText, ok := frame.(string); ok {
			b.WriteString(ProtocolText)
		} else {
			b.WriteString(CloudJSON(frame))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestCloudCodecInterleavedTools(t *testing.T) {
	for _, upstream := range cloudProtocols {
		for _, client := range cloudProtocols {
			for _, stream := range []bool{false, true} {
				t.Run(string(upstream)+"/"+string(client), func(t *testing.T) {
					c, _ := NewCloudCodec(string(client), cloudRequestFixture(t, client, stream))
					input := cloudInterleavedFixture(t, upstream)
					w := httptest.NewRecorder()
					_, err := c.Reply(w, string(upstream), "text/event-stream", strings.NewReader(input))
					if err != nil {
						t.Fatal(err, w.Body.String())
					}
					if client == upstream && stream {
						if w.Body.String() != input {
							t.Fatal("native stream changed")
						}
						return
					}
					res := cloudReadResult(t, client, stream, w.Body.Bytes())
					calls := map[string]string{}
					for _, p := range res.Parts {
						if p.Kind == ToolCall {
							calls[p.ID] = string(p.Args)
						}
					}
					if len(calls) != 2 || calls["call_first"] != `{"n":1}` || calls["call_second"] != `{"n":2}` {
						t.Fatalf("interleaved tools corrupted: %+v", res)
					}
				})
			}
		}
	}
}

func TestCloudCodecZeroUsageAndUnknownCounts(t *testing.T) {
	for _, protocol := range cloudProtocols {
		for _, stream := range []bool{false, true} {
			for _, upstreamStream := range []bool{false, true} {
				for _, usage := range []string{`{"input_tokens":0,"output_tokens":0}`, `{"input_tokens":0}`} {
					if protocol == Chat {
						usage = strings.ReplaceAll(strings.ReplaceAll(usage, "input_tokens", "prompt_tokens"), "output_tokens", "completion_tokens")
					}
					b := cloudResponseFixture(t, protocol)
					var object Obj
					json.Unmarshal(b, &object)
					object["usage"] = json.RawMessage(usage)
					input, typ := CloudJSON(object), "application/json"
					if upstreamStream {
						input, typ = cloudSSEFixture(t, protocol, []byte(input)), "text/event-stream"
						if !strings.Contains(usage, "output_tokens") && !strings.Contains(usage, "completion_tokens") {
							input = strings.ReplaceAll(input, `,"output_tokens":0`, "")
						}
					}
					c, _ := NewCloudCodec(string(protocol), cloudRequestFixture(t, protocol, stream))
					w := httptest.NewRecorder()
					r, err := c.Reply(w, string(protocol), typ, strings.NewReader(input))
					wantKnown := strings.Contains(usage, "output_tokens") || strings.Contains(usage, "completion_tokens")
					if err != nil || r.UsageKnown != wantKnown || r.Usage.Input != 0 || r.Usage.Output != 0 {
						t.Fatalf("zero usage lost: %+v, %v wantKnown=%v", r, err, wantKnown)
					}
				}
			}
		}
	}
}

func TestCloudCodecRefusesLossyTranslation(t *testing.T) {
	cases := []struct{ from, to, extra string }{
		{"chat", "responses", `"stop":["end"]`},
		{"chat", "anthropic", `"messages":[{"role":"user","content":"   "}]`},
		{"chat", "anthropic", `"messages":[{"role":"user","content":""}]`},
		{"chat", "responses", `"tools":[],"parallel_tool_calls":false`},
		{"responses", "chat", `"tools":[],"tool_choice":"none"`},
		{"chat", "responses", `"reasoning_effort":"none"`},
		{"responses", "chat", `"reasoning":{"summary":"detailed"}`},
		{"anthropic", "chat", `"thinking":{"type":"disabled"}`},
		{"anthropic", "responses", `"stop_sequences":["end"]`},
		{"chat", "anthropic", `"tools":[{"type":"function","function":{"name":"run","strict":true,"parameters":{"type":"object"}}}]`},
		{"responses", "chat", `"tools":[{"type":"function","name":"run","strict":true,"parameters":{"type":"object"}}]`},
		{"chat", "anthropic", `"service_tier":"priority"`},
		{"responses", "chat", `"service_tier":"default"`},
		{"chat", "anthropic", `"prompt_cache_key":"key"`},
		{"anthropic", "chat", `"output_config":{"effort":"high"}`},
		{"chat", "anthropic", `"temperature":0.2,"top_p":0.8`},
		{"anthropic", "chat", `"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"run","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"failed","is_error":true}]}]`},
		{"chat", "responses", `"messages":[{"role":"assistant","content":"text","reasoning_content":"thought"}]`},
		{"responses", "chat", `"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thought"}]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.from+"/"+tc.to+"/"+tc.extra, func(t *testing.T) {
			var input Obj
			json.Unmarshal(cloudRequestFixture(t, Protocol(tc.from), false), &input)
			var extra Obj
			json.Unmarshal([]byte("{"+tc.extra+"}"), &extra)
			for key, value := range extra {
				input[key] = value
			}
			c, err := NewCloudCodec(tc.from, []byte(CloudJSON(input)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Request(tc.to, "cloud-model", "example.test"); err == nil {
				t.Fatal("silently accepted a lossy translation")
			}
			if _, err := c.Request(tc.from, "cloud-model", "example.test"); err != nil {
				t.Fatal("native request rejected", err)
			}
		})
	}
}

func TestCloudCodecRefusesRewrittenToolHistories(t *testing.T) {
	for _, protocol := range cloudProtocols {
		for _, kind := range []string{"orphan", "duplicate_result", "duplicate_call", "pending_call"} {
			t.Run(string(protocol)+"/"+kind, func(t *testing.T) {
				call := Part{Kind: ToolCall, ID: "call_a", Name: "run", Args: json.RawMessage(`{}`)}
				result := Part{Kind: ToolResult, CallID: "call_a", Text: "done"}
				r := &Request{Model: "m", MaxTokens: 100}
				switch kind {
				case "orphan":
					r.Messages = []Message{{Role: "user", Parts: []Part{result}}}
				case "duplicate_result":
					r.Messages = []Message{{Role: "assistant", Parts: []Part{call}}, {Role: "user", Parts: []Part{result, result}}}
				case "duplicate_call":
					r.Messages = []Message{{Role: "assistant", Parts: []Part{call, call}}, {Role: "user", Parts: []Part{result}}}
				case "pending_call":
					r.Messages = []Message{{Role: "assistant", Parts: []Part{call}}}
				}
				// The builders deliberately tidy these histories, so construct
				// the original wire directly instead of building the fixture.
				var input map[string]any
				switch protocol {
				case Chat:
					var messages []any
					for _, m := range r.Messages {
						var calls []any
						for _, p := range m.Parts {
							if p.Kind == ToolCall {
								calls = append(calls, map[string]any{"id": p.ID, "type": "function", "function": map[string]any{"name": p.Name, "arguments": "{}"}})
							} else {
								messages = append(messages, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": p.Text})
							}
						}
						if len(calls) > 0 {
							messages = append(messages, map[string]any{"role": "assistant", "tool_calls": calls})
						}
					}
					input = map[string]any{"model": "m", "messages": messages}
				case Anthropic:
					var messages []any
					for _, m := range r.Messages {
						var parts []any
						for _, p := range m.Parts {
							if p.Kind == ToolCall {
								parts = append(parts, map[string]any{"type": "tool_use", "id": p.ID, "name": p.Name, "input": map[string]any{}})
							} else {
								parts = append(parts, map[string]any{"type": "tool_result", "tool_use_id": p.CallID, "content": p.Text})
							}
						}
						messages = append(messages, map[string]any{"role": m.Role, "content": parts})
					}
					input = map[string]any{"model": "m", "max_tokens": 100, "messages": messages}
				case Responses:
					var items []any
					for _, m := range r.Messages {
						for _, p := range m.Parts {
							if p.Kind == ToolCall {
								items = append(items, map[string]any{"type": "function_call", "call_id": p.ID, "name": p.Name, "arguments": "{}"})
							} else {
								items = append(items, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": p.Text})
							}
						}
					}
					input = map[string]any{"model": "m", "input": items}
				}
				c, err := NewCloudCodec(string(protocol), []byte(CloudJSON(input)))
				if err != nil {
					t.Fatal(err)
				}
				for _, target := range cloudProtocols {
					_, err := c.Request(string(target), "m", "example.test")
					if (err == nil) != (target == protocol) {
						t.Fatalf("target=%s history=%s error=%v", target, kind, err)
					}
				}
			})
		}
	}
	for _, tc := range []struct{ protocol, body string }{
		{"anthropic", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"}}]}`},
		{"responses", `{"model":"m","input":"hi","tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{"arg":{"type":"string","encrypted":false}}}}]}`},
	} {
		c, err := NewCloudCodec(tc.protocol, []byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range cloudProtocols {
			_, err := c.Request(string(target), "m", "example.test")
			if (err == nil) != (string(target) == tc.protocol) {
				t.Fatalf("lossy tools target=%s error=%v", target, err)
			}
		}
	}
}

func TestCloudCodecSSELimitsRawBytes(t *testing.T) {
	c, _ := NewCloudCodec("chat", cloudRequestFixture(t, Chat, false))
	for _, prefix := range []string{": comment\n", "data: x\n"} {
		// No blank line: readSSE otherwise accumulates an unbounded event
		// before calling the per-frame limit. Comments exercise raw bytes too.
		input := strings.Repeat(prefix, CloudReplyLimit/len(prefix)+2)
		w := httptest.NewRecorder()
		r, err := c.Reply(w, "chat", "text/event-stream", strings.NewReader(input))
		if err == nil || r.OutputCommitted {
			t.Fatalf("unbounded SSE prefix %q accepted: %+v %v", prefix, r, err)
		}
	}
}

func TestCloudCodecChatChoiceIndexEquivalence(t *testing.T) {
	for _, first := range []string{`"index":0,`, `"index":"0",`, ""} {
		input := `data: {"id":"x","choices":[{` + first + `"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"run","arguments":"{\"n\":"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":"0","delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":0.0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
		c, _ := NewCloudCodec("responses", cloudRequestFixture(t, Responses, false))
		w := httptest.NewRecorder()
		_, err := c.Reply(w, "chat", "text/event-stream", strings.NewReader(input))
		if err != nil {
			t.Fatal(first, err)
		}
		res := cloudReadResult(t, Responses, false, w.Body.Bytes())
		if len(res.Parts) != 1 || res.Parts[0].ID != "call_a" || string(res.Parts[0].Args) != `{"n":1}` {
			t.Fatalf("choice alias split the call: %+v", res)
		}
	}
}

func TestCloudCodecChatIgnoresOtherChoices(t *testing.T) {
	input := `data: {"id":"x","choices":[{"index":1,"delta":{"content":"chosen"}},{"index":0,"delta":{"tool_calls":[{"index":0,"id":"ignored","function":{"name":"run","arguments":"broken"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"choices":[{"index":"1","delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"run","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	c, _ := NewCloudCodec("responses", cloudRequestFixture(t, Responses, false))
	w := httptest.NewRecorder()
	_, err := c.Reply(w, "chat", "text/event-stream", strings.NewReader(input))
	if err != nil || strings.Contains(w.Body.String(), "ignored") || !strings.Contains(w.Body.String(), "call_a") {
		t.Fatalf("wrong Chat choice selected: %v %s", err, w.Body.String())
	}
}

func TestCloudCodecResponsesKeepsFullestValidArguments(t *testing.T) {
	input := cloudInterleavedFixture(t, Responses)
	input = strings.Replace(input, `"arguments":"{\"n\":1}"`, `"arguments":"{}"`, 1)
	input = strings.Replace(input, `"arguments":"{\"n\":2}"`, `"arguments":"broken"`, 1)
	c, _ := NewCloudCodec("anthropic", cloudRequestFixture(t, Anthropic, false))
	w := httptest.NewRecorder()
	_, err := c.Reply(w, "responses", "text/event-stream", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	res := cloudReadResult(t, Anthropic, false, w.Body.Bytes())
	if len(res.Parts) != 2 || string(res.Parts[0].Args) != `{"n":1}` || string(res.Parts[1].Args) != `{"n":2}` {
		t.Fatalf("valid delta arguments lost: %+v", res)
	}
}

func TestCloudCodecRejectsIllTypedReplyFields(t *testing.T) {
	cases := []struct{ protocol, body string }{
		{"chat", `{"choices":[{"message":{"role":"assistant","content":3},"finish_reason":"stop"}]}`},
		{"chat", `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":"bad","completion_tokens":1}}`},
		{"chat", `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":3,"function":{"name":"run","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{"anthropic", `{"type":"message","id":"x","content":[{"type":"text","text":3}],"stop_reason":"end_turn"}`},
		{"anthropic", `{"type":"message","id":"x","content":[{"type":"tool_use","id":"call_a","name":"run","input":[]}],"stop_reason":"tool_use"}`},
		{"anthropic", `{"type":"message","id":"x","content":[],"stop_reason":"end_turn","usage":{"input_tokens":"bad","output_tokens":1}}`},
		{"responses", `{"object":"response","id":"x","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":3}]}]}`},
		{"responses", `{"object":"response","id":"x","status":"completed","output":[{"type":"function_call","id":"fc_a","call_id":"call_a","name":"run","arguments":{}}]}`},
		{"responses", `{"object":"response","id":"x","status":"completed","output":[],"usage":{"input_tokens":"bad","output_tokens":1}}`},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			c, _ := NewCloudCodec("chat", cloudRequestFixture(t, Chat, stream))
			w := httptest.NewRecorder()
			r, err := c.Reply(w, tc.protocol, "application/json", strings.NewReader(tc.body))
			if err == nil || r.OutputCommitted {
				t.Fatalf("ill-typed JSON accepted: %+v %v %s", r, err, tc.body)
			}
		}
	}
	frames := []struct{ protocol, data string }{
		{"chat", `{"choices":[{"delta":{"content":3}}]}`},
		{"chat", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":3,"arguments":"{}"}}]}}]}`},
		{"chat", `{"choices":[],"usage":{"prompt_tokens":"bad","completion_tokens":1}}`},
		{"anthropic", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":3}}`},
		{"anthropic", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":3,"name":"run","input":{}}}`},
		{"anthropic", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":"bad"}}`},
		{"responses", `{"type":"response.output_text.delta","delta":3}`},
		{"responses", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_a","call_id":"a","name":"run","arguments":{}}}`},
		{"responses", `{"type":"response.completed","response":{"id":"x","status":"completed","output":[],"usage":{"input_tokens":"bad","output_tokens":1}}}`},
	}
	for _, tc := range frames {
		for _, client := range cloudProtocols {
			c, _ := NewCloudCodec(string(client), cloudRequestFixture(t, client, true))
			w := httptest.NewRecorder()
			input := "data: " + tc.data + "\n\n" + cloudSSEFixture(t, Protocol(tc.protocol), cloudResponseFixture(t, Protocol(tc.protocol)))
			_, err := c.Reply(w, tc.protocol, "text/event-stream", strings.NewReader(input))
			if err == nil || strings.Contains(w.Body.String(), "data: [DONE]") || strings.Contains(w.Body.String(), "response.completed") || strings.Contains(w.Body.String(), "message_stop") {
				t.Fatalf("ill-typed SSE accepted: %v %s; input=%s", err, w.Body.String(), tc.data)
			}
		}
	}
}

func TestCloudCodecRejectsTerminalWithoutValidStart(t *testing.T) {
	for _, tc := range []struct{ protocol, stream string }{
		{"chat", "data: {}\n\ndata: [DONE]\n\n"},
		{"chat", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n"},
		{"anthropic", "data: {\"type\":\"message_stop\"}\n\n"},
		{"anthropic", "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"responses", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"x\",\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"responses", "data: {\"type\":\"response.created\",\"response\":{}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"},
	} {
		c, _ := NewCloudCodec("chat", cloudRequestFixture(t, Chat, false))
		w := httptest.NewRecorder()
		r, err := c.Reply(w, tc.protocol, "text/event-stream", strings.NewReader(tc.stream))
		if err == nil || r.OutputCommitted {
			t.Fatalf("startless stream accepted: %+v %v %s", r, err, tc.stream)
		}
	}
}
