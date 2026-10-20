package routerobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }

func ledger(t *testing.T) (*routerledger.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.json")
	s, err := routerledger.Open(path, routerledger.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, path
}
func metadata(id string) routerledger.Metadata {
	return routerledger.Metadata{RequestID: id, AppID: "codex", AppName: "Codex snapshot", Operation: "generate", Path: "/v1/chat/completions", ProviderID: "provider-a", Model: "model-snapshot"}
}
func begin(t *testing.T, s Sink, id string) *Request {
	t.Helper()
	r, err := Begin(s, metadata(id))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func target(base string) routercloud.Target {
	return routercloud.Target{Model: "model-snapshot", SessionID: "fake-private-session", Authorization: "Bearer fake-dynamic-secret", Provider: routerconfig.ResolvedProvider{Config: routerconfig.CloudProvider{ID: "provider-a", BaseURL: base, Protocol: "chat", ProxyMode: "direct"}, APIKey: "fake-provider-secret", Headers: map[string]string{"X-Fake-Secret": "fake-custom-header"}}}
}
func body(stream bool) []byte {
	return []byte(fmt.Sprintf(`{"model":"model-snapshot","stream":%t,"messages":[{"role":"user","content":"fake-private-prompt"}]}`, stream))
}
func reply(usage string) string {
	result := `{"id":"chatcmpl-fake","object":"chat.completion","model":"model-snapshot","choices":[{"index":0,"message":{"role":"assistant","content":"fake-private-response"},"finish_reason":"stop"}]`
	if usage != "" {
		result += `,"usage":` + usage
	}
	return result + "}"
}
func read(t *testing.T, s *routerledger.Store, id string) routerledger.Record {
	t.Helper()
	r, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func verifyNoSecrets(t *testing.T, path string, records ...any) {
	t.Helper()
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(records)
	data = append(data, disk...)
	for _, secret := range []string{"fake-private-prompt", "fake-private-response", "fake-provider-secret", "fake-dynamic-secret", "fake-custom-header", "fake-private-session", "fake-upstream-error", "fake-transport-secret", "fake-code-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("persisted private data %q", secret)
		}
	}
}

func TestExecutorSuccessAndRetryAreOneBusinessRecord(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			s, path := ledger(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer fake-dynamic-secret" {
					t.Error("wrong dynamic auth")
				}
				if retry && n == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":"fake-upstream-error"}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, reply(`{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}`))
			}))
			defer server.Close()
			md := metadata("request-success")
			observed, err := Begin(s, md)
			if err != nil {
				t.Fatal(err)
			}
			md.AppName = "later edited app"
			md.Model = "later edited model"
			result, execErr := (routercloud.Executor{Observe: observed.Observe}).Execute(context.Background(), httptest.NewRecorder(), routercloud.Request{Protocol: "chat", Body: body(false), Target: target(server.URL)})
			if execErr != nil {
				t.Fatal(execErr)
			}
			if err := observed.Finish(result, execErr); err != nil {
				t.Fatal(err)
			}
			if err := observed.Reject(errors.New("fake-transport-secret")); err != nil {
				t.Fatal(err)
			}
			r := read(t, s, "request-success")
			expected := 1
			if retry {
				expected = 2
			}
			if r.Status != "succeeded" || r.Attempts != expected || int(calls.Load()) != expected || len(r.AttemptDetails) != expected || r.EndedAt == nil || r.DurationMS < 0 || r.Destination != "cloud" || !r.OutputCommitted {
				t.Fatalf("bad completed record: %+v", r)
			}
			if r.Model != "model-snapshot" || r.AppName != "Codex snapshot" {
				t.Fatal("profile edit rewrote history")
			}
			if r.Usage == nil || *r.Usage.InputTokens != 7 || *r.Usage.OutputTokens != 2 || *r.Usage.TotalTokens != 9 {
				t.Fatalf("missing actual usage: %+v", r.Usage)
			}
			if retry && (r.AttemptDetails[0].Usage != nil || !r.AttemptDetails[0].Retried || r.AttemptDetails[0].HTTPStatus != 503) {
				t.Fatalf("retry usage/count wrong: %+v", r.AttemptDetails)
			}
			if r.AttemptDetails[expected-1].Usage == nil {
				t.Fatal("final attempt did not own usage")
			}
			page, err := s.List(routerledger.Query{})
			if err != nil || page.Total != 1 {
				t.Fatalf("business request duplicated: %+v %v", page, err)
			}
			verifyNoSecrets(t, path, r)
		})
	}
}

func TestCanceledRetryDoesNotInventAnAttempt(t *testing.T) {
	s, _ := ledger(t)
	observed := begin(t, s, "request-canceled-retry")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	executor := routercloud.Executor{Observe: func(e routercloud.Event) {
		observed.Observe(e)
		if e.Kind == "retry_wait_started" {
			cancel()
		}
	}}
	result, execErr := executor.Execute(ctx, httptest.NewRecorder(), routercloud.Request{Protocol: "chat", Body: body(false), Target: target(server.URL)})
	if err := observed.Finish(result, execErr); err != nil {
		t.Fatal(err)
	}
	r := read(t, s, "request-canceled-retry")
	if r.Status != "canceled" || r.SafeErrorCode != "request_canceled" || r.FailureStage != "retry_wait" || r.Attempts != 1 || len(r.AttemptDetails) != 1 || r.AttemptDetails[0].Retried || calls.Load() != 1 || r.Usage != nil {
		t.Fatalf("canceled wait invented work: %+v", r)
	}
}

func TestPreflightCancellationAndRejectionHaveZeroAttempts(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		cancel               bool
		invalid              bool
		wantStatus, wantCode string
	}{{"invalid", false, true, "failed", "unsupported_input"}, {"canceled", true, false, "canceled", "request_canceled"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := ledger(t)
			observed := begin(t, s, "request-"+tc.name)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			requestBody := body(false)
			if tc.invalid {
				requestBody = []byte(`{"messages":[]}`)
			}
			result, execErr := (routercloud.Executor{Observe: observed.Observe}).Execute(ctx, httptest.NewRecorder(), routercloud.Request{Protocol: "chat", Body: requestBody, Target: target("http://127.0.0.1:1")})
			if err := observed.Finish(result, execErr); err != nil {
				t.Fatal(err)
			}
			r := read(t, s, "request-"+tc.name)
			if r.Status != tc.wantStatus || r.SafeErrorCode != tc.wantCode || r.Attempts != 0 || len(r.AttemptDetails) != 0 || r.Destination != "none" || r.Usage != nil {
				t.Fatalf("preflight counted outbound work: %+v", r)
			}
		})
	}
}

func TestTimeoutAndFailureAfterOutputAreNotSuccess(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			s, path := ledger(t)
			observed := begin(t, s, "request-failure")
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !partial {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fake-private-response\"}}],\"usage\":{\"prompt_tokens\":4}}\n\n")
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			defer server.Close()
			defer close(release)
			timeout := time.Second * 10
			if !partial {
				timeout = 50 * time.Millisecond
			}
			result, execErr := (routercloud.Executor{Timeout: timeout, Observe: observed.Observe}).Execute(context.Background(), httptest.NewRecorder(), routercloud.Request{Protocol: "chat", Body: body(partial), Target: target(server.URL)})
			if execErr == nil {
				t.Fatal("failed execution returned success")
			}
			if err := observed.Finish(result, execErr); err != nil {
				t.Fatal(err)
			}
			r := read(t, s, "request-failure")
			if r.Attempts != 1 || r.Status == "succeeded" || r.OutputCommitted != partial {
				t.Fatalf("wrong failure result: %+v", r)
			}
			if partial && (r.Status != "failed" || r.FailureStage != "response" || r.Usage == nil || *r.Usage.InputTokens != 4 || r.Usage.OutputTokens != nil) {
				t.Fatalf("lost partial failure usage: %+v", r)
			}
			if !partial && (r.Status != "timed_out" || r.SafeErrorCode != "cloud_timeout") {
				t.Fatalf("timeout mislabeled: %+v", r)
			}
			verifyNoSecrets(t, path, r)
		})
	}
}

func TestReportedUsageUnknownZeroAndPartial(t *testing.T) {
	for _, tc := range []struct {
		name, usage    string
		known          bool
		in, out, total *int64
	}{{"absent", "", false, nil, nil, nil}, {"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, true, number(0), number(0), number(0)}, {"partial", `{"prompt_tokens":7}`, true, number(7), nil, nil}, {"nonzero", `{"prompt_tokens":7,"completion_tokens":2}`, true, number(7), number(2), nil}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := ledger(t)
			observed := begin(t, s, "request-usage")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, reply(tc.usage))
			}))
			defer server.Close()
			result, execErr := (routercloud.Executor{Observe: observed.Observe}).Execute(context.Background(), httptest.NewRecorder(), routercloud.Request{Protocol: "chat", Body: body(false), Target: target(server.URL)})
			if execErr != nil {
				t.Fatal(execErr)
			}
			if err := observed.Finish(result, nil); err != nil {
				t.Fatal(err)
			}
			r := read(t, s, "request-usage")
			if (r.Usage != nil) != tc.known {
				t.Fatalf("unknown usage became known: %+v", r.Usage)
			}
			if !tc.known {
				return
			}
			for _, pair := range [][2]*int64{{r.Usage.InputTokens, tc.in}, {r.Usage.OutputTokens, tc.out}, {r.Usage.TotalTokens, tc.total}} {
				if pair[0] == nil && pair[1] == nil {
					continue
				}
				if pair[0] == nil || pair[1] == nil || *pair[0] != *pair[1] {
					t.Fatalf("changed field presence: %+v", r.Usage)
				}
			}
		})
	}
}
func number(n int64) *int64 { return &n }

func TestCloudSuccessPreservesLocalRoutingFailureReason(t *testing.T) {
	for _, reason := range []string{"judgment_queue_timeout", "judgment_stage_timeout", "judgment_inference_timeout", "context_limit_exceeded", "judgment_failed", "execution_timeout", "judgment_summary_incomplete", "fake-code-secret"} {
		t.Run(reason, func(t *testing.T) {
			s, path := ledger(t)
			observed := begin(t, s, "routing-fallback")
			result := routercloud.Result{Operation: "generate", Destination: "cloud", Attempts: 1, OutputCommitted: true, Decision: "cloud", CapabilityState: reason, JudgmentDuration: 30 * time.Second}
			if err := observed.Finish(result, nil); err != nil {
				t.Fatal(err)
			}
			record := read(t, s, "routing-fallback")
			expected := reason
			if reason == "fake-code-secret" {
				expected = "router_error"
			}
			if record.Status != "succeeded" || record.Destination != "cloud" || record.SafeErrorCode != "" || record.CapabilityState != expected || record.JudgmentMS != 30000 {
				t.Fatalf("cloud success hid local fallback: %+v", record)
			}
			verifyNoSecrets(t, path, record)
		})
	}
}

type blockingSink struct {
	calls            atomic.Int32
	entered, release chan struct{}
	outcome          routerledger.Outcome
	err              error
}

func (s *blockingSink) Start(routerledger.Metadata) error { return nil }
func (s *blockingSink) Finish(_ string, o routerledger.Outcome) error {
	s.calls.Add(1)
	s.outcome = o
	close(s.entered)
	<-s.release
	return s.err
}

func TestObserveAndConcurrentFinishKeepCallbackFreeOfPersistence(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("test durable write failed")}
	observed := begin(t, sink, "request-concurrent")
	var workers sync.WaitGroup
	for i := 0; i < 50; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				observed.Observe(routercloud.Event{Kind: "attempt_started", Attempt: 1, At: time.Now()})
				observed.Observe(routercloud.Event{Kind: "attempt_finished", Attempt: 1, ErrorCode: "fake-code-secret"})
			}
		}()
	}
	workers.Wait()
	finished := make(chan error, 2)
	go func() { finished <- observed.Reject(errors.New("fake-transport-secret")) }()
	<-sink.entered
	// A blocked durable write holds no observer lock. This call must return
	// before release is closed; no wall-clock threshold is needed.
	observed.Observe(routercloud.Event{Kind: "attempt_finished", Attempt: 1})
	go func() { finished <- observed.End("succeeded", "", "") }()
	close(sink.release)
	for i := 0; i < 2; i++ {
		if err := <-finished; err != sink.err {
			t.Fatalf("write failure hidden: %v", err)
		}
	}
	if sink.calls.Load() != 1 || sink.outcome.Status != "failed" || sink.outcome.SafeErrorCode != "router_error" || len(sink.outcome.AttemptDetails) != 1 || sink.outcome.AttemptDetails[0].ErrorCode != "router_error" {
		t.Fatalf("unsafe/duplicate completion: %+v", sink.outcome)
	}
}

func TestExternalToolTurnsAreSeparateAndCountingHasNoGenerationUsage(t *testing.T) {
	s, _ := ledger(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "count_tokens") {
			fmt.Fprint(w, `{"input_tokens":9}`)
			return
		}
		fmt.Fprint(w, reply(`{"prompt_tokens":7,"completion_tokens":2}`))
	}))
	defer server.Close()
	for _, id := range []string{"tool-turn-one", "tool-turn-two", "count-operation"} {
		md := metadata(id)
		path := "/v1/chat/completions"
		protocol := "chat"
		requestBody := body(false)
		configured := target(server.URL)
		if id == "count-operation" {
			md.Operation = "count_tokens"
			md.Path = "/v1/messages/count_tokens"
			path = md.Path
			protocol = "anthropic"
			configured.Provider.Config.Protocol = "anthropic"
			requestBody = []byte(`{"model":"model-snapshot","messages":[{"role":"user","content":"fake-private-prompt"}]}`)
		}
		observed, err := Begin(s, md)
		if err != nil {
			t.Fatal(err)
		}
		result, execErr := (routercloud.Executor{Observe: observed.Observe}).ExecutePath(context.Background(), httptest.NewRecorder(), path, routercloud.Request{Protocol: protocol, Body: requestBody, Target: configured})
		if execErr != nil {
			t.Fatal(execErr)
		}
		if err := observed.Finish(result, nil); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := s.Summary(routerledger.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Succeeded != 2 || summary.InputTokens.KnownTokens != 14 {
		t.Fatalf("auxiliary operation counted as generation: %+v", summary)
	}
	if r := read(t, s, "count-operation"); r.Operation != "count_tokens" || r.Usage != nil || r.Attempts != 1 {
		t.Fatalf("count operation mislabeled: %+v", r)
	}
}

func TestPreflightErrorProjectionNeverPersistsRawMessages(t *testing.T) {
	s, path := ledger(t)
	cases := []struct {
		id   string
		err  error
		code string
	}{
		{"unknown-error", errors.New("fake-transport-secret"), "router_error"},
		{"unknown-code", &routercloud.Error{Code: "fake-code-secret", Message: "fake-upstream-error"}, "router_error"},
		{"typed-request", &routerrequest.Error{Code: "credential_missing", Message: "fake-upstream-error"}, "credential_missing"},
	}
	for _, tc := range cases {
		observed := begin(t, s, tc.id)
		if err := observed.Reject(tc.err); err != nil {
			t.Fatal(err)
		}
		record := read(t, s, tc.id)
		if record.Status != "failed" || record.SafeErrorCode != tc.code || record.Attempts != 0 || record.Destination != "none" {
			t.Fatalf("unsafe preflight outcome: %+v", record)
		}
		verifyNoSecrets(t, path, record)
	}
	observed := begin(t, s, "unsafe-end")
	if err := observed.End("fake-code-secret", "fake-code-secret", "fake-code-secret"); err != nil {
		t.Fatal(err)
	}
	record := read(t, s, "unsafe-end")
	if record.Status != "failed" || record.SafeErrorCode != "router_error" || record.FailureStage != "preflight" {
		t.Fatalf("unsafe fixed-end projection: %+v", record)
	}
	verifyNoSecrets(t, path, record)
}

func TestProgressShowsActualAttemptAndFirstOutputBeforeCompletion(t *testing.T) {
	s, _ := ledger(t)
	observed := begin(t, s, "live-request")
	initial := read(t, s, "live-request")
	if initial.Status != "processing" || initial.Attempts != 0 || initial.OutputCommitted {
		t.Fatalf("invented initial progress: %+v", initial)
	}
	observed.Observe(routercloud.Event{Kind: "attempt_started", Attempt: 1, At: time.Now()})
	active := read(t, s, "live-request")
	if active.Status != "processing" || active.Attempts != 1 || active.Destination != "cloud" {
		t.Fatalf("missing real dispatch progress: %+v", active)
	}
	observed.Observe(routercloud.Event{Kind: "output_committed", Attempt: 1, OutputCommitted: true})
	active = read(t, s, "live-request")
	if !active.OutputCommitted || active.FailureStage != "response" {
		t.Fatalf("first output remained hidden until completion: %+v", active)
	}
	if err := observed.End("interrupted", "request_interrupted", "interrupted"); err != nil {
		t.Fatal(err)
	}
	done := read(t, s, "live-request")
	if done.Status != "interrupted" || !done.OutputCommitted || len(done.AttemptDetails) != 1 || !done.AttemptDetails[0].OutputCommitted {
		t.Fatalf("early end lost committed output: %+v", done)
	}
	observed.Observe(routercloud.Event{Kind: "attempt_started", Attempt: 2, At: time.Now()})
	final := read(t, s, "live-request")
	if final.Status != "interrupted" || final.Attempts != 1 {
		t.Fatalf("late event resurrected completed request: %+v", final)
	}
}

func TestFirstOutputCannotBeRevertedByAFinalSummary(t *testing.T) {
	s, _ := ledger(t)
	observed := begin(t, s, "commitment-monotonic")
	started := time.Now()
	observed.Observe(routercloud.Event{Kind: "attempt_started", Attempt: 1, At: started})
	observed.Observe(routercloud.Event{Kind: "output_committed", Attempt: 1, OutputCommitted: true})
	result := routercloud.Result{Attempts: 1, AttemptDetails: []routercloud.Attempt{{Number: 1, StartedAt: started}}}
	if err := observed.Finish(result, &routercloud.Error{Code: "cloud_response_error"}); err != nil {
		t.Fatal(err)
	}
	r := read(t, s, "commitment-monotonic")
	if !r.OutputCommitted || !r.AttemptDetails[0].OutputCommitted || r.Status != "failed" || r.FailureStage != "response" {
		t.Fatalf("committed output was reverted: %+v", r)
	}
}
