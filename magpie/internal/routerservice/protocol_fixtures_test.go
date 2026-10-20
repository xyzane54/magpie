package routerservice

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerledger"
)

func protocolFixture(t *testing.T, protocol, extension string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "protocol-contracts", protocol+extension))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func protocolFixtureRequestRecord(t *testing.T, rows []routerledger.Record, requestID string) routerledger.Record {
	t.Helper()
	if requestID == "" {
		t.Fatal("rejected request has no X-Request-ID")
	}
	// The ledger sorts equal start timestamps by random request ID, not by
	// arrival or completion order. Locate this response's immutable identity.
	for _, row := range rows {
		if row.RequestID == requestID {
			return row
		}
	}
	t.Fatalf("rejected request %s is absent from the ledger", requestID)
	return routerledger.Record{}
}

func TestProtocolFixtureLedgerLookupWithEqualStartTimes(t *testing.T) {
	h, ledger, _, _ := ledgerHTTPFixture(t, ModelOptions{})
	started := time.Now().UTC().Add(-time.Minute)
	const earlierID = "ffffffffffffffffffffffffffffffff"
	const currentID = "00000000000000000000000000000001"
	for _, id := range []string{earlierID, currentID} {
		if err := ledger.Start(routerledger.Metadata{AcceptedAt: started, RequestID: id, AppID: "workbuddy", AppName: "workbuddy", Operation: "generate", Path: "/v1/chat/completions", ProviderID: "fixture-provider", Model: "profile-pinned"}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Finish(id, routerledger.Outcome{ExecutionKnown: true, Status: "failed", Destination: "none", SafeErrorCode: "unsupported_input", FailureStage: "validation"}); err != nil {
			t.Fatal(err)
		}
	}
	rows := requestRecords(t, h, "")
	if len(rows) != 2 || !rows[0].StartedAt.Equal(started) || !rows[1].StartedAt.Equal(started) || rows[0].RequestID != earlierID {
		t.Fatal("fixture did not reproduce the ledger's documented timestamp tie ordering")
	}
	row := protocolFixtureRequestRecord(t, rows, currentID)
	if row.RequestID != currentID || row.AppID != "workbuddy" || row.ProviderID != "fixture-provider" || row.Model != "profile-pinned" || row.Attempts != 0 || row.Destination != "none" || row.FailureStage != "validation" || row.SafeErrorCode != "unsupported_input" || row.OutputCommitted {
		t.Fatalf("current request lost its complete bound snapshot: %+v", row)
	}
	t.Logf("equal start timestamps: list first=%s, current request=%s", rows[0].RequestID, row.RequestID)
}

func TestProtocolFixtureInputScopeAndBoundLedger(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			t.Run(source+"-to-"+target, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer upstream.Close()
				h, _, store, _ := ledgerHTTPFixture(t, ModelOptions{})
				cloudHTTPBind(t, h, upstream.URL, target, "workbuddy", "scope-provider-key")
				bound, err := store.ResolveApp("workbuddy", "", "", cloudHTTPManagement)
				if err != nil {
					t.Fatal(err)
				}
				for _, stream := range []bool{false, true} {
					for _, kind := range []string{"image", "audio", "unknown", "unknown-text-field", "model"} {
						var body map[string]any
						_ = json.Unmarshal(protocolFixture(t, source, ".json"), &body)
						body["stream"] = stream
						code := "unsupported_input"
						switch kind {
						case "model":
							body["model"], code = "unbound-provider/model", "target_mismatch"
						case "unknown":
							body["unknown_host_field"] = "private-input-marker"
						default:
							block := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "private-input-marker"}}
							if source == "chat" {
								block = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,private-input-marker"}}
							} else if source == "responses" {
								block = map[string]any{"type": "input_image", "image_url": "data:image/png;base64,private-input-marker"}
							}
							if kind == "audio" {
								block = map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "private-input-marker", "format": "wav"}}
								if source == "anthropic" {
									block = map[string]any{"type": "audio", "source": map[string]any{"type": "base64", "media_type": "audio/wav", "data": "private-input-marker"}}
								}
							}
							if kind == "unknown-text-field" {
								textType := "text"
								if source == "responses" {
									textType = "input_text"
								}
								block = map[string]any{"type": textType, "text": "hello", "unknown": "private-input-marker"}
							}
							field := "messages"
							if source == "responses" {
								field = "input"
							}
							items := body[field].([]any)
							items[len(items)-1].(map[string]any)["content"] = []any{block}
						}
						data, _ := json.Marshal(body)
						w := contractHTTPRaw(h, source, "", string(data), nil)
						if w.Code != 400 || calls.Load() != 0 {
							t.Fatalf("%s: status=%d outbound=%d", kind, w.Code, calls.Load())
						}
						rows := requestRecords(t, h, "")
						row := protocolFixtureRequestRecord(t, rows, w.Header().Get("X-Request-ID"))
						if row.RequestID != w.Header().Get("X-Request-ID") || row.AppID != "workbuddy" || row.ProviderID != bound.Provider.Config.ID || row.Model != "profile-pinned" || row.Attempts != 0 || row.Destination != "none" || row.FailureStage != "validation" || row.SafeErrorCode != code || row.OutputCommitted {
							t.Fatalf("%s: rejected request %s lost bound snapshot: %+v", kind, w.Header().Get("X-Request-ID"), row)
						}
						encoded, _ := json.Marshal(row)
						if bytes.Contains(encoded, []byte("scope-provider-key")) || bytes.Contains(encoded, []byte("private-input-marker")) {
							t.Fatal("private data entered ledger")
						}
					}
				}
			})
		}
	}
}

func TestProtocolFixtureFragmentedSSEAndEmptyToolResults(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			for _, stream := range []bool{false, true} {
				for _, complete := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s-to-%s/stream=%t/complete=%t", source, target, stream, complete), func(t *testing.T) {
						var body map[string]json.RawMessage
						_ = json.Unmarshal(protocolFixture(t, source, ".json"), &body)
						body["stream"], _ = json.Marshal(stream)
						data, _ := json.Marshal(body)
						// The file has one final LF; finish its last SSE packet here.
						wire := append(protocolFixture(t, target, ".sse"), '\n')
						if !complete {
							// End before the successful terminal event, with valid JSON
							// deltas and a finished tool block still in the stream.
							packets := strings.Split(strings.TrimSpace(string(wire)), "\n\n")
							wire = []byte(strings.Join(packets[:len(packets)-1], "\n\n") + "\n\n")
						}
						var calls atomic.Int32
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							input, _ := io.ReadAll(r.Body)
							if r.URL.Path != "/v1/"+cloudHTTPPath(target) || !bytes.Contains(input, []byte("9007199254740993")) || !bytes.Contains(input, []byte("anyOf")) || !bytes.Contains(input, []byte("business data")) {
								t.Error("request target, precision or business schema changed")
							}
							var request struct {
								Model    string
								Messages []struct {
									Role       string
									Content    json.RawMessage
									ToolCallID string `json:"tool_call_id"`
								}
								Input []struct {
									Type   string
									Output json.RawMessage
									CallID string `json:"call_id"`
								}
							}
							_ = json.Unmarshal(input, &request)
							emptyResult := false
							for _, m := range request.Messages {
								if m.Role == "tool" && m.ToolCallID == "call_old" && string(m.Content) == `""` {
									emptyResult = true
								}
								var blocks []struct {
									Type      string
									ToolUseID string `json:"tool_use_id"`
									Content   json.RawMessage
								}
								_ = json.Unmarshal(m.Content, &blocks)
								for _, block := range blocks {
									if block.Type == "tool_result" && block.ToolUseID == "call_old" && string(block.Content) == `""` {
										emptyResult = true
									}
								}
							}
							for _, item := range request.Input {
								if item.Type == "function_call_output" && item.CallID == "call_old" && string(item.Output) == `""` {
									emptyResult = true
								}
							}
							if !emptyResult {
								t.Error("empty tool result dropped")
							}
							if request.Model != "profile-pinned" || target == "anthropic" && r.Header.Get("x-api-key") != "fixture-provider-key" || target != "anthropic" && r.Header.Get("Authorization") != "Bearer fixture-provider-key" {
								t.Error("request escaped bound model or credential")
							}
							w.Header().Set("Content-Type", "text/event-stream")
							// Arbitrary byte cuts include UTF-8 and JSON escape boundaries.
							for start := 0; start < len(wire); start += 7 {
								_, _ = w.Write(wire[start:min(start+7, len(wire))])
								w.(http.Flusher).Flush()
							}
						}))
						defer upstream.Close()
						var done ModelCompletion
						h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { done = c }})
						cloudHTTPBind(t, h, upstream.URL, target, "workbuddy", "fixture-provider-key")
						w := contractHTTPRaw(h, source, "", string(data), nil)
						if calls.Load() != 1 || done.Result.Attempts != 1 {
							t.Fatalf("attempts=%d calls=%d", done.Result.Attempts, calls.Load())
						}
						if !complete {
							if done.ErrorCode != "cloud_response_error" {
								t.Fatalf("truncated SSE succeeded: %+v", done)
							}
							for _, terminal := range []string{"[DONE]", "response.completed", "message_stop"} {
								if strings.Contains(w.Body.String(), terminal) {
									t.Fatal("successful terminal after interrupted SSE")
								}
							}
							return
						}
						if w.Code != 200 || done.ErrorCode != "" || done.Result.Usage == nil {
							t.Fatalf("fixture failed: %d %+v %s", w.Code, done, w.Body.String())
						}
						args := fixtureReplyArgs(t, source, stream, w.Body.Bytes())
						if !strings.Contains(w.Body.String(), "call_new") || !strings.Contains(w.Body.String(), "run") {
							t.Fatal("reply lost tool identity")
						}
						var result struct {
							Path string
							ID   json.Number
						}
						if json.Unmarshal([]byte(args), &result) != nil || result.Path != "下一项.txt" || result.ID.String() != "9007199254740993" {
							t.Fatalf("tool arguments changed: %s", args)
						}
					})
				}
			}
		}
	}
}

// Read only the public JSON/SSE fields needed by this assertion, independently
// of the production decoders and their common reply collector.
func fixtureReplyArgs(t *testing.T, protocol string, stream bool, body []byte) string {
	t.Helper()
	if !stream {
		var reply struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
				}
			}
			Output  []struct{ Arguments string }
			Content []struct{ Input json.RawMessage }
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			t.Fatal(err)
		}
		switch protocol {
		case "chat":
			if len(reply.Choices) == 1 && len(reply.Choices[0].Message.ToolCalls) == 1 {
				return reply.Choices[0].Message.ToolCalls[0].Function.Arguments
			}
		case "responses":
			if len(reply.Output) == 1 {
				return reply.Output[0].Arguments
			}
		case "anthropic":
			if len(reply.Content) == 1 {
				return string(reply.Content[0].Input)
			}
		}
		t.Fatalf("missing tool in JSON reply: %s", body)
	}
	var args strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var event struct {
			Type    string
			Delta   json.RawMessage
			Choices []struct {
				Delta struct {
					ToolCalls []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		for _, choice := range event.Choices {
			for _, call := range choice.Delta.ToolCalls {
				args.WriteString(call.Function.Arguments)
			}
		}
		if event.Type == "response.function_call_arguments.delta" {
			var delta string
			_ = json.Unmarshal(event.Delta, &delta)
			args.WriteString(delta)
		}
		if event.Type == "content_block_delta" {
			var delta struct {
				Partial string `json:"partial_json"`
			}
			_ = json.Unmarshal(event.Delta, &delta)
			args.WriteString(delta.Partial)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return args.String()
}
