package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecheckStageDeadlineFallsBackWhileCallerIsAlive(t *testing.T) {
	const secret = "stage-timeout-secret"
	var cloudCalls atomic.Int32
	var redactionCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("stage timeout fallback sent original body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	body := []byte(`{"messages":[{"role":"user","content":"stage-timeout-secret"}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, true, body)
	defer cancel()
	redactor := r1ReviewRedactor(func(_ context.Context, req RedactionRequest) (PreparedCloudRequest, error) {
		redactionCalls.Add(1)
		if req.SessionVerified || req.SessionID != "" {
			t.Fatalf("unverified session crossed W4/W5 boundary: %+v", req)
		}
		return PreparedCloudRequest{Body: bytes.ReplaceAll(req.Body, []byte(secret), []byte("[ACCOUNT_1]"))}, nil
	})
	w := httptest.NewRecorder()
	result, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{
		Judge: &scriptedJudge{err: context.DeadlineExceeded}, Redactor: redactor,
	}}, nil, modelWriterForTest(w))
	if err != nil || result.Destination != "cloud" || result.CapabilityState != "judgment_stage_timeout" || cloudCalls.Load() != 1 || redactionCalls.Load() != 1 {
		t.Fatalf("stage timeout did not use live-caller cloud fallback: result=%+v err=%v cloud=%d redact=%d", result, err, cloudCalls.Load(), redactionCalls.Load())
	}
}

type flushProbeWriter struct {
	header  http.Header
	body    bytes.Buffer
	once    sync.Once
	flushed chan struct{}
}

func (w *flushProbeWriter) Header() http.Header         { return w.header }
func (w *flushProbeWriter) WriteHeader(int)             {}
func (w *flushProbeWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *flushProbeWriter) Flush()                      { w.once.Do(func() { close(w.flushed) }) }

func TestRecheckLocalStreamFlushesEachCommittedEvent(t *testing.T) {
	reader := &r3ChunkReader{waiting: make(chan struct{}), release: make(chan struct{})}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
	w := &flushProbeWriter{header: make(http.Header), flushed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- writeLocalResponse(w, resp, "chat", []byte(`{"stream":true}`), &modelWriter{ResponseWriter: w, commit: func() {}})
	}()
	select {
	case <-w.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("local SSE did not flush before source EOF")
	}
	if !bytes.Contains(w.body.Bytes(), []byte("data: {")) {
		t.Fatalf("flush happened without first committed event: %q", w.body.String())
	}
	close(reader.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRecheckPreparedSSEClosesAfterContinuousRestore(t *testing.T) {
	var restores atomic.Int32
	var closes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	body := []byte(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, true, body)
	defer cancel()
	redactor := r1ReviewRedactor(func(_ context.Context, req RedactionRequest) (PreparedCloudRequest, error) {
		return PreparedCloudRequest{
			Body: req.Body,
			RestoreSSE: func(ctx context.Context, protocol string, reader io.Reader, writer io.Writer) error {
				restores.Add(1)
				if protocol != "chat" {
					return errors.New("unexpected protocol")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					return err
				}
				_, err = writer.Write(data)
				return err
			},
			Close: func() error { closes.Add(1); return nil },
		}, nil
	})
	w := httptest.NewRecorder()
	result, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Redactor: redactor}}, nil, modelWriterForTest(w))
	if err != nil || !result.OutputCommitted || restores.Load() != 1 || closes.Load() != 1 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("continuous restore lifecycle failed: result=%+v err=%v restores=%d closes=%d body=%q", result, err, restores.Load(), closes.Load(), w.Body.String())
	}
}

func TestRecheckAnthropicControlsArePreservedOrRejected(t *testing.T) {
	source := []byte(`{"model":"client","max_tokens":32,"temperature":0.2,"stop_sequences":["DONE","结束"],"tool_choice":{"type":"tool","name":"read"},"messages":[{"role":"user","content":"read"}],"tools":[{"name":"read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	converted, err := localExecutionBody("anthropic", source, "local-model")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(converted, &body) != nil {
		t.Fatal("invalid local body")
	}
	var stop []string
	if json.Unmarshal(body["stop"], &stop) != nil || len(stop) != 2 || stop[1] != "结束" {
		t.Fatalf("stop_sequences were not preserved: %s", converted)
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(body["tool_choice"], &choice) != nil || choice.Type != "function" || choice.Function.Name != "read" {
		t.Fatalf("Anthropic tool_choice was not converted: %s", converted)
	}
	bad := bytes.Replace(source, []byte(`{"type":"tool","name":"read"}`), []byte(`{"type":"future"}`), 1)
	if _, err := localExecutionBody("anthropic", bad, "local-model"); err == nil {
		t.Fatal("unsupported tool_choice was silently accepted")
	}
}

func TestRecheckToolArgumentsPreserveLargeIntegersAndRejectInvalid(t *testing.T) {
	large := "9007199254740993"
	body := `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call-1","function":{"name":"read","arguments":"{\"account\":9007199254740993}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	w := httptest.NewRecorder()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if err := writeLocalResponse(w, resp, "anthropic", []byte(`{"stream":false}`), modelWriterForTest(w)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), large) || strings.Contains(w.Body.String(), "9007199254740992") {
		t.Fatalf("large tool integer changed: %s", w.Body.String())
	}
	invalid := strings.Replace(body, `{\"account\":9007199254740993}`, `{invalid`, 1)
	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(invalid))}
	if err := writeLocalResponse(httptest.NewRecorder(), resp, "anthropic", []byte(`{"stream":false}`), modelWriterForTest(httptest.NewRecorder())); err == nil {
		t.Fatal("invalid tool arguments became an empty successful object")
	}
}

func TestRecheckFinishReasonAndUsageMapByProtocol(t *testing.T) {
	body := `{"choices":[{"message":{"content":"truncated"},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
	for _, protocol := range []string{"anthropic", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			w := httptest.NewRecorder()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			if err := writeLocalResponse(w, resp, protocol, []byte(`{"stream":false}`), modelWriterForTest(w)); err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil {
				t.Fatal(w.Body.String())
			}
			if protocol == "anthropic" {
				var stop string
				var usage map[string]json.RawMessage
				_ = json.Unmarshal(payload["stop_reason"], &stop)
				_ = json.Unmarshal(payload["usage"], &usage)
				if stop != "max_tokens" || string(usage["input_tokens"]) != "7" || string(usage["output_tokens"]) != "3" || usage["prompt_tokens"] != nil {
					t.Fatalf("Anthropic truncation/usage mapping wrong: %s", w.Body.String())
				}
			} else {
				var status string
				_ = json.Unmarshal(payload["status"], &status)
				if status != "incomplete" || !bytes.Contains(payload["incomplete_details"], []byte("max_output_tokens")) || !bytes.Contains(payload["usage"], []byte(`"input_tokens":7`)) {
					t.Fatalf("Responses truncation/usage mapping wrong: %s", w.Body.String())
				}
			}
		})
	}
}
