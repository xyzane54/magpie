package routercloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateResponsesReasoningHistory(t *testing.T) {
	// The null/null item is the literal failing item in the isolated Qwen
	// Codex 0.161.0 second request. Other cases exercise the documented schema.
	for _, item := range []string{
		`{"type":"reasoning","id":"rs_fixture","summary":[],"content":null,"encrypted_content":null}`,
		`{"type":"reasoning","summary":[]}`,
		`{"type":"reasoning","summary":[],"encrypted_content":""}`,
		`{"type":"reasoning","summary":[],"content":[]}`,
		`{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Consider the result"}],"encrypted_content":null}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"Continue"}],"content":null,"encrypted_content":"opaque"}`,
	} {
		if err := ValidateInput("responses", []byte(`{"input":[`+item+`]}`)); err != nil {
			t.Errorf("legal reasoning history rejected: %s: %v", item, err)
		}
	}
}

func TestValidateResponsesNativeToolsAndHistory(t *testing.T) {
	for _, body := range []string{
		`{"input":"hi","tools":[{"type":"namespace","name":"multi_agent_v1","description":"Tools for spawning and managing sub-agents.","tools":[{"type":"function","name":"spawn_agent","strict":false,"parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","namespace":"multi_agent_v1","name":"spawn_agent"}}`,
		`{"input":[{"type":"additional_tools","id":"at_00000000-0000-0000-0000-000000000000","role":"developer","tools":[{"type":"namespace","name":"functions","description":"","tools":[{"type":"custom","name":"exec","format":{"type":"grammar","syntax":"lark","definition":"start: /[\\s\\S]+/"}}]}]},{"type":"custom_tool_call","id":"ct_1","call_id":"c1","namespace":"functions","name":"exec","input":"text(await tools.exec_command({cmd: 'pwd'}));"},{"type":"custom_tool_call_output","id":"cto_1","call_id":"c1","output":"/tmp/project"},{"type":"custom_tool_call","id":"ct_2","call_id":"c2","namespace":"functions","name":"exec","input":"// freeform, not JSON\ntext(42)"},{"type":"custom_tool_call_output","call_id":"c2","output":[{"type":"input_text","text":"42"}]}]}`,
		`{"input":[{"type":"function_call","call_id":"agent_1","namespace":"multi_agent_v1","name":"spawn_agent","arguments":"{\"message\":\"inspect\"}"},{"type":"function_call_output","id":"out_1","call_id":"agent_1","output":"agent-id"},{"type":"function_call","call_id":"agent_2","namespace":"multi_agent_v1","name":"wait_agent","arguments":"{\"targets\":[\"agent-id\"]}"},{"type":"function_call_output","call_id":"agent_2","output":"done"}]}`,
		`{"input":"hi","tools":[{"type":"custom","name":"freeform","format":{"type":"text"}},{"type":"custom","name":"regex","format":{"type":"grammar","syntax":"regex","definition":".*"}}],"tool_choice":{"type":"custom","name":"freeform"}}`,
		`{"input":"hi","tools":[{"type":"web_search","external_web_access":false,"search_context_size":"low","filters":{"allowed_domains":["example.com"],"blocked_domains":[]},"user_location":{"type":"approximate","country":"US","city":"Boston","region":"MA","timezone":"America/New_York"}}],"tool_choice":{"type":"web_search"},"include":["web_search_call.action.sources"]}`,
		`{"input":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"fixture","queries":["fixture"],"sources":[{"type":"url","url":"https://example.com"}]}},{"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"source","annotations":[{"type":"url_citation","start_index":0,"end_index":6,"title":"Example","url":"https://example.com"}]}]},{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://example.com"}},{"type":"web_search_call","id":"ws_3","status":"completed","action":{"type":"find_in_page","url":"https://example.com","pattern":"source"}}]}`,
		`{"input":"hi","tools":[{"type":"web_search_preview","search_context_size":"high","user_location":null}],"tool_choice":{"type":"web_search_preview"}}`,
	} {
		if err := ValidateInput("responses", []byte(body)); err != nil {
			t.Errorf("legal native tool history rejected: %s: %v", body, err)
		}
	}
}

func TestValidateResponsesNativeToolShapesFailBeforeOutbound(t *testing.T) {
	for i, body := range []string{
		`{"input":[{"type":"reasoning","summary":[],"encrypted_content":false}]}`,
		`{"input":[{"type":"reasoning","summary":[],"encrypted_content":[]}]}`,
		`{"input":[{"type":"additional_tools","role":"user","tools":[]}]}`,
		`{"input":[{"type":"reasoning","summary":[],"content":"plain"}]}`,
		`{"input":[{"type":"reasoning","summary":[],"content":[{"type":"output_text","text":"wrong"}]}]}`,
		`{"input":[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"x","unknown":true}]}]}`,
		`{"input":"hi","tools":[{"type":"namespace","name":"n","tools":[{"type":"namespace","name":"nested","tools":[]}]}]}`,
		`{"input":"hi","tools":[{"type":"namespace","name":"n","tools":[{"type":"web_search"}]}]}`,
		`{"input":"hi","tools":[{"type":"namespace","name":"n","tools":null}]}`,
		`{"input":"hi","tools":[{"type":"custom","name":"f","parameters":{}}]}`,
		`{"input":"hi","tools":[{"type":"custom","name":"f","format":{"type":"grammar","syntax":"unknown","definition":".*"}}]}`,
		`{"input":[{"type":"additional_tools","tools":[{"type":"unknown"}]}]}`,
		`{"input":[{"type":"additional_tools","tools":[],"unknown":true}]}`,
		`{"input":[{"type":"function_call","call_id":"c","namespace":42,"name":"f","arguments":"{}"}]}`,
		`{"input":[{"type":"custom_tool_call","call_id":"c","name":"f","input":{}}]}`,
		`{"input":[{"role":"user","phase":"commentary","content":"hi"}]}`,
		`{"input":[{"role":"assistant","phase":"unknown","content":"hi"}]}`,
		`{"input":[{"type":"custom_tool_call_output","call_id":"c","output":42}]}`,
		`{"input":"hi","tools":[{"type":"web_search","filters":{"allowed_domains":[42]}}]}`,
		`{"input":"hi","tools":[{"type":"web_search","user_location":{"type":"exact","country":"US"}}]}`,
		`{"input":"hi","tools":[{"type":"web_search","unknown":true}]}`,
		`{"input":[{"type":"web_search_call","id":"ws","status":"completed","action":{"type":"open_page","url":42}}]}`,
		`{"input":[{"type":"web_search_call","id":"ws","status":"completed","action":{"type":"search","sources":[{"type":"url","url":"https://example.com","secret":true}]}}]}`,
		`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"x","annotations":[{"type":"url_citation","start_index":2,"end_index":1,"url":"https://example.com","title":"x"}]}]}]}`,
		`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"x","annotations":[{"type":"url_citation","start_index":0.5,"end_index":1,"url":"https://example.com","title":"x"}]}]}]}`,
		`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"x","annotations":[{"type":"url_citation","start_index":0,"end_index":1,"url":"https://example.com","title":"x","unknown":true}]}]}]}`,
	} {
		for _, target := range []string{"responses", "chat", "anthropic"} {
			t.Run(fmt.Sprintf("%s/%d", target, i), func(t *testing.T) {
				calls := 0
				executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response(200, whole(target, true)), nil
				})}
				result, err := executor.Execute(context.Background(), httptest.NewRecorder(), Request{
					"responses", []byte(body), configured("http://fixture.invalid/v1", target),
				})
				failure, ok := err.(*Error)
				if !ok || failure.Code != "unsupported_input" || calls != 0 || result.Attempts != 0 {
					t.Fatalf("invalid native structure escaped boundary: result=%+v calls=%d error=%v", result, calls, err)
				}
			})
		}
	}
}
