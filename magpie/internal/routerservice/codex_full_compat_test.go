package routerservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

var codexToolCapabilities = []string{"namespace", "custom", "additional_tools", "web_search"}

func codexHTTPAuthorize(t *testing.T, h http.Handler, capabilities []string) {
	t.Helper()
	w := providerRequest(t, h, "GET", "/api/apps", cloudHTTPManagement, nil)
	var list struct {
		Apps []routerconfig.AppProfile `json:"apps"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatal("read Profiles", w.Code)
	}
	for _, p := range list.Apps {
		if p.ID != "codex" {
			continue
		}
		p.Cloud.Capabilities = capabilities
		w = providerRequest(t, h, "POST", "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{ID: p.ID, Revision: p.Revision, Name: p.Name, Cloud: p.Cloud, CredentialMode: p.CredentialMode})
		if w.Code != 200 {
			t.Fatalf("authorize exact target: %d %s", w.Code, w.Body.String())
		}
		return
	}
	t.Fatal("missing Codex Profile")
}

func codexFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../cloudcodec/testdata/codex-0.161.0/direct-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	body["model"] = "profile-pinned"
	return body
}

// These complete request bodies come from the committed isolated CLI capture,
// including the namespace, goal functions and additional_tools grammar. Only
// model and output preference change to match this local Profile and matrix.
func TestCodexHTTPCompleteCapturedRequests(t *testing.T) {
	for _, name := range []string{"default", "custom", "tools"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", name, stream), func(t *testing.T) {
				body := codexFixture(t, name)
				body["stream"] = stream
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var got map[string]any
					if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, body) {
						t.Error("complete captured body changed")
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply("responses", false))
				}))
				defer upstream.Close()
				h := cloudHTTPHandler(t)
				cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
				codexHTTPAuthorize(t, h, codexToolCapabilities)
				w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "Bearer public-placeholder", "")
				if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), "hello") {
					t.Fatalf("complete capture rejected: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
				}
			})
		}
	}
}

// Replay every final real-cloud CLI request through the current product after
// narrow capability-gate followups. This is HTTP regression evidence; the
// remote execution evidence remains pinned to its recorded binary and commit.
func TestCodexHTTPVerifiedFinalNativeHistories(t *testing.T) {
	for _, scenario := range []string{"tools", "multi", "goals", "search"} {
		files, err := filepath.Glob("testdata/codex-0.161.0/verified-final-" + scenario + "/request-*.json")
		if err != nil || len(files) == 0 {
			t.Fatalf("missing final real captures %s: %v", scenario, err)
		}
		for _, file := range files {
			t.Run(scenario+"/"+filepath.Base(file), func(t *testing.T) {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var capture struct {
					Body map[string]any `json:"body"`
				}
				if err := json.Unmarshal(data, &capture); err != nil || capture.Body == nil {
					t.Fatal("invalid real request capture", err)
				}
				body := capture.Body
				body["model"] = "profile-pinned"
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var got map[string]any
					if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, body) {
						t.Error("final native history changed")
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, cloudHTTPReply("responses", false))
				}))
				defer upstream.Close()
				h := cloudHTTPHandler(t)
				cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
				codexHTTPAuthorize(t, h, []string{"namespace", "web_search"})
				w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatalf("final native capture rejected: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
				}
			})
		}
	}
}

func codexResponseOutput(t *testing.T, w *httptest.ResponseRecorder, stream bool) []any {
	t.Helper()
	data := w.Body.Bytes()
	if stream {
		data = nil
		for _, line := range strings.Split(w.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Type == "response.completed" {
				data = event.Response
			}
		}
	}
	var reply struct {
		Output []any `json:"output"`
	}
	if len(data) == 0 || json.Unmarshal(data, &reply) != nil || reply.Output == nil {
		t.Fatalf("missing complete native output: %s", w.Body.String())
	}
	return reply.Output
}

// This is a simulated upstream behavior matrix, not a real search/agent result.
// Every output returned through the product is replayed unchanged in later
// requests, including citations, opaque reasoning and parallel call identities.
func TestCodexHTTPEnhancedThreeRoundHistory(t *testing.T) {
	cases := []struct {
		name, fixture, item string
		resultType          string
	}{
		{"namespace", "default", `{"type":"function_call","id":"fc_agent","call_id":"call_agent","namespace":"multi_agent_v1","name":"spawn_agent","arguments":"{\"message\":\"analyze\"}"}`, "function_call_output"},
		{"custom", "custom", `{"type":"custom_tool_call","id":"ct_patch","call_id":"call_patch","namespace":"functions","name":"exec","input":"arbitrary freeform\nnot JSON"}`, "custom_tool_call_output"},
		{"search", "default", `{"type":"web_search_call","id":"ws_fixture","status":"completed","action":{"type":"search","query":"fixture documentation"}}`, ""},
		{"goals", "default", `{"type":"function_call","id":"fc_goal","call_id":"call_goal","name":"create_goal","arguments":"{\"objective\":\"fixture objective\"}"}`, "function_call_output"},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			for _, upstreamStream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/clientSSE=%t/upstreamSSE=%t", tc.name, stream, upstreamStream), func(t *testing.T) {
					body := codexFixture(t, tc.fixture)
					body["stream"] = stream
					if tc.name == "search" {
						body["tools"] = append(body["tools"].([]any), map[string]any{"type": "web_search"})
					}
					var item any
					json.Unmarshal([]byte(tc.item), &item)
					var reason any
					json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_fixture","summary":[],"content":null,"encrypted_content":null}`), &reason)
					var citation any
					json.Unmarshal([]byte(`{"type":"message","id":"msg_citation","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixture result","annotations":[{"type":"url_citation","url":"https://example.com/source","title":"Fixture","start_index":0,"end_index":7}]}]}`), &citation)
					outputs := []any{reason, item, citation}
					// A second simultaneous call verifies call/output pairing survives.
					if tc.resultType != "" && tc.name != "goals" {
						parallel := map[string]any{}
						for k, v := range item.(map[string]any) {
							parallel[k] = v
						}
						parallel["id"], parallel["call_id"] = "parallel_item", "parallel_call"
						outputs = append(outputs, parallel)
					}
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var got map[string]any
						if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, body) {
							t.Error("native complete history changed")
						}
						reply := map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "model": "profile-pinned", "output": outputs}
						encoded, _ := json.Marshal(reply)
						if upstreamStream {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
							for i, output := range outputs {
								frame, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": i, "item": output})
								fmt.Fprintf(w, "data: %s\n\n", frame)
							}
							fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", encoded)
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.Write(encoded)
						}
					}))
					defer upstream.Close()
					h := cloudHTTPHandler(t)
					cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
					codexHTTPAuthorize(t, h, codexToolCapabilities)
					for round := 0; round < 3; round++ {
						w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
						if w.Code != 200 {
							t.Fatalf("round %d: %d %s", round, w.Code, w.Body.String())
						}
						actual := codexResponseOutput(t, w, stream)
						if !reflect.DeepEqual(actual, outputs) {
							t.Fatalf("native output lost fields at round %d: %s", round, w.Body.String())
						}
						body["input"] = append(body["input"].([]any), actual...)
						if tc.resultType != "" {
							// Reverse result order, as asynchronous parallel tools can do.
							for i := len(outputs) - 1; i >= 0; i-- {
								call := outputs[i].(map[string]any)
								id, ok := call["call_id"].(string)
								if !ok {
									continue
								}
								body["input"] = append(body["input"].([]any), map[string]any{"type": tc.resultType, "id": "out_" + id, "call_id": id, "output": "freeform result\nwith text"})
							}
						}
						body["input"] = append(body["input"].([]any), map[string]any{"role": "user", "content": "continue"})
						// Each next response uses unique identifiers and an opaque fixture.
						clone, _ := json.Marshal(outputs)
						json.Unmarshal(clone, &outputs)
						for _, output := range outputs {
							m := output.(map[string]any)
							if id, ok := m["id"].(string); ok {
								m["id"] = fmt.Sprintf("%s_%d", id, round)
							}
							if id, ok := m["call_id"].(string); ok {
								m["call_id"] = fmt.Sprintf("%s_%d", id, round)
							}
						}
						item = outputs[1]
						if tc.name == "goals" {
							name, arguments := "get_goal", "{}"
							if round > 0 {
								name, arguments = "update_goal", `{"status":"complete"}`
							}
							for _, output := range outputs {
								m := output.(map[string]any)
								if m["type"] == "function_call" {
									m["name"], m["arguments"] = name, arguments
								}
							}
						}
						reason = outputs[0]
						reason.(map[string]any)["encrypted_content"] = "opaque-do-not-decode"
					}
					// Send the last tool results too, then receive a final answer.
					finalMessage := map[string]any{}
					for k, v := range citation.(map[string]any) {
						finalMessage[k] = v
					}
					finalMessage["id"] = "msg_final"
					outputs = []any{finalMessage}
					final := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
					if final.Code != 200 || !reflect.DeepEqual(codexResponseOutput(t, final, stream), outputs) {
						t.Fatalf("final reply failed: %d %s", final.Code, final.Body.String())
					}
					if calls.Load() != 4 {
						t.Fatal("wrong attempt count", calls.Load())
					}
				})
			}
		}
	}
}

func TestCodexHTTPInvalidReasoningZeroOutbound(t *testing.T) {
	for _, invalid := range []any{17, true, map[string]any{"opaque": "x"}, []any{"x"}} {
		t.Run(fmt.Sprintf("%T", invalid), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
			body := map[string]any{"model": "profile-pinned", "input": []any{map[string]any{"type": "reasoning", "id": "rs_invalid", "summary": []any{}, "encrypted_content": invalid}}}
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
			if w.Code != 400 || calls.Load() != 0 {
				t.Fatalf("invalid type escaped: %d calls=%d", w.Code, calls.Load())
			}
		})
	}
}
