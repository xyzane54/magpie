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
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routerruntime"
)

type privacyTestField struct {
	Field int    `json:"field"`
	Text  string `json:"text"`
}
type privacyTestFinding struct {
	Label      string  `json:"label"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}
type privacyTestRuntime struct {
	calls       atomic.Int32
	fingerprint string
	suite       string
	classify    func(context.Context, int, []privacyTestField) ([]byte, error)
}

func (r *privacyTestRuntime) Status() routerruntime.Status {
	return routerruntime.Status{Capabilities: map[routerruntime.Purpose]routerruntime.Capability{routerruntime.Redaction: {Ready: true, State: "available", Fingerprint: r.fingerprint, Suite: r.suite}}}
}
func (*privacyTestRuntime) Require(p routerruntime.Purpose) error {
	if p != routerruntime.Redaction {
		return routerruntime.ErrCapability
	}
	return nil
}
func (r *privacyTestRuntime) Invoke(ctx context.Context, p routerruntime.Purpose, body []byte, consume func(*http.Response) error) error {
	if p != routerruntime.Redaction {
		return errors.New("synthetic wrong purpose")
	}
	var request struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Kwargs map[string]any `json:"chat_template_kwargs"`
	}
	if json.Unmarshal(body, &request) != nil || request.Stream || request.Kwargs["enable_thinking"] != false {
		return errors.New("synthetic invalid runtime request")
	}
	var fields []privacyTestField
	for _, message := range request.Messages {
		if message.Role == "user" {
			var input struct {
				Operation string             `json:"operation"`
				Fields    []privacyTestField `json:"fields"`
			}
			if json.Unmarshal([]byte(message.Content), &input) != nil || input.Operation != "classify" {
				return errors.New("synthetic invalid classifier input")
			}
			fields = input.Fields
		}
	}
	if len(fields) == 0 {
		return errors.New("synthetic empty classifier fields")
	}
	call := int(r.calls.Add(1))
	raw, err := r.classify(ctx, call, fields)
	if err != nil {
		return err
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}
	defer response.Body.Close()
	return consume(response)
}

func privacyTestEnvelope(fields []privacyTestField, values map[string]string) []byte {
	results := make([]any, 0, len(fields))
	for _, field := range fields {
		findings := []privacyTestFinding{}
		for value, label := range values {
			for offset := 0; offset < len(field.Text); {
				at := strings.Index(field.Text[offset:], value)
				if at < 0 {
					break
				}
				at += offset
				findings = append(findings, privacyTestFinding{Label: label, Start: utf8.RuneCountInString(field.Text[:at]), End: utf8.RuneCountInString(field.Text[:at+len(value)]), Text: value, Confidence: 1})
				offset = at + len(value)
			}
		}
		results = append(results, map[string]any{"field": field.Field, "complete": true, "findings": findings})
	}
	content, _ := json.Marshal(map[string]any{"complete": true, "fields": results})
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	return raw
}

func privacyTestSpecification(t *testing.T) *PrivacySpecification {
	t.Helper()
	spec, err := NewPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
func privacyTestExtractor(t *testing.T, values map[string]string) (*RuntimeExtractor, *privacyTestRuntime, *PrivacySpecification) {
	t.Helper()
	spec := privacyTestSpecification(t)
	runtime := &privacyTestRuntime{fingerprint: spec.RuntimeFingerprint(), suite: spec.CapabilitySuite(), classify: func(_ context.Context, _ int, fields []privacyTestField) ([]byte, error) {
		return privacyTestEnvelope(fields, values), nil
	}}
	return NewRuntimeExtractor(runtime, spec), runtime, spec
}

func TestPrivacyPipelineRuntimeToolJSONAndExactAmounts(t *testing.T) {
	extractor, runtime, spec := privacyTestExtractor(t, map[string]string{"Alice": "private_person", "932000元": "financial_amount"})
	pipeline := New(Options{Extractor: extractor})
	defer pipeline.Close()
	in := testInput("🙂Alice Alice")
	in.Versions = spec.Versions()
	in.Body = []byte(`{"model":"cloud","messages":[{"role":"user","content":"🙂Alice Alice"},{"role":"assistant","tool_calls":[{"id":"call-privacy","type":"function","function":{"name":"pay","arguments":"{\"person\":\"Alice\",\"amount\":\"932000元\",\"large\":9007199254740993,\"money\":932000.00,\"ok\":true,\"nil\":null}"}}]},{"role":"tool","tool_call_id":"call-privacy","content":"Alice"}]}`)
	prepared, err := pipeline.Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	body, err := prepared.Body()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("Alice")) || bytes.Contains(body, []byte("932000元")) || !bytes.Contains(body, []byte("9007199254740993")) || !bytes.Contains(body, []byte("932000.00")) {
		t.Fatal("exact tool request semantics or source privacy changed")
	}
	if runtime.calls.Load() == 0 {
		t.Fatal("real specification was not invoked through runtime seam")
	}
	restored, err := prepared.RestoreResponse(Chat, []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-privacy","type":"function","function":{"name":"pay","arguments":"{\"person\":\"<PERSON:1>\",\"amount\":\"<FINANCIALAMOUNT:1>\",\"large\":9007199254740993,\"money\":932000.00}"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(restored, []byte("932000元")) || !bytes.Contains(restored, []byte("Alice")) || !bytes.Contains(restored, []byte("9007199254740993")) || !bytes.Contains(restored, []byte("932000.00")) || !bytes.Contains(restored, []byte("call-privacy")) {
		t.Fatal("exact amount/tool restoration failed")
	}
}

func TestPrivacyPipelineCacheVersionAndProtectedHost(t *testing.T) {
	extractor, runtime, spec := privacyTestExtractor(t, map[string]string{"Alice": "private_person"})
	pipeline := New(Options{Extractor: extractor})
	defer pipeline.Close()
	in := testInput("Alice")
	in.Versions = spec.Versions()
	first := mustPrepare(t, pipeline, in)
	calls := runtime.calls.Load()
	second := mustPrepare(t, pipeline, in)
	if runtime.calls.Load() != calls || pipeline.Stats().CacheHits != 1 {
		t.Fatal("stable exact version/session cache not reused")
	}
	for _, version := range []Versions{{Model: in.Versions.Model + "new", Prompt: in.Versions.Prompt, Config: in.Versions.Config}, {Model: in.Versions.Model, Prompt: in.Versions.Prompt + "new", Config: in.Versions.Config}, {Model: in.Versions.Model, Prompt: in.Versions.Prompt, Config: in.Versions.Config + "new"}} {
		changed := in
		changed.Versions = version
		if result, err := pipeline.Prepare(context.Background(), changed); err == nil || result != nil {
			t.Fatal("version mismatch reused cached recognition")
		}
	}
	if runtime.calls.Load() != calls {
		t.Fatal("wrong versions reached runtime")
	}
	first.Close()
	if text, err := second.RestoreText("<PERSON:1>"); err != nil || text != "Alice" {
		t.Fatal("independent cached snapshot corrupted")
	}
	for _, raw := range []string{`{"messages":[{"role":"user","agent":"Alice","content":"safe"}]}`, `{"messages":[{"role":"assistant","content":"safe","rawUsage":{"vendor_note":"Alice"}}]}`, `{"messages":[{"role":"user","content":"safe","unknownHostField":"Alice"}]}`} {
		input := in
		input.Body = []byte(raw)
		if p, err := pipeline.Prepare(context.Background(), input); err == nil || p != nil {
			t.Fatal("protected/unknown host text was admitted")
		}
	}
}

func TestPrivacyPipelineThreeProtocolCrossEventRestoration(t *testing.T) {
	for _, protocol := range []Protocol{Chat, Responses, Anthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			extractor, _, spec := privacyTestExtractor(t, map[string]string{"Alice": "private_person"})
			pipeline := New(Options{Extractor: extractor})
			defer pipeline.Close()
			in := testInput("Alice")
			in.Protocol = protocol
			in.Versions = spec.Versions()
			if protocol == Responses {
				in.Body = []byte(`{"input":"Alice"}`)
			} else if protocol == Anthropic {
				in.Body = []byte(`{"messages":[{"role":"user","content":"Alice"}]}`)
			}
			prepared := mustPrepare(t, pipeline, in)
			var source strings.Builder
			source.WriteString(streamTestEvent(streamTestTextDelta(protocol, "hi <PERSON:")))
			source.WriteString(streamTestEvent(streamTestTextDelta(protocol, "1>")))
			for _, part := range []string{`{"v":"<PER`, `SON:1>","large":9007199254740993}`} {
				source.WriteString(streamTestEvent(streamTestToolDelta(protocol, 1, part)))
			}
			source.WriteString(streamTestEnd(protocol, true))
			var output bytes.Buffer
			if err := prepared.RestoreSSE(context.Background(), protocol, streamByteReader{strings.NewReader(source.String())}, &output); err != nil {
				t.Fatal(err)
			}
			text, tools, _ := streamTestCollect(t, protocol, output.String())
			if text != "hi Alice" || !strings.Contains(tools["1"], `"v":"Alice"`) || !strings.Contains(tools["1"], "9007199254740993") {
				t.Fatal("recognized text/tool SSE mapping failed")
			}
		})
	}
}

func TestPrivacyBatchesPartialFailureTimeoutAndCancellationBlock(t *testing.T) {
	spec := privacyTestSpecification(t)
	for _, mode := range []string{"partial-invalid", "second-execution-failed", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var dispatched atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dispatched.Add(1); w.WriteHeader(200) }))
			defer upstream.Close()
			runtime := &privacyTestRuntime{fingerprint: spec.RuntimeFingerprint(), suite: spec.CapabilitySuite(), classify: func(ctx context.Context, call int, fields []privacyTestField) ([]byte, error) {
				if call == 1 {
					return privacyTestEnvelope(fields, map[string]string{"Alice": "private_person"}), nil
				}
				switch mode {
				case "partial-invalid":
					return []byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"complete\":true,\"fields\":[]}"}}]}`), nil
				case "second-execution-failed":
					return nil, errors.New("Alice private runtime failure")
				default:
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}}
			budget := 15 * time.Second
			if mode == "timeout" {
				// Only the timeout case expires by time. Leave ample room for
				// planning and the first batch before the second blocks on ctx.
				budget = 500 * time.Millisecond
			}
			pipeline := New(Options{Extractor: NewRuntimeExtractor(runtime, spec), Budget: budget})
			defer pipeline.Close()
			texts := make([]string, 17)
			for i := range texts {
				texts[i] = "Alice"
			}
			in := testInput(texts...)
			in.SessionVerified = false
			in.Versions = spec.Versions()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				runtime.classify = func(ctx context.Context, call int, fields []privacyTestField) ([]byte, error) {
					if call == 1 {
						cancel()
					}
					return privacyTestEnvelope(fields, map[string]string{"Alice": "private_person"}), nil
				}
			}
			err := finalBoundaryExecute(ctx, pipeline, upstream.URL, finalBoundaryPlan{Input: in, ClientProtocol: Chat, CloudProtocol: Chat, CloudModel: "bound"}, io.Discard)
			if err == nil || dispatched.Load() != 0 || pipeline.Stats().Blocked != 1 {
				t.Fatal("partial recognition permitted outbound work")
			}
			if mode != "cancel" && runtime.calls.Load() != 2 {
				t.Fatal("batch failure did not exercise subsequent batch", runtime.calls.Load())
			}
			if strings.Contains(err.Error(), "Alice") {
				t.Fatal("partial batch failure leaked private runtime error")
			}
		})
	}
}

func TestPrivacyConcurrentRequestsKeepInputAndMappingIsolated(t *testing.T) {
	extractor, _, spec := privacyTestExtractor(t, map[string]string{"Alice": "private_person", "Bob": "private_person"})
	pipeline := New(Options{Extractor: extractor})
	defer pipeline.Close()
	var wg sync.WaitGroup
	errors := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			name := "Alice"
			if index%2 == 1 {
				name = "Bob"
			}
			in := testInput(name)
			in.SessionVerified = false
			in.Versions = spec.Versions()
			prepared, err := pipeline.Prepare(context.Background(), in)
			if err != nil {
				errors <- "prepare failed"
				return
			}
			defer prepared.Close()
			if restored, err := prepared.RestoreText("<PERSON:1>"); err != nil || restored != name {
				errors <- "cross-request private mapping changed"
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
