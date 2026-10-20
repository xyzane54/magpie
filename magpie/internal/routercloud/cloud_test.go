package routercloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func input(protocol string, stream bool) []byte {
	switch protocol {
	case "chat":
		return []byte(fmt.Sprintf(`{"model":"caller-cannot-select-another-target","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream))
	case "responses":
		return []byte(fmt.Sprintf(`{"model":"caller-cannot-select-another-target","stream":%t,"input":"hello"}`, stream))
	default:
		return []byte(fmt.Sprintf(`{"model":"caller-cannot-select-another-target","stream":%t,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`, stream))
	}
}
func whole(protocol string, usage bool) string {
	var body string
	switch protocol {
	case "chat":
		body = `{"id":"chatcmpl-test","object":"chat.completion","model":"pinned","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]`
		if usage {
			body += `,"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}`
		}
	case "responses":
		body = `{"id":"resp_test","object":"response","status":"completed","model":"pinned","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}]`
		if usage {
			body += `,"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}`
		}
	default:
		body = `{"id":"msg_test","type":"message","role":"assistant","model":"pinned","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"`
		if usage {
			body += `,"usage":{"input_tokens":7,"output_tokens":2}`
		}
	}
	return body + "}"
}
func configured(base, protocol string) Target {
	return Target{Model: "pinned", Provider: routerconfig.ResolvedProvider{Config: routerconfig.CloudProvider{ID: "only-this-provider", BaseURL: base, Protocol: protocol, Model: "provider-default-not-the-profile-model", ProxyMode: "direct"}, APIKey: "fake-cloud-key"}}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestProtocolMatrixPinsTargetAndPreservesUsage(t *testing.T) {
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, upstream := range []string{"chat", "responses", "anthropic"} {
			for _, known := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s/usage-%t", source, upstream, known), func(t *testing.T) {
					target := configured("http://upstream.test/v1", upstream)
					e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						var body map[string]json.RawMessage
						if json.NewDecoder(r.Body).Decode(&body) != nil || string(body["model"]) != `"pinned"` {
							t.Errorf("request escaped Profile model: %s", body["model"])
						}
						want := "/v1/chat/completions"
						if upstream == "responses" {
							want = "/v1/responses"
						}
						if upstream == "anthropic" {
							want = "/v1/messages"
						}
						if r.URL.Path != want || r.GetBody != nil {
							t.Errorf("wrong path or hidden replay: %s", r.URL.Path)
						}
						if upstream == "anthropic" {
							if r.Header.Get("x-api-key") != "fake-cloud-key" || r.Header.Get("anthropic-version") == "" {
								t.Error("missing Anthropic auth")
							}
						} else if r.Header.Get("Authorization") != "Bearer fake-cloud-key" {
							t.Error("missing Bearer auth")
						}
						return response(200, whole(upstream, known)), nil
					})}
					w := httptest.NewRecorder()
					out, err := e.Execute(context.Background(), w, Request{Protocol: source, Body: input(source, false), Target: target})
					if err != nil || out.Attempts != 1 || !out.OutputCommitted || !strings.Contains(w.Body.String(), "hello") || !json.Valid(w.Body.Bytes()) {
						t.Fatalf("result=%+v err=%v body=%s", out, err, w.Body.String())
					}
					if (out.Usage != nil) != known {
						t.Fatalf("usage known=%t, got %+v", known, out.Usage)
					}
					if known && (out.Usage.Input != 7 || out.Usage.Output != 2) {
						t.Fatalf("wrong actual usage: %+v", out.Usage)
					}
				})
			}
		}
	}
}

func TestExecutorMultiTurnToolHistoryAndReply(t *testing.T) {
	requests := map[string]string{
		"chat":      `{"model":"untrusted","messages":[{"role":"user","content":"read file"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_old","type":"function","function":{"name":"run","arguments":"{\"path\":\"old.txt\"}"}}]},{"role":"tool","tool_call_id":"call_old","content":"old file content"},{"role":"user","content":"read next file"}],"tools":[{"type":"function","function":{"name":"run","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`,
		"responses": `{"model":"untrusted","input":[{"role":"user","content":"read file"},{"type":"function_call","call_id":"call_old","name":"run","arguments":"{\"path\":\"old.txt\"}"},{"type":"function_call_output","call_id":"call_old","output":"old file content"},{"role":"user","content":"read next file"}],"tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`,
		"anthropic": `{"model":"untrusted","max_tokens":64,"messages":[{"role":"user","content":"read file"},{"role":"assistant","content":[{"type":"tool_use","id":"call_old","name":"run","input":{"path":"old.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_old","content":"old file content"},{"type":"text","text":"read next file"}]}],"tools":[{"name":"run","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`,
	}
	replies := map[string]string{
		"chat":      `{"id":"chatcmpl-test","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_new","type":"function","function":{"name":"run","arguments":"{\"path\":\"next.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`,
		"responses": `{"id":"resp_test","object":"response","status":"completed","output":[{"type":"function_call","id":"fc_test","call_id":"call_new","name":"run","arguments":"{\"path\":\"next.txt\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":3}}`,
		"anthropic": `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_new","name":"run","input":{"path":"next.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":3}}`,
	}
	for _, source := range []string{"chat", "responses", "anthropic"} {
		for _, upstream := range []string{"chat", "responses", "anthropic"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s/stream-%t", source, upstream, stream), func(t *testing.T) {
					var body map[string]json.RawMessage
					if json.Unmarshal([]byte(requests[source]), &body) != nil {
						t.Fatal("invalid fixture")
					}
					body["stream"], _ = json.Marshal(stream)
					data, _ := json.Marshal(body)
					e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						forwarded, _ := io.ReadAll(r.Body)
						if !json.Valid(forwarded) || !strings.Contains(string(forwarded), "call_old") || !strings.Contains(string(forwarded), "old file content") || !strings.Contains(string(forwarded), "old.txt") {
							t.Fatalf("history or tools lost: %s", forwarded)
						}
						return response(200, replies[upstream]), nil
					})}
					w := httptest.NewRecorder()
					out, err := e.Execute(context.Background(), w, Request{source, data, configured("http://upstream.test/v1", upstream)})
					if err != nil || out.Attempts != 1 || out.Usage == nil || out.Usage.Input != 10 || !out.OutputCommitted || !strings.Contains(w.Body.String(), "call_new") || !strings.Contains(w.Body.String(), "next.txt") {
						t.Fatalf("out=%+v err=%v body=%s", out, err, w.Body.String())
					}
				})
			}
		}
	}
}

func TestRetryOnlyTemporaryStatusesWithSameImmutableRequest(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 409, 422, 429, 500, 501, 502, 503, 504, 505, 307} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var bodies []string
			var addresses []string
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(b))
				addresses = append(addresses, r.URL.String())
				rsp := response(status, `{"error":"fake-cloud-key must never be returned"}`)
				rsp.Header.Set("Retry-After", "0")
				return rsp, nil
			})}
			w := httptest.NewRecorder()
			out, err := e.Execute(context.Background(), w, Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
			want := 1
			if retryStatus(status) {
				want = 2
			}
			if err == nil || out.Attempts != want || len(bodies) != want || out.OutputCommitted || w.Body.Len() != 0 {
				t.Fatalf("status=%d out=%+v err=%v bodies=%d", status, out, err, len(bodies))
			}
			if want == 2 && (bodies[0] != bodies[1] || addresses[0] != addresses[1]) {
				t.Fatal("retry changed model, body or target")
			}
			WriteError(w, "chat", err)
			if strings.Contains(w.Body.String(), "fake-cloud-key") || strings.Contains(w.Body.String(), "upstream.test") {
				t.Fatal("unsafe upstream error exposed")
			}
		})
	}
}

func TestRetryAfterAndBudget(t *testing.T) {
	for _, tc := range []struct {
		value   string
		delay   time.Duration
		allowed bool
	}{
		{"", time.Second, true}, {"0", 0, true}, {"3", 3 * time.Second, true}, {"4", 0, false}, {"999999999999999999", 0, false}, {"999999999999999999999999", 0, false}, {"-1", 0, false}, {"nonsense", time.Second, true},
	} {
		delay, allowed := retryDelay(tc.value, time.Now())
		if delay != tc.delay || allowed != tc.allowed {
			t.Errorf("%q: %s/%t", tc.value, delay, allowed)
		}
	}
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if delay, ok := retryDelay(now.Add(4*time.Second).Format(http.TimeFormat), now); ok || delay != 4*time.Second {
		t.Fatal("HTTP-date wait truncated")
	}
	if delay, ok := retryDelay(now.Add(3*time.Second).Format(http.TimeFormat), now); !ok || delay != 3*time.Second {
		t.Fatal("valid HTTP-date rejected")
	}
	for _, wait := range []string{"4", "3"} {
		t.Run(wait, func(t *testing.T) {
			var calls atomic.Int32
			e := Executor{Timeout: 30 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				rsp := response(429, "")
				rsp.Header.Set("Retry-After", wait)
				return rsp, nil
			})}
			out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
			if err == nil || out.Attempts != 1 || calls.Load() != 1 {
				t.Fatalf("wait %s: %+v %v", wait, out, err)
			}
			var typed *Error
			if !errors.As(err, &typed) {
				t.Fatal(err)
			}
			if wait == "3" && typed.Code != "cloud_timeout" {
				t.Fatalf("retry got a new budget: %v", err)
			}
		})
	}
}

func TestCancellationDuringRetryWaitStartsNoSecondAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return response(503, ""), nil })}
	out, err := e.Execute(ctx, httptest.NewRecorder(), Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "request_canceled" || calls != 1 || out.Attempts != 1 {
		t.Fatalf("%+v %v calls=%d", out, err, calls)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(entered); done <- waitRetry(ctx2, time.Hour) }()
	<-entered
	cancel2()
	if err := <-done; err == nil {
		t.Fatal("wait ignored cancellation")
	}
}

func TestRejectBeforeAnyOutboundAndRedactionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		protocol, body string
		redact         bool
	}{
		{"chat", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://private.test/a.png"}}]}]}`, false},
		{"responses", `{"input":"hello","previous_response_id":"secret"}`, false},
		{"anthropic", `{"messages":[],"unknown_option":true}`, false},
		{"chat", string(input("chat", false)), true},
	} {
		calls := 0
		e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, whole("chat", true)), nil })}
		target := configured("http://upstream.test/v1", "chat")
		target.RedactionRequired = tc.redact
		out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{tc.protocol, []byte(tc.body), target})
		if err == nil || calls != 0 || out.Attempts != 0 {
			t.Fatalf("unsupported input reached cloud: %+v %v calls=%d", out, err, calls)
		}
	}
}

func TestAdvancedOptionsDoNotMutateOrOverrideTarget(t *testing.T) {
	for _, key := range []string{"model", "!model", "messages", "input", "tools", "stream", "previous_response_id", "conversation", "background", "store", "web_search_options", "plugins", "mcp_servers", "prompt", "tool_resources", "models", "provider", "route", "fallbacks"} {
		if _, err := extraBody(input("chat", false), map[string]json.RawMessage{key: json.RawMessage(`"bad"`)}, ""); err == nil {
			t.Errorf("accepted override %s", key)
		}
	}
	for _, session := range []string{"bad\r\nsession", "bad\x00session"} {
		target := configured("http://upstream.test/v1", "chat")
		target.SessionID = session
		target.Provider.Headers = map[string]string{"X-Session": "${sessionId}"}
		calls := 0
		executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dispatch") })}
		out, err := executor.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), target})
		if err == nil || out.Attempts != 0 || calls != 0 {
			t.Fatalf("invalid session dispatched: %+v %v", out, err)
		}
	}
	target := configured("http://upstream.test/v1", "chat")
	target.SessionID = "session-\"\\\n中文"
	target.Provider.Headers = map[string]string{"X-Session": "${sessionId}", "Idempotency-Key": "safe"}
	// A header cannot contain an arbitrary raw session ID; use a safe native ID.
	target.SessionID = "session_123"
	target.Provider.ExtraBody = map[string]json.RawMessage{"metadata": json.RawMessage(`{"session":"${sessionId}","n":9007199254740993}`)}
	e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"n":9007199254740993`) || !strings.Contains(string(b), `"session":"session_123"`) || r.Header.Get("X-Session") != "session_123" || r.GetBody != nil {
			t.Fatalf("unsafe advanced options %s", b)
		}
		return response(200, whole("chat", true)), nil
	})}
	if _, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), target}); err != nil {
		t.Fatal(err)
	}
	if string(target.Provider.ExtraBody["metadata"]) != `{"session":"${sessionId}","n":9007199254740993}` {
		t.Fatal("saved options mutated")
	}
	b, err := extraBody(input("chat", false), map[string]json.RawMessage{"user": json.RawMessage(`"${sessionId}"`)}, "\"\\\n中文")
	if err != nil || !json.Valid(b) {
		t.Fatal("session replacement corrupted JSON")
	}
}

func TestRealTransportRedirectAndCancellation(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer server.Close()
	out, err := (Executor{}).Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), configured(server.URL+"/v1", "chat")})
	if err == nil || out.Attempts != 1 || redirected.Load() != 0 {
		t.Fatalf("followed credential redirect: %+v %v", out, err)
	}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request before waiting so the server detects peer cancellation.
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer blocked.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (Executor{}).Execute(ctx, httptest.NewRecorder(), Request{"chat", input("chat", false), configured(blocked.URL+"/v1", "chat")})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled request succeeded")
	}
	<-canceled
}

func TestAuxiliaryRoutesStayProfileConstrained(t *testing.T) {
	target := configured("http://unused.test/v1", "chat")
	if got := Models(target); len(got) != 1 || got[0] != "pinned" {
		t.Fatal(got)
	}
	for _, path := range []string{"/v1/messages/count_tokens", "/v1/images/generations", "/v1/responses/resp_any", "/v1/embeddings", "/v1/models"} {
		if _, ok := ProtocolForPath(path); ok {
			t.Fatalf("accepted unsupported route %s", path)
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		if _, ok := ProtocolForPath(path); !ok {
			t.Fatal(path)
		}
	}
}

func TestConfiguredEndpointPrefixIsPreserved(t *testing.T) {
	for _, tc := range []struct{ base, protocol, want string }{
		{"https://spark-api-open.xf-yun.com/v1", "chat", "https://spark-api-open.xf-yun.com/v1/chat/completions"},
		{"https://upstream.test/v2/", "chat", "https://upstream.test/v2/chat/completions"},
		{"https://upstream.test/agent/v1", "chat", "https://upstream.test/agent/v1/chat/completions"},
		{"https://upstream.test", "anthropic", "https://upstream.test/v1/messages"},
		{"https://upstream.test/v1/", "anthropic", "https://upstream.test/v1/messages"},
		{"https://upstream.test/tenant%2Fone/v1", "responses", "https://upstream.test/tenant%2Fone/v1/responses"},
	} {
		got, err := targetURL(tc.base, tc.protocol)
		if err != nil || got != tc.want {
			t.Errorf("%s %s: %s %v", tc.base, tc.protocol, got, err)
		}
	}
	for _, base := range []string{"file:///private", "https://key@upstream.test/v1", "https://upstream.test/v1?api_key=secret", "https://upstream.test/v1#fragment"} {
		if _, err := targetURL(base, "chat"); err == nil {
			t.Errorf("accepted invalid target %s", base)
		}
	}
}

func TestProtocolErrorEnvelopes(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, status := range []int{400, 401, 403, 404, 429, 503, 504} {
			w := httptest.NewRecorder()
			WriteError(w, protocol, failure("business_failure", "safe failure", status))
			var envelope struct {
				Type  string                               `json:"type"`
				Error struct{ Type, Code, Message string } `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || w.Code != status || envelope.Error.Type == "business_failure" || envelope.Error.Message != "safe failure" {
				t.Fatalf("%s/%d: %s", protocol, status, w.Body.String())
			}
			if protocol == "anthropic" {
				if envelope.Type != "error" || envelope.Error.Code != "" {
					t.Fatal(w.Body.String())
				}
			} else if envelope.Error.Code != "business_failure" {
				t.Fatal(w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	WriteError(w, "responses", errors.New("private-key-reflected-by-upstream"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private-key") {
		t.Fatal(w.Body.String())
	}
}

type failingReader struct {
	prefix string
	failed bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.prefix != "" {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	if !r.failed {
		r.failed = true
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.EOF
}

func TestRetryNetworkFailureOnlyBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name         string
		firstError   error
		body         io.Reader
		stream       bool
		wantAttempts int
	}{
		{"temporary-connection", syscall.ECONNRESET, nil, false, 2},
		{"permanent-connection", errors.New("secret-address-and-key"), nil, false, 1},
		{"response-before-output", nil, &failingReader{}, false, 2},
		{"response-after-text", nil, &failingReader{prefix: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if tc.firstError != nil {
						return nil, tc.firstError
					}
					rsp := response(200, "")
					rsp.Body = io.NopCloser(tc.body)
					if tc.stream {
						rsp.Header.Set("Content-Type", "text/event-stream")
					}
					return rsp, nil
				}
				return response(200, whole("chat", true)), nil
			})}
			w := httptest.NewRecorder()
			out, err := e.Execute(context.Background(), w, Request{"chat", input("chat", tc.stream), configured("http://upstream.test/v1", "chat")})
			if calls != tc.wantAttempts || out.Attempts != tc.wantAttempts {
				t.Fatalf("attempts: %+v calls=%d err=%v", out, calls, err)
			}
			if tc.wantAttempts == 2 && err != nil {
				t.Fatal(err)
			}
			if tc.wantAttempts == 1 && err == nil {
				t.Fatal("failed request succeeded")
			}
			if tc.stream && (!out.OutputCommitted || strings.Contains(w.Body.String(), "[DONE]")) {
				t.Fatalf("partial stream falsely finished: %+v %s", out, w.Body.String())
			}
		})
	}
}
