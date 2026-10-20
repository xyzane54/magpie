package routerredact

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

	"github.com/yetone/magpie/internal/cloudcodec"
)

// These are independent module compositions, not the W4 product dispatcher.
// This fixture models the required order: bind/convert, trusted option merge,
// decoded-value session expansion, Prepare, immutable attempts, client-protocol
// conversion and restoration. It deliberately does not import W4-only types.
type finalBoundaryPlan struct {
	Input          Input
	ClientProtocol Protocol
	CloudProtocol  Protocol
	CloudModel     string
	Expansion      string
	Options        map[string]any
	Retry          bool
	Prepared       func(*Prepared)
}

func finalBoundaryExpand(value any, expansion string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "${sessionId}", expansion)
	case []any:
		for i := range v {
			v[i] = finalBoundaryExpand(v[i], expansion)
		}
	case map[string]any:
		for k := range v {
			v[k] = finalBoundaryExpand(v[k], expansion)
		}
	}
	return value
}

func finalBoundaryBuild(plan finalBoundaryPlan) (*cloudcodec.CloudCodec, Input, error) {
	codec, err := cloudcodec.NewCloudCodec(string(plan.ClientProtocol), plan.Input.Body)
	if err != nil {
		return nil, Input{}, fail("fixture_conversion_failed")
	}
	body, err := codec.Request(string(plan.CloudProtocol), plan.CloudModel, "loopback")
	if err != nil {
		return nil, Input{}, fail("fixture_conversion_failed")
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil {
		return nil, Input{}, fail("fixture_finalization_failed")
	}
	// Options are a synthetic already-authorized finalizer input. This helper
	// does not claim to duplicate routercloud's private ExtraBody allowlist.
	for key, value := range plan.Options {
		raw, _ := json.Marshal(value)
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var copied any
		if d.Decode(&copied) != nil {
			return nil, Input{}, fail("fixture_finalization_failed")
		}
		object[key] = copied
	}
	body, err = json.Marshal(finalBoundaryExpand(object, plan.Expansion))
	if err != nil {
		return nil, Input{}, fail("fixture_finalization_failed")
	}
	in := plan.Input
	in.Protocol = plan.CloudProtocol
	in.Body = body
	return codec, in, nil
}

type finalBoundaryPipeWriter struct {
	header http.Header
	writer io.Writer
}

func (w *finalBoundaryPipeWriter) Header() http.Header         { return w.header }
func (w *finalBoundaryPipeWriter) WriteHeader(int)             {}
func (w *finalBoundaryPipeWriter) Write(b []byte) (int, error) { return w.writer.Write(b) }
func (w *finalBoundaryPipeWriter) Flush()                      {}

func finalBoundaryConsume(ctx context.Context, codec *cloudcodec.CloudCodec, prepared *Prepared, plan finalBoundaryPlan, response *http.Response, output io.Writer) error {
	defer response.Body.Close()
	if plan.Input.Operation == "count_tokens" {
		_, err := io.Copy(output, io.LimitReader(response.Body, 1<<20))
		return err
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		reader, writer := io.Pipe()
		defer reader.Close()
		done := make(chan error, 1)
		go func() {
			_, err := codec.Reply(&finalBoundaryPipeWriter{header: make(http.Header), writer: writer}, string(plan.CloudProtocol), response.Header.Get("Content-Type"), response.Body)
			_ = writer.CloseWithError(err)
			done <- err
		}()
		err := prepared.RestoreSSE(ctx, plan.ClientProtocol, reader, output)
		if err != nil {
			_ = reader.CloseWithError(err)
			_ = response.Body.Close()
		}
		conversionErr := <-done
		if err != nil {
			return err
		}
		return conversionErr
	}
	converted := httptest.NewRecorder()
	if _, err := codec.Reply(converted, string(plan.CloudProtocol), response.Header.Get("Content-Type"), response.Body); err != nil {
		return err
	}
	result, err := prepared.RestoreResponse(plan.ClientProtocol, converted.Body.Bytes())
	if err != nil {
		return err
	}
	_, err = output.Write(result)
	return err
}

func finalBoundaryExecute(ctx context.Context, pipeline *Pipeline, endpoint string, plan finalBoundaryPlan, output io.Writer) error {
	codec, in, err := finalBoundaryBuild(plan)
	if err != nil {
		return err
	}
	prepared, err := pipeline.Prepare(ctx, in)
	if err != nil {
		return err
	}
	defer prepared.Close()
	if plan.Prepared != nil {
		plan.Prepared(prepared)
	}
	for attempt := 0; attempt < 2; attempt++ {
		body, err := prepared.Body()
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return fail("fixture_transport_failed")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return fail("fixture_transport_failed")
		}
		if response.StatusCode == 503 && plan.Retry && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			response.Body.Close()
			continue
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return fail("fixture_upstream_failed")
		}
		return finalBoundaryConsume(ctx, codec, prepared, plan, response, output)
	}
	return fail("fixture_attempts_exhausted")
}

func finalBoundaryRequest(protocol Protocol, stream bool) []byte {
	var request map[string]any
	switch protocol {
	case Chat:
		request = map[string]any{"messages": []any{map[string]any{"role": "system", "content": "policy ${sessionId}"}, map[string]any{"role": "user", "content": "BODY_SYNTHETIC"}}}
	case Responses:
		request = map[string]any{"instructions": "policy ${sessionId}", "input": "BODY_SYNTHETIC"}
	case Anthropic:
		request = map[string]any{"system": "policy ${sessionId}", "messages": []any{map[string]any{"role": "user", "content": "BODY_SYNTHETIC"}}, "max_tokens": 64}
	}
	request["model"], request["stream"] = "client-unbound", stream
	body, _ := json.Marshal(request)
	return body
}

func finalBoundaryInput(protocol Protocol, stream bool) Input {
	return Input{RequestID: "boundary-request", AppID: "boundary-app", Protocol: protocol, Operation: "generation", SessionID: "server-verified-session", SessionVerified: true, Versions: Versions{Model: "runtime:b10828/model:sha256/template:sha256", Prompt: "redaction-prompt-v1", Config: "redaction-config-v1"}, Body: finalBoundaryRequest(protocol, stream)}
}

func finalBoundaryReply(protocol Protocol) string {
	switch protocol {
	case Chat:
		return `{"id":"reply","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"<PERSON:1>","tool_calls":[{"id":"call-final","type":"function","function":{"name":"deliver","arguments":"{\"v\":\"<PERSON:2>\",\"n\":7,\"ok\":true}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":0,"completion_tokens":3}}`
	case Responses:
		return `{"id":"reply","object":"response","status":"completed","output":[{"type":"message","id":"message-final","role":"assistant","content":[{"type":"output_text","text":"<PERSON:1>"}]},{"type":"function_call","id":"tool-final","call_id":"call-final","name":"deliver","arguments":"{\"v\":\"<PERSON:2>\",\"n\":7,\"ok\":true}"}],"usage":{"input_tokens":0,"output_tokens":3}}`
	default:
		return `{"id":"reply","type":"message","role":"assistant","content":[{"type":"text","text":"<PERSON:1>"},{"type":"tool_use","id":"call-final","name":"deliver","input":{"v":"<PERSON:2>","n":7,"ok":true}}],"stop_reason":"tool_use","usage":{"input_tokens":0,"output_tokens":3}}`
	}
}

func finalBoundaryAssertReleased(t *testing.T, prepared *Prepared) {
	t.Helper()
	if prepared == nil {
		t.Fatal("Prepare result not observed")
	}
	if _, err := prepared.Body(); err == nil {
		t.Fatal("finalizer retained prepared body")
	}
	if _, err := prepared.RestoreText("<PERSON:1>"); err == nil {
		t.Fatal("finalizer retained private mapping")
	}
}

func TestFinalBoundaryConvertedFinalBytesAndRetryAcrossBranches(t *testing.T) {
	branches := []string{"normal-cloud", "judgment-failure", "capability-mismatch", "local-failure-fallback", "same-target-retry", "count_tokens"}
	for _, branch := range branches {
		for _, clientProtocol := range []Protocol{Chat, Responses, Anthropic} {
			t.Run(branch+"/"+string(clientProtocol), func(t *testing.T) {
				cloudProtocol := Chat
				if clientProtocol == Chat {
					cloudProtocol = Responses
				}
				if branch == "count_tokens" {
					clientProtocol, cloudProtocol = Anthropic, Anthropic
				}
				var calls atomic.Int32
				var observed [][]byte
				var mu sync.Mutex
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					mu.Lock()
					observed = append(observed, bytes.Clone(raw))
					mu.Unlock()
					attempt := calls.Add(1)
					for _, source := range []string{"BODY_SYNTHETIC", "SESSION_SYNTHETIC", "OPTION_SYNTHETIC"} {
						if bytes.Contains(raw, []byte(source)) {
							t.Errorf("final outbound contains synthetic original %s", source)
						}
					}
					var body map[string]any
					if json.Unmarshal(raw, &body) != nil || body["model"] != "cloud-bound" {
						t.Error("final target model not bound")
					}
					if cloudProtocol == Chat && body["messages"] == nil || cloudProtocol == Responses && body["input"] == nil {
						t.Error("Prepare protocol differs from final body")
					}
					if branch == "same-target-retry" && attempt == 1 {
						w.WriteHeader(503)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if branch == "count_tokens" {
						_, _ = io.WriteString(w, `{"input_tokens":17}`)
					} else {
						_, _ = io.WriteString(w, finalBoundaryReply(cloudProtocol))
					}
				}))
				defer upstream.Close()
				var extractionCalls atomic.Int32
				pipeline := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
					extractionCalls.Add(1)
					if in.Versions.Model == "cloud-bound" {
						t.Error("cloud model mistaken for extraction version")
					}
					return synthetic("BODY_SYNTHETIC", "SESSION_SYNTHETIC", "OPTION_SYNTHETIC")(c, in)
				})})
				defer pipeline.Close()
				in := finalBoundaryInput(clientProtocol, false)
				if branch == "count_tokens" {
					in.Operation = "count_tokens"
				}
				options := map[string]any{}
				if cloudProtocol == Chat {
					options["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "result", "description": "OPTION_SYNTHETIC ${sessionId}", "schema": map[string]any{"type": "object"}}}
				} else if cloudProtocol == Responses {
					options["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "result", "description": "OPTION_SYNTHETIC ${sessionId}", "schema": map[string]any{"type": "object"}}}
				} else {
					options["max_tokens"] = 96
				}
				var prepared *Prepared
				plan := finalBoundaryPlan{Input: in, ClientProtocol: clientProtocol, CloudProtocol: cloudProtocol, CloudModel: "cloud-bound", Expansion: "SESSION_SYNTHETIC", Options: options, Retry: branch == "same-target-retry", Prepared: func(p *Prepared) { prepared = p }}
				var output bytes.Buffer
				if err := finalBoundaryExecute(context.Background(), pipeline, upstream.URL, plan, &output); err != nil {
					t.Fatal(err)
				}
				expected := int32(1)
				if plan.Retry {
					expected = 2
				}
				if calls.Load() != expected || extractionCalls.Load() != 1 {
					t.Fatal("retry repeated extraction or branch did not dispatch", calls.Load(), extractionCalls.Load())
				}
				if plan.Retry && !bytes.Equal(observed[0], observed[1]) {
					t.Fatal("same-target retry regenerated final request")
				}
				finalBoundaryAssertReleased(t, prepared)
				if branch == "count_tokens" {
					if output.String() != `{"input_tokens":17}` {
						t.Fatal("count response changed")
					}
				} else {
					if !bytes.Contains(output.Bytes(), []byte("call-final")) || !bytes.Contains(output.Bytes(), []byte("deliver")) || bytes.Contains(output.Bytes(), []byte("<PERSON:")) {
						t.Fatal("client protocol response/tool restoration missing", output.String())
					}
				}
				if !bytes.Equal(in.Body, finalBoundaryRequest(clientProtocol, false)) {
					t.Fatal("original client request mutated")
				}
			})
		}
	}
}

func TestFinalBoundaryOpaqueOptionInjectionBlocksBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	for _, protocol := range []Protocol{Chat, Responses, Anthropic} {
		pipeline := New(Options{Extractor: synthetic("BODY_SYNTHETIC", "SESSION_SYNTHETIC")})
		plan := finalBoundaryPlan{Input: finalBoundaryInput(protocol, false), ClientProtocol: protocol, CloudProtocol: protocol, CloudModel: "bound", Expansion: "SESSION_SYNTHETIC", Options: map[string]any{"metadata": map[string]any{"user_id": "OPAQUE_SYNTHETIC ${sessionId}"}}}
		err := finalBoundaryExecute(context.Background(), pipeline, upstream.URL, plan, io.Discard)
		if err == nil || calls.Load() != 0 {
			t.Fatalf("opaque final option bypassed gate: %s %v", protocol, err)
		}
		pipeline.Close()
	}
}

type finalBoundaryObserveWriter struct {
	mu      sync.Mutex
	body    bytes.Buffer
	visible chan struct{}
	once    sync.Once
}

func (w *finalBoundaryObserveWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.body.Write(b)
	if bytes.Contains(w.body.Bytes(), []byte("safe-first")) {
		w.once.Do(func() { close(w.visible) })
	}
	return n, err
}
func (w *finalBoundaryObserveWriter) Flush() {}
func (w *finalBoundaryObserveWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestFinalBoundarySSEVisibleBeforeEOFAndLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "malformed"} {
		for _, clientProtocol := range []Protocol{Chat, Responses, Anthropic} {
			t.Run(mode+"/"+string(clientProtocol), func(t *testing.T) {
				end := make(chan struct{})
				var endOnce sync.Once
				release := func() { endOnce.Do(func() { close(end) }) }
				defer release()
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					if bytes.Contains(raw, []byte("BODY_SYNTHETIC")) || bytes.Contains(raw, []byte("SESSION_SYNTHETIC")) {
						t.Error("stream final outbound leaked original")
					}
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, streamTestEvent(streamTestTextDelta(Chat, "safe-first <PERSON:")))
					w.(http.Flusher).Flush()
					select {
					case <-end:
					case <-r.Context().Done():
						return
					}
					if mode == "malformed" {
						_, _ = io.WriteString(w, "data: invalid-json\n\n")
						return
					}
					_, _ = io.WriteString(w, streamTestEvent(streamTestTextDelta(Chat, "1>"))+streamTestEnd(Chat, false))
				}))
				defer upstream.Close()
				pipeline := New(Options{Extractor: synthetic("BODY_SYNTHETIC", "SESSION_SYNTHETIC")})
				defer pipeline.Close()
				preparedResult := make(chan *Prepared, 1)
				plan := finalBoundaryPlan{Input: finalBoundaryInput(clientProtocol, true), ClientProtocol: clientProtocol, CloudProtocol: Chat, CloudModel: "bound", Expansion: "SESSION_SYNTHETIC", Prepared: func(p *Prepared) { preparedResult <- p }}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writer := &finalBoundaryObserveWriter{visible: make(chan struct{})}
				done := make(chan error, 1)
				go func() { done <- finalBoundaryExecute(ctx, pipeline, upstream.URL, plan, writer) }()
				joined := false
				defer func() {
					cancel()
					release()
					if !joined {
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Error("fixture cleanup failed to join stream")
						}
					}
				}()
				select {
				case <-writer.visible:
				case err := <-done:
					joined = true
					t.Fatalf("stream finished before visible first text: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("first text waits for upstream EOF")
				}
				select {
				case err := <-done:
					joined = true
					t.Fatalf("source ended before fixture release: %v", err)
				default:
				}
				if mode == "cancel" {
					cancel()
				} else {
					release()
				}
				select {
				case err := <-done:
					joined = true
					if mode == "success" && err != nil {
						t.Fatal(err)
					}
					if mode != "success" && err == nil {
						t.Fatal("failed/canceled stream reported success")
					}
				case <-time.After(5 * time.Second):
					cancel()
					release()
					t.Fatal("stream ownership failed to join")
				}
				prepared := <-preparedResult
				finalBoundaryAssertReleased(t, prepared)
				if calls.Load() != 1 {
					t.Fatal("stream repeated upstream after commitment")
				}
				text := writer.String()
				if mode != "success" && (strings.Contains(text, "[DONE]") || strings.Contains(text, "response.completed") || strings.Contains(text, "message_stop")) {
					t.Fatal("failed stream has success terminal")
				}
				if mode == "success" && !strings.Contains(text, "SYNTHETIC") {
					t.Fatal("restored original missing from client stream")
				}
			})
		}
	}
}

func TestFinalBoundaryIndependentVersionsAndVerifiedSessions(t *testing.T) {
	var extractionCalls atomic.Int32
	expected := Versions{"runtime:b10828/model:sha256/template:sha256", "redaction-prompt-v1", "redaction-config-v1"}
	pipeline := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
		extractionCalls.Add(1)
		return synthetic("BODY_SYNTHETIC", "SESSION_SYNTHETIC")(c, in)
	})})
	defer pipeline.Close()
	in := finalBoundaryInput(Chat, false)
	plan := finalBoundaryPlan{Input: in, ClientProtocol: Chat, CloudProtocol: Chat, CloudModel: "unrelated-cloud-model", Expansion: "SESSION_SYNTHETIC"}
	_, final, err := finalBoundaryBuild(plan)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(input Input) {
		p, err := pipeline.Prepare(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
	}
	prepare(final)
	prepare(final)
	if extractionCalls.Load() != 1 {
		t.Fatal("verified stable session failed to reuse")
	}
	for _, version := range []Versions{{Model: expected.Model + "-new", Prompt: expected.Prompt, Config: expected.Config}, {Model: expected.Model, Prompt: expected.Prompt + "-new", Config: expected.Config}, {Model: expected.Model, Prompt: expected.Prompt, Config: expected.Config + "-new"}} {
		changed := final
		changed.Versions = version
		before := extractionCalls.Load()
		prepare(changed)
		if extractionCalls.Load() != before+1 {
			t.Fatal("independent version change reused mapping")
		}
	}
	unverified := final
	unverified.SessionID = "profile-id-is-not-session"
	unverified.SessionVerified = false
	before := extractionCalls.Load()
	prepare(unverified)
	prepare(unverified)
	if extractionCalls.Load() != before+2 {
		t.Fatal("unverified profile identity enabled history cache")
	}
	other := final
	other.AppID = "another-app"
	before = extractionCalls.Load()
	prepare(other)
	if extractionCalls.Load() != before+1 {
		t.Fatal("cross-app mapping reused")
	}
	if final.Versions != expected || final.Versions.Model == plan.CloudModel {
		t.Fatal("cloud model/judgment version replaced extraction fingerprint")
	}
	pipeline.Clear()
	before = extractionCalls.Load()
	prepare(final)
	if extractionCalls.Load() != before+1 {
		t.Fatal("explicit invalidation reused old mapping")
	}
}

func TestFinalBoundaryPrepareFailuresHaveZeroAttempts(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	for _, options := range []Options{{}, {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, errors.New("source private error") })}, {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, nil })}, {Budget: time.Millisecond, Extractor: ExtractorFunc(func(c context.Context, _ Extraction) (Result, error) { <-c.Done(); return Result{Complete: true}, nil })}} {
		pipeline := New(options)
		plan := finalBoundaryPlan{Input: finalBoundaryInput(Chat, false), ClientProtocol: Chat, CloudProtocol: Chat, CloudModel: "bound", Expansion: "SESSION_SYNTHETIC"}
		err := finalBoundaryExecute(context.Background(), pipeline, upstream.URL, plan, io.Discard)
		if err == nil || calls.Load() != 0 {
			t.Fatal("failed Prepare reached final outbound")
		}
		if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SYNTHETIC") {
			t.Fatal("private failure text escaped")
		}
		pipeline.Close()
	}
}

type finalBoundaryVersionedExtractor struct {
	expected Versions
	calls    atomic.Int32
}

func (e *finalBoundaryVersionedExtractor) Available(_ context.Context, versions Versions) error {
	if versions != e.expected {
		return errors.New("synthetic version mismatch")
	}
	return nil
}

func (e *finalBoundaryVersionedExtractor) Extract(c context.Context, in Extraction) (Result, error) {
	e.calls.Add(1)
	return synthetic("BODY_SYNTHETIC", "SESSION_SYNTHETIC")(c, in)
}

func TestFinalBoundaryWrongCloudOrJudgmentVersionsCannotDispatch(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls.Add(1) }))
	defer upstream.Close()
	in := finalBoundaryInput(Chat, false)
	extractor := &finalBoundaryVersionedExtractor{expected: in.Versions}
	pipeline := New(Options{Extractor: extractor})
	defer pipeline.Close()
	for _, versions := range []Versions{
		{Model: "cloud-bound", Prompt: in.Versions.Prompt, Config: in.Versions.Config},
		{Model: in.Versions.Model, Prompt: "judgment-prompt-v1", Config: in.Versions.Config},
		{Model: in.Versions.Model, Prompt: in.Versions.Prompt},
	} {
		wrong := in
		wrong.Versions = versions
		plan := finalBoundaryPlan{Input: wrong, ClientProtocol: Chat, CloudProtocol: Chat, CloudModel: "cloud-bound", Expansion: "SESSION_SYNTHETIC"}
		if err := finalBoundaryExecute(context.Background(), pipeline, upstream.URL, plan, io.Discard); err == nil {
			t.Fatal("incorrect independent version mapping accepted")
		}
	}
	if upstreamCalls.Load() != 0 || extractor.calls.Load() != 0 {
		t.Fatal("incorrect versions reached extractor or upstream")
	}
}
