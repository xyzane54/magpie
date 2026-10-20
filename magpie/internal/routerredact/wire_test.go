package routerredact

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWireRequestsCoverTextAndNestedArguments(t *testing.T) {
	fixtures := map[Protocol]string{
		Chat:      `{"messages":[{"role":"system","content":"secret-system"},{"role":"developer","content":"secret-developer"},{"role":"user","content":[{"type":"text","text":"secret-user"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"nested\":[{\"secret-key\":\"secret-value\\n雪😀\\\"\\\\\",\"n\":9007199254740993,\"b\":true,\"z\":null}]}"}}]},{"role":"tool","tool_call_id":"call_1","content":"secret-result"}],"tools":[{"type":"function","function":{"name":"run","description":"secret-description","parameters":{"type":"object","properties":{"secret-key":{"type":"string"}}}}}]}`,
		Responses: `{"instructions":"secret-system","input":[{"role":"user","content":[{"type":"input_text","text":"secret-user"}]},{"type":"function_call","id":"item_1","call_id":"call_1","name":"run","arguments":"{\"nested\":[{\"secret-key\":\"secret-value\\n雪😀\\\"\\\\\",\"n\":9007199254740993,\"b\":true,\"z\":null}]}"},{"type":"function_call_output","call_id":"call_1","output":"secret-result"},{"type":"reasoning","summary":[{"type":"summary_text","text":"secret-summary"}],"content":[{"type":"reasoning_text","text":"secret-reasoning"}]}],"tools":[{"type":"function","name":"run","description":"secret-description","parameters":{"type":"object","properties":{"secret-key":{"type":"string"}}}}]}`,
		Anthropic: `{"system":[{"type":"text","text":"secret-system"}],"messages":[{"role":"user","content":"secret-user"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"run","input":{"nested":[{"secret-key":"secret-value\n雪😀\"\\","n":9007199254740993,"b":true,"z":null}]}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"secret-result"}]}]}],"tools":[{"name":"run","description":"secret-description","input_schema":{"type":"object","properties":{"secret-key":{"type":"string"}}}}]}`,
	}
	for protocol, fixture := range fixtures {
		t.Run(string(protocol), func(t *testing.T) {
			original := []byte(fixture)
			before := bytes.Clone(original)
			d, err := parseRequest(protocol, original)
			if err != nil {
				t.Fatal(err)
			}
			canonicalOriginal, err := d.render(d.texts)
			if err != nil {
				t.Fatal(err)
			}
			replacements := append([]string(nil), d.texts...)
			for i, text := range replacements {
				// Schema's property key is a protected structural text. All
				// ordinary content values in this fixture begin secret-.
				if text != "secret-key" {
					replacements[i] = strings.ReplaceAll(text, "secret-", "[SAFE]-")
				}
			}
			got, err := d.render(replacements)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, original) {
				t.Fatal("parse mutated caller bytes")
			}
			again, err := d.render(replacements)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatal("render mutated its private tree")
			}
			out, err := parseRequest(protocol, got)
			if err != nil {
				t.Fatalf("rendered request invalid: %v", err)
			}
			for _, text := range out.texts {
				if text != "secret-key" && strings.Contains(text, "secret-") {
					t.Fatalf("allowed text escaped extraction: %q", text)
				}
			}
			// Undo the fixture's text rewrite and compare JSON semantics,
			// including IDs, names, schema, large numbers and null/bool.
			undo := append([]string(nil), out.texts...)
			for i := range undo {
				undo[i] = strings.ReplaceAll(undo[i], "[SAFE]-", "secret-")
			}
			roundtrip, err := out.render(undo)
			if err != nil {
				t.Fatal(err)
			}
			wireAssertJSONEqual(t, canonicalOriginal, roundtrip)
		})
	}
}

func TestWireSchemaIsScannedAndNeverRewritten(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"ordinary"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"field":{"const":"sensitive-schema-value"}}}}}]}`)
	doc, err := parseRequest(Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	replacements := append([]string(nil), doc.texts...)
	found := false
	for i, text := range replacements {
		if text == "sensitive-schema-value" {
			found = true
			replacements[i] = "<SECRET:1>"
		}
	}
	if !found {
		t.Fatal("schema string escaped extraction")
	}
	if _, err := doc.render(replacements); err == nil || err.Error() != "wire_unsupported" {
		t.Fatalf("rewrote schema instead of blocking: %v", err)
	}
}

func TestWireArgumentKeysAndControlStringsBlockIfSensitive(t *testing.T) {
	fixtures := map[Protocol]string{
		Chat:      `{"service_tier":"sensitive-control","messages":[{"role":"assistant","tool_calls":[{"id":"call_A","type":"function","function":{"name":"run","arguments":"{\"sensitive-key\":\"ordinary\"}"}}]}]}`,
		Responses: `{"service_tier":"sensitive-control","input":[{"type":"function_call","call_id":"call_A","name":"run","arguments":"{\"sensitive-key\":\"ordinary\"}"}]}`,
		Anthropic: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_A","name":"run","input":{"sensitive-key":"ordinary"}}]}]}`,
	}
	for protocol, fixture := range fixtures {
		t.Run(string(protocol), func(t *testing.T) {
			doc, err := parseRequest(protocol, []byte(fixture))
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{"sensitive-key", "sensitive-control"} {
				if protocol == Anthropic && target == "sensitive-control" {
					continue
				}
				replacements := append([]string(nil), doc.texts...)
				found := false
				for i, text := range replacements {
					if text == target {
						found = true
						replacements[i] = "<SECRET:1>"
					}
				}
				if !found {
					t.Fatalf("protected text escaped extraction: %s", target)
				}
				if _, err := doc.render(replacements); err == nil || err.Error() != "wire_unsupported" {
					t.Fatalf("sensitive structural field was sent or rewritten: %v", err)
				}
			}
		})
	}
}

func TestWireCacheIdentityTracksRoleAndToolAssociation(t *testing.T) {
	for _, fixtures := range [][2]string{
		{`{"messages":[{"role":"user","content":"same text"}]}`, `{"messages":[{"role":"assistant","content":"same text"}]}`},
		{`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_A","type":"function","function":{"name":"run","arguments":"{\"text\":\"same text\"}"}}]}]}`, `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_B","type":"function","function":{"name":"run","arguments":"{\"text\":\"same text\"}"}}]}]}`},
	} {
		a, err := parseRequest(Chat, []byte(fixtures[0]))
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseRequest(Chat, []byte(fixtures[1]))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.texts, b.texts) {
			t.Fatal("fixture did not keep identical text")
		}
		if reflect.DeepEqual(a.identities, b.identities) {
			t.Fatal("history cache would reuse edited semantic position")
		}
		again, err := parseRequest(Chat, []byte(fixtures[0]))
		if err != nil || !reflect.DeepEqual(a.identities, again.identities) {
			t.Fatal("identities depend on map iteration order")
		}
	}
}

func TestWireUnsafeAndMalformedRequestsReject(t *testing.T) {
	cases := []struct {
		p Protocol
		b string
	}{
		{Chat, `{"messages":[{"role":"user","content":"private","content":"other"}]}`},
		{Chat, `{"messages":[{"role":"user","content":"private","unknown":"hidden"}]}`},
		{Chat, `{"messages":[{"role":"user","content":"\ud800"}]}`},
		{Chat, "{\"messages\":[{\"role\":\"user\",\"content\":\"\xff\"}]}"},
		{Chat, `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"run","arguments":"{\"k\":\"a\",\"k\":\"b\"}"}}]}]}`},
		{Chat, `{"messages":[{"role":"user","content":"private"}],"metadata":{"key":"private"}}`},
		{Chat, `{"messages":[{"role":"user","content":"private"}],"stop":["private"]}`},
		{Responses, `{"input":"private","previous_response_id":"remote"}`},
		{Responses, `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"opaque"}]}`},
		{Responses, `{"input":[{"type":"custom_tool_call","name":"run","call_id":"c","input":"opaque"}]}`},
		{Responses, `{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private","annotations":[{"type":"url_citation","url":"https://private.test","title":"private","start_index":0,"end_index":1}]}]}]}`},
		{Anthropic, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"signed"}]}]}`},
		{Anthropic, `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}]}`},
		{Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://private.test"}}]}]}`},
	}
	for _, tc := range cases {
		if _, err := parseRequest(tc.p, []byte(tc.b)); err == nil {
			t.Errorf("accepted unsafe request: %s", tc.b)
		} else if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "opaque") {
			t.Errorf("error leaked text: %v", err)
		}
	}
}

func TestWireNonstreamRestorePreservesToolTypesAndAssociation(t *testing.T) {
	original := "张三\n\"quoted\"\\tail 😀"
	snapshot := &Snapshot{mapping: map[string]string{"<PERSON:1>": original}}
	fixtures := map[Protocol]string{
		Chat:      `{"id":"reply","choices":[{"index":0,"message":{"role":"assistant","content":"hello <PERSON:1> <UNKNOWN:2>","tool_calls":[{"id":"call_A","type":"function","function":{"name":"<PERSON:1>","arguments":"{\"<PERSON:1>\":\"<PERSON:1>\",\"nested\":[{\"n\":9007199254740993,\"b\":true,\"z\":null,\"text\":\"<PERSON:1>\"}]}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":7}}`,
		Responses: `{"id":"reply","status":"completed","output":[{"type":"message","id":"msg_A","role":"assistant","content":[{"type":"output_text","text":"hello <PERSON:1> <UNKNOWN:2>"}]},{"type":"function_call","id":"item_A","call_id":"call_A","name":"<PERSON:1>","arguments":"{\"<PERSON:1>\":\"<PERSON:1>\",\"nested\":[{\"n\":9007199254740993,\"b\":true,\"z\":null,\"text\":\"<PERSON:1>\"}]}"}],"usage":{"input_tokens":0,"output_tokens":7}}`,
		Anthropic: `{"id":"reply","type":"message","role":"assistant","content":[{"type":"text","text":"hello <PERSON:1> <UNKNOWN:2>"},{"type":"tool_use","id":"call_A","name":"<PERSON:1>","input":{"<PERSON:1>":"<PERSON:1>","nested":[{"n":9007199254740993,"b":true,"z":null,"text":"<PERSON:1>"}]}}],"stop_reason":"tool_use","usage":{"input_tokens":0,"output_tokens":7}}`,
	}
	for protocol, fixture := range fixtures {
		t.Run(string(protocol), func(t *testing.T) {
			restored, err := snapshot.RestoreResponse(protocol, []byte(fixture))
			if err != nil {
				t.Fatal(err)
			}
			v, err := wireDecode(restored, 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			o := v.(map[string]any)
			var text, name, id string
			var args map[string]any
			switch protocol {
			case Chat:
				choice := o["choices"].([]any)[0].(map[string]any)
				m := choice["message"].(map[string]any)
				text = m["content"].(string)
				call := m["tool_calls"].([]any)[0].(map[string]any)
				function := call["function"].(map[string]any)
				name, id = function["name"].(string), call["id"].(string)
				decoded, err := wireDecode([]byte(function["arguments"].(string)), 32<<20)
				if err != nil {
					t.Fatal(err)
				}
				args = decoded.(map[string]any)
				if choice["finish_reason"] != "tool_calls" {
					t.Fatal("finish reason changed")
				}
			case Responses:
				items := o["output"].([]any)
				text = items[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
				call := items[1].(map[string]any)
				name, id = call["name"].(string), call["call_id"].(string)
				decoded, err := wireDecode([]byte(call["arguments"].(string)), 32<<20)
				if err != nil {
					t.Fatal(err)
				}
				args = decoded.(map[string]any)
			case Anthropic:
				blocks := o["content"].([]any)
				text = blocks[0].(map[string]any)["text"].(string)
				call := blocks[1].(map[string]any)
				name, id = call["name"].(string), call["id"].(string)
				args = call["input"].(map[string]any)
				if o["stop_reason"] != "tool_use" {
					t.Fatal("stop reason changed")
				}
			}
			if text != "hello "+original+" <UNKNOWN:2>" || name != "<PERSON:1>" || id != "call_A" {
				t.Fatalf("text/identity mismatch: text=%q name=%q id=%q", text, name, id)
			}
			if args["<PERSON:1>"] != original {
				t.Fatal("argument string value was not restored or property key was changed")
			}
			nested := args["nested"].([]any)[0].(map[string]any)
			if nested["text"] != original || nested["n"] != json.Number("9007199254740993") || nested["b"] != true || nested["z"] != nil {
				t.Fatalf("nested argument types changed: %#v", nested)
			}
			before, _ := wireDecode([]byte(fixture), 32<<20)
			if !reflect.DeepEqual(before.(map[string]any)["usage"], o["usage"]) {
				t.Fatal("usage semantics changed")
			}
		})
	}
}

func TestWireResponseErrorsAndMalformedArguments(t *testing.T) {
	snapshot := &Snapshot{mapping: map[string]string{"<PERSON:1>": "private"}}
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		body := []byte(`{"error":{"type":"upstream_error","message":"<PERSON:1>"}}`)
		got, err := snapshot.RestoreResponse(p, body)
		if err != nil {
			t.Fatal(err)
		}
		wireAssertJSONEqual(t, body, got)
	}
	bad := []string{
		`{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"text\":\"<PERSON:1>\",\"text\":\"other\"}"}}]}}]}`,
		`{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"[\"<PERSON:1>\"]"}}]}}]}`,
		`{"choices":[{"message":{"content":"\udfff"}}]}`,
		`{"choices":[{"message":{"content":"one","content":"two"}}]}`,
		`{"choices":[1]}`,
	}
	for _, body := range bad {
		if _, err := snapshot.RestoreResponse(Chat, []byte(body)); err == nil {
			t.Errorf("malformed response accepted: %s", body)
		}
	}
	snapshot.Close()
	if _, err := snapshot.RestoreResponse(Chat, []byte(`{"choices":[{"message":{"content":"<PERSON:1>"}}]}`)); err == nil {
		t.Fatal("released mapping restored response")
	}
}

func wireAssertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	a, err := wireDecode(want, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wireDecode(got, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON semantics changed:\nwant %s\ngot  %s", want, got)
	}
}
