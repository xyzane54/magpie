package cloudcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCodex0161CapturedNativeRequests(t *testing.T) {
	for _, name := range []string{"direct-default", "direct-custom", "direct-tools", "direct-tools-2", "direct-tools-3", "direct-tools-4", "executed-custom-2", "executed-multi-4", "executed-goals-5"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile("testdata/codex-0.161.0/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			codec, err := NewCloudCodec("responses", body)
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.Request("responses", "pinned", "fixture.invalid")
			if err != nil {
				t.Fatal(err)
			}
			var want, result Obj
			json.Unmarshal(body, &want)
			json.Unmarshal(got, &result)
			want["model"] = Raw("pinned")
			if !bytes.Equal(Raw(want), Raw(result)) {
				t.Fatal("native request changed besides bound model")
			}
			for _, destination := range []string{"chat", "anthropic"} {
				_, err := codec.Request(destination, "pinned", "fixture.invalid")
				var capability *CapabilityError
				if !errors.As(err, &capability) {
					t.Fatalf("expected explicit capability error, got %v", err)
				}
			}
		})
	}
}

// This generated upstream fixture is intentionally separate from the real CLI
// captures above. It exercises wire preservation, not vendor certification.
const enhancedNativeReply = `{"id":"resp_full","object":"response","model":"pinned","status":"completed","opaque":{"vendor":[1,2]},"usage":{"input_tokens":11,"output_tokens":7},"output":[
{"type":"reasoning","id":"rs_null","summary":[],"content":null,"encrypted_content":null},
{"type":"reasoning","id":"rs_opaque","summary":[{"type":"summary_text","text":"summary"}],"content":[{"type":"reasoning_text","text":"thought"}],"encrypted_content":"opaque-secret"},
{"type":"function_call","id":"fc_1","call_id":"call_1","namespace":"agent_a","name":"run","arguments":"{\"n\":1}","status":"completed","vendor_field":"keep"},
{"type":"function_call","id":"fc_2","call_id":"call_2","namespace":"agent_b","name":"run","arguments":"{\"n\":2}","status":"completed"},
{"type":"custom_tool_call","id":"ctc_1","call_id":"call_custom","namespace":"functions","name":"exec","input":"text(\"hello\");\n// not JSON\n$literal","status":"completed"},
{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"official docs","sources":[{"type":"url","url":"https://example.test/doc"}]}},
{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Found documentation.","annotations":[{"type":"url_citation","start_index":0,"end_index":20,"url":"https://example.test/doc","title":"Documentation"}]}]}
]}`

func TestCodexNativeResponsesPreserveAllOutputModes(t *testing.T) {
	for _, clientStream := range []bool{false, true} {
		for _, upstreamStream := range []bool{false, true} {
			t.Run(CloudJSON([]bool{clientStream, upstreamStream}), func(t *testing.T) {
				codec, err := NewCloudCodec("responses", Raw(map[string]any{"input": "fixture", "stream": clientStream}))
				if err != nil {
					t.Fatal(err)
				}
				body, contentType := enhancedNativeReply, "application/json"
				if upstreamStream {
					body, contentType = cloudSSEFixture(t, Responses, []byte(body)), "text/event-stream"
				}
				writer := httptest.NewRecorder()
				reply, err := codec.Reply(writer, "responses", contentType, strings.NewReader(body))
				if err != nil || !reply.UsageKnown || reply.Usage.Input != 11 || reply.Usage.Output != 7 {
					t.Fatalf("reply=%+v err=%v", reply, err)
				}
				result := writer.Body.Bytes()
				var doneItems []json.RawMessage
				if clientStream {
					err := ReadSSE(bytes.NewReader(result), func(_, data string) error {
						var event Obj
						json.Unmarshal([]byte(data), &event)
						if Str(event, "type") == "response.output_item.done" {
							doneItems = append(doneItems, event["item"])
						}
						if Str(event, "type") == "response.completed" {
							result = event["response"]
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				var want, got any
				json.Unmarshal([]byte(enhancedNativeReply), &want)
				json.Unmarshal(result, &got)
				if !reflect.DeepEqual(want, got) {
					t.Fatalf("native response lost fields: %s", result)
				}
				if clientStream {
					var whole struct {
						Output []json.RawMessage `json:"output"`
					}
					json.Unmarshal([]byte(enhancedNativeReply), &whole)
					var gotItems, wantItems any
					json.Unmarshal(Raw(doneItems), &gotItems)
					json.Unmarshal(Raw(whole.Output), &wantItems)
					if !reflect.DeepEqual(gotItems, wantItems) {
						t.Fatal("stream completed item IDs, order or fields changed")
					}
				}
			})
		}
	}
}

func TestNativeResponsesEmptyTerminalRetainsCompletedItems(t *testing.T) {
	stream := cloudSSEFixture(t, Responses, []byte(enhancedNativeReply))
	var frames strings.Builder
	ReadSSE(strings.NewReader(stream), func(name, data string) error {
		var event Obj
		json.Unmarshal([]byte(data), &event)
		if Str(event, "type") == "response.completed" {
			var response Obj
			json.Unmarshal(event["response"], &response)
			response["output"] = Raw([]any{})
			event["response"] = Raw(response)
			data = CloudJSON(event)
		}
		frames.WriteString("event: " + name + "\ndata: " + data + "\n\n")
		return nil
	})
	codec, _ := NewCloudCodec("responses", []byte(`{"input":"fixture"}`))
	w := httptest.NewRecorder()
	if _, err := codec.Reply(w, "responses", "text/event-stream", strings.NewReader(frames.String())); err != nil {
		t.Fatal(err)
	}
	var want, got any
	json.Unmarshal([]byte(enhancedNativeReply), &want)
	json.Unmarshal(w.Body.Bytes(), &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("lost terminal items: %s", w.Body.String())
	}
}

func TestResponseCapabilitiesInspectOnlyProtocolContainers(t *testing.T) {
	cases := []struct{ body, capability string }{
		{`{"input":"hi","tools":[{"type":"namespace","name":"agents","tools":[]}]}`, "namespace"},
		{`{"input":"hi","tools":[{"type":"custom","name":"exec"}]}`, "custom"},
		{`{"input":[{"type":"additional_tools","tools":[]}]}`, "additional_tools"},
		{`{"input":"hi","tools":[{"type":"web_search_preview"}]}`, "web_search"},
		{`{"input":[{"type":"function_call","namespace":"agents"}]}`, "namespace"},
		{`{"input":[{"type":"custom_tool_call_output","call_id":"c","output":"freeform"}]}`, "custom"},
		{`{"input":[{"type":"message","content":[{"type":"output_text","text":"citation","annotations":[{"type":"url_citation"}]}]}]}`, "web_search"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			var capability *CapabilityError
			if err := ValidateCapabilities("responses", []byte(tc.body), nil); !errors.As(err, &capability) || capability.Code != "capability_"+tc.capability {
				t.Fatalf("missing capability classification: %v", err)
			}
			if err := ValidateCapabilities("responses", []byte(tc.body), []string{tc.capability}); err != nil {
				t.Fatal(err)
			}
		})
	}
	data := []byte(`{"input":[{"type":"function_call","arguments":"{\"type\":\"web_search\",\"namespace\":\"secret\"}"},{"type":"function_call_output","output":"namespace"}],"tools":[{"type":"function","name":"f","parameters":{"properties":{"type":{"const":"custom"},"namespace":{"type":"string"}}}}]}`)
	if features := ResponseCapabilities(data); len(features) != 0 {
		t.Fatalf("business data interpreted as capabilities: %v", features)
	}
}

func TestCloudCodecExplicitCrossProtocolCapabilityErrors(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":null,"content":null}]}`, "capability_reasoning_history"},
		{`{"input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-private"}]}`, "capability_reasoning_history"},
		{`{"input":"hi","reasoning":{"effort":"medium"}}`, "capability_reasoning_configuration"},
		{`{"input":"hi","include":["reasoning.encrypted_content"]}`, "capability_metadata"},
		{`{"input":"hi","client_metadata":{"private":"opaque-private"}}`, "capability_metadata"},
		{`{"input":[{"type":"function_call","id":"fc_1","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","id":"fco_1","call_id":"c","output":"result"}]}`, "capability_item_identity"},
		{`{"input":"hi","tools":[{"type":"namespace","name":"a","tools":[{"type":"function","name":"f","parameters":{}}]}]}`, "capability_namespace"},
		{`{"input":"hi","tools":[{"type":"custom","name":"f"}]}`, "capability_custom"},
		{`{"input":"hi","tools":[{"type":"web_search"}]}`, "capability_web_search"},
		{`{"input":[{"type":"additional_tools","tools":[]},{"role":"user","content":"hi"}]}`, "capability_additional_tools"},
	} {
		codec, err := NewCloudCodec("responses", []byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		_, err = codec.Request("chat", "pinned", "fixture.invalid")
		var capability *CapabilityError
		if !errors.As(err, &capability) || capability.Code != tc.code || strings.Contains(err.Error(), "opaque-private") {
			t.Fatalf("capability error=%v want %s", err, tc.code)
		}
		if _, err := codec.Request("responses", "pinned", "fixture.invalid"); err != nil {
			t.Fatal(err)
		}
	}
}
