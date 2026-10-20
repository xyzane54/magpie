package routercloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestValidateInputTextAndFunctionTurns(t *testing.T) {
	requests := map[string]string{
		"chat": `{"model":"m","messages":[
			{"role":"system","content":"Follow instructions"},
			{"role":"user","content":[{"type":"text","text":"Look up the value"}]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"image\"}"}}]},
			{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"42"}]},
			{"role":"assistant","content":"The value is 42"},{"role":"user","content":"Thanks"}],
			"tools":[{"type":"function","function":{"name":"lookup","description":"Get data","parameters":{"type":"object","properties":{"image":{"type":"string"}}},"strict":true}}],
			"tool_choice":{"type":"function","function":{"name":"lookup"}},"max_completion_tokens":100,"stream":true,"temperature":0.5,"top_p":1,"stop":["END"],"parallel_tool_calls":false}`,
		"responses": `{"model":"m","instructions":"Follow instructions","input":[
			{"role":"developer","content":"Use tools"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Look up the value"}]},
			{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"42"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The value is 42"}]},
			{"role":"user","content":"Thanks"}],
			"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":false}],
			"tool_choice":"auto","max_output_tokens":100,"stream":false,"store":false,"background":false,"reasoning":{"effort":"medium","summary":"auto"}}`,
		"anthropic": `{"model":"m","system":[{"type":"text","text":"Use tools"}],"messages":[
			{"role":"user","content":"Look up the value"},
			{"role":"assistant","content":[{"type":"text","text":"Looking"},{"type":"tool_use","id":"c1","name":"lookup","input":{"file":"business data","image":{"url":"ordinary argument"}}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"42"}],"is_error":false}]},
			{"role":"assistant","content":"The value is 42"},{"role":"user","content":"Thanks"}],
			"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"max_tokens":100,"stream":true,"stop_sequences":["END"]}`,
	}
	for protocol, body := range requests {
		t.Run(protocol, func(t *testing.T) {
			if err := ValidateInput(protocol, []byte(body)); err != nil {
				t.Fatalf("valid text and function conversation rejected: %v", err)
			}
		})
	}
}

func TestValidateInputSimpleRequests(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"chat", `{"messages":[{"role":"user","content":""}]}`},
		{"chat", `{"messages":[{"role":"assistant","content":"","reasoning_content":"Consider it"}],"tools":[]}`},
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true},"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":true}}}`},
		{"responses", `{"input":"Hello","tools":[]}`},
		{"responses", `{"input":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Considering"}]},{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[],"logprobs":[]}]}],"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":true}}}`},
		{"responses", `{"input":[{"type":"function_call","id":"fc_1","status":"completed","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"Hello"}],"tools":[]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"Hello"}],"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"Hello"}],"thinking":{"type":"disabled"}}`},
	} {
		t.Run(tc.protocol+tc.body, func(t *testing.T) {
			if err := ValidateInput(tc.protocol, []byte(tc.body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateInputRejectsMalformedRequests(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic", "unknown"} {
		for _, body := range []string{"", "null", "[]", `"secret"`, "true", "42", "{", "{}", `{} {}`, `{"input":"hi","input":"bye"}`, `{"messages":null}`, `{"messages":{}}`} {
			t.Run(protocol+body, func(t *testing.T) {
				if err := ValidateInput(protocol, []byte(body)); err == nil {
					t.Fatal("malformed model input accepted")
				}
			})
		}
	}
}

func TestValidateInputRejectsUnsupportedContentAtEveryPosition(t *testing.T) {
	positions := []struct{ protocol, request string }{
		{"chat", `{"messages":[{"role":"user","content":[%s]}]}`},
		{"chat", `{"messages":[{"role":"system","content":[%s]}]}`},
		{"chat", `{"messages":[{"role":"assistant","content":[%s]}]}`},
		{"chat", `{"messages":[{"role":"tool","tool_call_id":"c1","content":[%s]}]}`},
		{"responses", `{"input":[{"role":"user","content":[%s]}]}`},
		{"responses", `{"input":[{"role":"assistant","content":[%s]}]}`},
		{"responses", `{"input":[{"type":"function_call_output","call_id":"c1","output":[%s]}]}`},
		{"anthropic", `{"system":[%s],"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[%s]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[%s]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[%s]}]}]}`},
	}
	blocks := []string{
		`{"type":"image_url","image_url":{"url":"https://secret.invalid/image"}}`,
		`{"type":"image","source":{"type":"base64","data":"secret"}}`,
		`{"type":"input_image","image_url":"secret"}`,
		`{"type":"input_audio","input_audio":{"data":"secret","format":"wav"}}`,
		`{"type":"audio","source":{"data":"secret"}}`,
		`{"type":"video","video_url":"secret"}`,
		`{"type":"input_video","video_url":"secret"}`,
		`{"type":"file","file":{"file_id":"secret"}}`,
		`{"type":"input_file","file_id":"secret"}`,
		`{"type":"document","source":{"data":"secret"}}`,
		`{"type":"web_search_tool_result","content":[]}`,
		`{"type":"text","text":"hi","secret_unknown_field":"secret"}`,
		`{"type":"text","text":{"source":"secret"}}`,
		`{"type":"input_text","text":null}`,
		`{"type":"text"}`, `{}`, `null`, `42`, `"secret"`,
	}
	for i, position := range positions {
		for j, block := range blocks {
			t.Run(fmt.Sprintf("%s-position%d-block%d", position.protocol, i, j), func(t *testing.T) {
				body := fmt.Sprintf(position.request, block)
				if err := ValidateInput(position.protocol, []byte(body)); err == nil {
					t.Fatalf("unsupported content accepted: %s", body)
				}
			})
		}
	}
}

func TestValidateInputRejectsFieldsLostInTranslation(t *testing.T) {
	tests := []struct{ protocol, body string }{
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"web_search_options":{}}`},
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"modalities":["text","audio"]}`},
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"audio":{"voice":"alloy"}}`},
		{"chat", `{"messages":[{"role":"user","content":"hi","name":"speaker"}]}`},
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true,"unknown":true}}`},
		{"chat", `{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":[]}}}`},
		{"chat", `{"messages":[{"role":"user","content":"hi","tool_calls":[]}]}`},
		{"chat", `{"messages":[{"role":"assistant","content":"hi","reasoning_details":[]}]}`},
		{"responses", `{"input":"hi","previous_response_id":"resp_secret"}`},
		{"responses", `{"input":"hi","previous_response_id":false}`},
		{"responses", `{"input":"hi","conversation":"conv_secret"}`},
		{"responses", `{"input":"hi","background":true}`},
		{"responses", `{"input":"hi","background":"false"}`},
		{"responses", `{"input":"hi","store":true}`},
		{"responses", `{"input":[{"type":"item_reference","id":"secret"}]}`},
		{"responses", `{"input":[{"type":"reasoning","encrypted_content":42,"summary":[]}]}`},
		{"responses", `{"input":[{"type":"web_search_call","id":"secret"}]}`},
		{"responses", `{"input":[{"type":"message","role":"user","content":"hi","unknown":"secret"}]}`},
		{"responses", `{"input":"hi","reasoning":{"effort":"unknown"}}`},
		{"responses", `{"input":"hi","reasoning":{"effort":"high","unknown":true}}`},
		{"responses", `{"input":"hi","text":{"format":{"type":"json_schema","name":"answer","schema":null}}}`},
		{"responses", `{"input":[{"role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[{"type":"url_citation","url":"secret"}]}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":null}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi","citations":[]}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"mcp_servers":[]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"context_management":{}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive","budget_tokens":100}}`},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%s-%d", tc.protocol, i), func(t *testing.T) {
			if err := ValidateInput(tc.protocol, []byte(tc.body)); err == nil {
				t.Fatal("field with unpreserved semantics accepted")
			}
		})
	}
}

func TestValidateInputRejectsInvalidToolsAndTypes(t *testing.T) {
	for _, tc := range []struct{ protocol, prefix string }{
		{"chat", `{"messages":[{"role":"user","content":"hi"}]`},
		{"responses", `{"input":"hi"`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}]`},
	} {
		for i, fields := range []string{
			`,"tools":null`, `,"tools":{}`, `,"tools":[null]`, `,"tools":[{}]`,
			`,"tools":[{"type":"web_search","name":"search","input_schema":{}}]`,
			`,"tools":[{"type":"function","name":"f","parameters":[],"function":{"name":"f","parameters":[]}}]`,
			`,"tools":[{"type":"code_interpreter","name":"code","input_schema":{}}]`,
			`,"tool_choice":42`, `,"tool_choice":{}`, `,"tool_choice":"web_search"`,
			`,"model":null`, `,"model":42`, `,"model":" "`, `,"stream":"true"`,
			`,"temperature":{}`, `,"top_p":"1"`, `,"unknown":"secret"`,
			`,"temperature":1e1000`,
		} {
			t.Run(fmt.Sprintf("%s-%d", tc.protocol, i), func(t *testing.T) {
				if err := ValidateInput(tc.protocol, []byte(tc.prefix+fields+`}`)); err == nil {
					t.Fatalf("invalid fields accepted: %s", fields)
				}
			})
		}
	}
	for _, tc := range []struct{ protocol, body string }{
		{"chat", `{"messages":[{"role":"unknown","content":"hi"}]}`},
		{"chat", `{"messages":[{"role":"tool","content":"hi"}]}`},
		{"chat", `{"messages":[{"role":"user","content":null}]}`},
		{"chat", `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"bad"}}]}]}`},
		{"chat", `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"[]"}}]}]}`},
		{"chat", `{"messages":[{"role":"user","content":[]}],"max_tokens":0}`},
		{"responses", `{"input":[]}`},
		{"responses", `{"input":{}}`},
		{"responses", `{"input":[{"type":"function_call","call_id":"c1","name":"f","arguments":{}}]}`},
		{"responses", `{"input":[{"type":"function_call_output","output":"hi"}]}`},
		{"responses", `{"input":[{"type":"function_call_output","call_id":"c1","id":null,"output":"hi"}]}`},
		{"responses", `{"input":[{"type":"function_call_output","call_id":"c1","id":42,"output":"hi"}]}`},
		{"responses", `{"input":[{"type":"function_call_output","call_id":"c1","id":" ","output":"hi"}]}`},
		{"responses", `{"input":"hi","max_output_tokens":1.5}`},
		{"anthropic", `{"messages":[{"role":"developer","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":null}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":null}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"tool_result","tool_use_id":"nested","content":"secret"}]}]}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":-1}`},
	} {
		t.Run(tc.protocol+tc.body, func(t *testing.T) {
			if err := ValidateInput(tc.protocol, []byte(tc.body)); err == nil {
				t.Fatal("invalid protocol structure accepted")
			}
		})
	}
}

func TestValidateInputBodyBoundaries(t *testing.T) {
	const prefix = `{"input":"`
	const suffix = `"}`
	limitBody := prefix + strings.Repeat("a", (8<<20)-len(prefix)-len(suffix)) + suffix
	if err := ValidateInput("responses", []byte(limitBody)); err != nil {
		t.Fatalf("body at 8 MiB limit rejected: %v", err)
	}
	if err := ValidateInput("responses", []byte(limitBody+" ")); err == nil {
		t.Fatal("body exceeding 8 MiB limit accepted")
	}
	if err := ValidateInput("responses", []byte("{\"input\":\"\xff\"}")); err == nil {
		t.Fatal("invalid UTF-8 silently changed")
	}
	if err := ValidateInput("responses", []byte(`{"input":[{"role":"user","role":"assistant","content":"hi"}]}`)); err == nil {
		t.Fatal("duplicate structure key accepted")
	}
}

func TestValidateInputErrorsContainNoCallerData(t *testing.T) {
	secret := "do-not-expose-prompt-or-credential"
	for _, body := range []string{
		`{"input":"` + secret + `","` + secret + `":true}`,
		`{"input":[{"type":"` + secret + `"}]}`,
		`{"input":"` + secret + ``,
	} {
		err := ValidateInput("responses", []byte(body))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

func TestValidateInputCapturedHostSubsets(t *testing.T) {
	// The existing workdir tests document these as requests captured from
	// sandboxed Claude Code 2.1.296 and Codex 0.162.0, not a live-host test.
	claude, err := os.ReadFile("../gateway/testdata/workdir/claude-code-2.1.296.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput("anthropic", claude); err != nil {
		t.Fatalf("Claude Code 2.1.296 captured native shape rejected: %v", err)
	}
	codex, err := os.ReadFile("../gateway/testdata/workdir/codex-0.162.0.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput("responses", codex); err != nil {
		t.Fatalf("unfiltered captured Codex native search/namespaces rejected: %v", err)
	}
	var subset map[string]json.RawMessage
	if err := json.Unmarshal(codex, &subset); err != nil {
		t.Fatal(err)
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(subset["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	var functions []map[string]json.RawMessage
	for _, tool := range tools {
		if string(tool["type"]) == `"function"` {
			functions = append(functions, tool)
		}
	}
	if len(functions) == 0 || len(functions) == len(tools) {
		t.Fatal("capture did not exercise the intended filtered function subset")
	}
	subset["tools"], _ = json.Marshal(functions)
	filtered, _ := json.Marshal(subset)
	if err := ValidateInput("responses", filtered); err != nil {
		t.Fatalf("filtered Codex text/function subset rejected: %v", err)
	}
}

func TestValidateInputNativeHostHistory(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"responses", `{"previous_response_id":null,"input":"hi"}`},
		{"chat", `{"metadata":{"task":"task-1"},"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", `{"previous_response_id":null,"metadata":{"task":"task-1"},"input":"hi"}`},
		{"responses", `{"client_metadata":{"session":"fixture"},"include":["reasoning.encrypted_content"],"reasoning":{"effort":"low","summary":null,"context":"all_turns","mode":"standard"},"text":{"verbosity":"low"},"input":[{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"opaque-fixture"},{"role":"user","content":"continue"}]}`},
		{"responses", `{"input":[{"type":"reasoning","summary":[]},{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"cache_control":{"type":"ephemeral","ttl":"5m"}}`},
		{"anthropic", `{"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"f","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"5m"}}],"metadata":{"user_id":"{\"session_id\":\"fixture\"}"},"thinking":{"type":"adaptive","display":"updates"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"system","content":"reminder","output_config":{"effort":"medium"}},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed-fixture"},{"type":"redacted_thinking","data":"opaque-fixture"},{"type":"tool_use","id":"c1","name":"f","input":{},"cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"ok","cache_control":{"type":"ephemeral"}}],"cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`},
	} {
		if err := ValidateInput(tc.protocol, []byte(tc.body)); err != nil {
			t.Errorf("native host history %s rejected: %v", tc.protocol, err)
		}
	}
}

func TestValidateInputHostFieldsRemainStrict(t *testing.T) {
	for _, tc := range []struct {
		protocol, base string
		fields         []string
	}{
		{"responses", `{"input":"hi"`, []string{
			`,"metadata":null`, `,"metadata":[]`, `,"metadata":{"task":42}`, `,"metadata":{"task":{}}`,
			`,"previous_response_id":""`, `,"previous_response_id":"resp_state"`, `,"previous_response_id":{}`,
			`,"client_metadata":null`, `,"client_metadata":[]`, `,"include":"reasoning.encrypted_content"`,
			`,"client_metadata":{"thread_id":42}`, `,"client_metadata":{"session_id":false}`,
			`,"client_metadata":{"x-codex-turn-metadata":{"model":"unexpected"}}`,
			`,"include":["unknown.sources"]`, `,"include":[42]`,
			`,"text":{"verbosity":true}`, `,"text":{"verbosity":"extreme"}`, `,"text":{"verbosity":"low","unknown":true}`,
			`,"reasoning":{"summary":42}`, `,"reasoning":{"context":"unknown"}`, `,"reasoning":{"mode":"unknown"}`,
		}},
		{"chat", `{"messages":[{"role":"user","content":"hi"}]`, []string{
			`,"metadata":null`, `,"metadata":[]`, `,"metadata":{"task":42}`, `,"metadata":{"task":{}}`,
		}},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}]`, []string{
			`,"cache_control":null`, `,"cache_control":{"type":"ephemeral","ttl":"2h"}`,
			`,"metadata":null`, `,"metadata":{"user_id":42}`, `,"metadata":{"unknown":"x"}`,
			`,"thinking":{"type":"adaptive","display":true}`, `,"thinking":{"type":"adaptive","display":"unknown"}`,
			`,"context_management":null`, `,"context_management":{"edits":{}}`,
			`,"context_management":{"edits":[{"type":"clear_tool_uses_20250919","keep":"all"}]}`,
			`,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":1}]}`,
			`,"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all","unknown":true}]}`,
		}},
	} {
		for i, fields := range tc.fields {
			t.Run(fmt.Sprintf("%s-option%d", tc.protocol, i), func(t *testing.T) {
				if ValidateInput(tc.protocol, []byte(tc.base+fields+"}")) == nil {
					t.Fatal("invalid native host option accepted")
				}
			})
		}
	}
	for _, block := range []string{
		`{"type":"thinking","thinking":"x"}`, `{"type":"thinking","thinking":null,"signature":"sig"}`,
		`{"type":"thinking","thinking":"x","signature":42}`, `{"type":"thinking","thinking":"x","signature":"sig","unknown":true}`,
		`{"type":"redacted_thinking","data":42}`, `{"type":"redacted_thinking","data":"opaque","source":{}}`,
		`{"type":"text","text":"hi","cache_control":null}`, `{"type":"text","text":"hi","cache_control":{"type":"persistent"}}`,
		`{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"2h"}}`,
		`{"type":"text","text":"hi","cache_control":{"type":"ephemeral","unknown":true}}`,
	} {
		body := `{"messages":[{"role":"assistant","content":[` + block + `]}]}`
		if ValidateInput("anthropic", []byte(body)) == nil {
			t.Error("invalid native block accepted")
		}
	}
	for _, message := range []string{
		`{"role":"system","content":"hi","output_config":null}`,
		`{"role":"system","content":"hi","output_config":{"effort":42}}`,
		`{"role":"system","content":"hi","output_config":{"effort":"medium","unknown":true}}`,
		`{"role":"user","content":[{"type":"thinking","thinking":"x","signature":"sig"}]}`,
	} {
		if ValidateInput("anthropic", []byte(`{"messages":[`+message+`]}`)) == nil {
			t.Error("invalid host message accepted")
		}
	}
	for _, block := range []string{
		`{"type":"reasoning","summary":null,"encrypted_content":"opaque"}`,
		`{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":42}]}`,
		`{"type":"reasoning","summary":[],"encrypted_content":42}`,
		`{"type":"reasoning","summary":[],"encrypted_content":"opaque","unknown":true}`,
	} {
		if ValidateInput("responses", []byte(`{"input":[`+block+`]}`)) == nil {
			t.Error("invalid native reasoning accepted")
		}
	}
}

func TestValidateInputHostFieldsNativeOrZeroOutbound(t *testing.T) {
	for _, tc := range []struct{ protocol, body string }{
		{"chat", `{"metadata":{"task":"task-1"},"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", `{"metadata":{"task":"task-1"},"previous_response_id":null,"input":"hi"}`},
		{"responses", `{"input":"hi","client_metadata":{"session":"fixture"}}`},
		{"responses", `{"input":[{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"exec_command","arguments":"{\"cmd\":\"cat input.txt\"}"},{"type":"function_call_output","id":"fco_fixture","call_id":"call_fixture","output":"19 23"}],"client_metadata":{"session_id":"fixture","x-codex-turn-metadata":"{\"model\":\"fixture\"}"}}`},
		{"responses", `{"input":"hi","include":["reasoning.encrypted_content"]}`},
		{"responses", `{"input":"hi","text":{"verbosity":"low"}}`},
		{"responses", `{"input":[{"type":"reasoning","summary":[],"content":null,"encrypted_content":"opaque"},{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"fixture"}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"cache_control":{"type":"ephemeral","ttl":"1h"}}`},
		{"anthropic", `{"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}}`},
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed"},{"type":"redacted_thinking","data":"opaque"}]},{"role":"user","content":"hi"}]}`},
		{"anthropic", `{"messages":[{"role":"system","content":"reminder","output_config":{"effort":"medium"}},{"role":"user","content":"hi"}]}`},
	} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			t.Run(tc.protocol+"-to-"+target+"/"+tc.body, func(t *testing.T) {
				calls := 0
				executor := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					var got, want any
					decoder := json.NewDecoder(r.Body)
					decoder.UseNumber()
					if err := decoder.Decode(&got); err != nil {
						t.Fatal(err)
					}
					json.Unmarshal([]byte(tc.body), &want)
					want.(map[string]any)["model"] = "pinned"
					actual, _ := json.Marshal(got)
					expected, _ := json.Marshal(want)
					if string(actual) != string(expected) {
						t.Error("native execution changed checked host fields")
					}
					return response(200, whole(target, true)), nil
				})}
				w := httptest.NewRecorder()
				result, err := executor.Execute(context.Background(), w, Request{tc.protocol, []byte(tc.body), configured("http://fixture.invalid/v1", target)})
				if target == tc.protocol {
					if err != nil || calls != 1 || result.Attempts != 1 || !result.OutputCommitted {
						t.Fatalf("native execution failed: attempts=%d calls=%d error=%v", result.Attempts, calls, err)
					}
				} else if err == nil || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
					t.Fatalf("lossy host conversion reached outbound: attempts=%d calls=%d error=%v", result.Attempts, calls, err)
				}
			})
		}
	}
}

func TestCodexClientMetadataValueShapesRejectBeforeOutbound(t *testing.T) {
	for _, value := range []string{`42`, `true`, `null`, `[]`, `{"thread_id":"nested"}`} {
		for _, target := range []string{"responses", "chat", "anthropic"} {
			t.Run(target+"/"+value, func(t *testing.T) {
				calls := 0
				executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response(200, whole(target, true)), nil
				})}
				body := []byte(`{"input":"hello","client_metadata":{"thread_id":` + value + `}}`)
				w := httptest.NewRecorder()
				result, err := executor.Execute(context.Background(), w, Request{"responses", body, configured("http://fixture.invalid/v1", target)})
				failure, ok := err.(*Error)
				if !ok || failure.Code != "unsupported_input" || failure.Status != 400 || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
					t.Fatalf("unsupported metadata escaped input boundary: result=%+v calls=%d error=%v", result, calls, err)
				}
			})
		}
	}
}
