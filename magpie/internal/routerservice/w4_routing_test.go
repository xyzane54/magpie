package routerservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

type scriptedJudge struct {
	decision Decision
	err      error
	calls    atomic.Int32
	views    chan routerrequest.JudgmentView
}

func (j *scriptedJudge) Judge(_ context.Context, view routerrequest.JudgmentView) (Decision, error) {
	j.calls.Add(1)
	if j.views != nil {
		j.views <- view
	}
	return j.decision, j.err
}

type scriptedInvoker struct {
	err       error
	response  *http.Response
	calls     atomic.Int32
	request   []byte
	committed bool
}

func (i *scriptedInvoker) Invoke(_ context.Context, _ routerruntime.Purpose, body []byte, consume func(*http.Response) error) error {
	i.calls.Add(1)
	i.request = append([]byte(nil), body...)
	if i.response != nil {
		if err := consume(i.response); err != nil {
			return err
		}
		i.committed = true
	}
	return i.err
}

// routedRequest binds a real Profile so executeRoutedModel exercises the same
// immutable target/cancellation context as the HTTP entry point.
func routedRequest(t *testing.T, ctx context.Context, upstream string, redact bool, body []byte) (*http.Request, func(), []byte) {
	t.Helper()
	l, _ := fixture(t)
	store := routerconfig.New(l)
	key := "bound-provider-key"
	p, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "fixture", Protocol: "chat", BaseURL: upstream + "/v1", Model: "provider-model", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveApp(routerconfig.AppEdit{ID: "codex", Name: "Codex", Cloud: routerconfig.AppCloud{ProviderID: p.ID, Model: "profile-pinned"}, CredentialMode: routerconfig.StoredCredential}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://"+"127.0.0.1:13145/app/codex/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1234"
	prepared, cancel, err := routerrequest.Prepare(r, store, cloudHTTPManagement, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if redact {
		// Existing Profiles can retain an enabled redaction intent even while the
		// W5 provider is unavailable; SaveApp correctly refuses to newly enable it.
		state, ok := routerrequest.FromContext(prepared.Context())
		if !ok {
			t.Fatal("missing request state")
		}
		state.Target.Profile.RedactionEnabled = true
	}
	return prepared, cancel, body
}

func modelWriterForTest(w http.ResponseWriter) *modelWriter {
	return &modelWriter{ResponseWriter: w, commit: func() {}}
}

func localChatResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"local","choices":[{"message":{"role":"assistant","content":"local"}}]}`))}
}

func TestRoutedModelJudgesEachRequestAndPreservesExecutionBody(t *testing.T) {
	body := []byte(`{"model":"caller-model","messages":[{"role":"user","content":"read file"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-old","type":"function","function":{"name":"read","arguments":"{\"path\":\"old.txt\"}"}}]},{"role":"tool","tool_call_id":"call-old","content":"old contents"},{"role":"user","content":"summarize"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`)
	judge := &scriptedJudge{decision: DecisionLocal, views: make(chan routerrequest.JudgmentView, 2)}
	invoker := &scriptedInvoker{response: localChatResponse()}
	options := ModelOptions{Routing: &RoutingOptions{Judge: judge, LocalInvoker: invoker}}
	for n := 0; n < 2; n++ {
		prepared, cancel, _ := routedRequest(t, context.Background(), "http://127.0.0.1:1", false, body)
		w := httptest.NewRecorder()
		result, err := executeRoutedModel(w, prepared, "chat", body, options, nil, modelWriterForTest(w))
		cancel()
		if err != nil || result.Destination != "local" || result.Decision != string(DecisionLocal) {
			t.Fatalf("request %d result=%+v err=%v", n, result, err)
		}
	}
	if judge.calls.Load() != 2 || invoker.calls.Load() != 2 {
		t.Fatalf("judge/local calls=%d/%d", judge.calls.Load(), invoker.calls.Load())
	}
	// The local adapter may bind a selected model and re-encode the envelope;
	// complete conversation/tool semantics must remain intact.
	if !bytes.Contains(invoker.request, []byte("old contents")) || !bytes.Contains(invoker.request, []byte("call-old")) {
		t.Fatalf("execution body lost complete history/tools: %s", invoker.request)
	}
	for n := 0; n < 2; n++ {
		view := <-judge.views
		if len(view.History) != 4 || len(view.Tools) != 1 || view.History[1].ToolCalls[0].ID != "call-old" {
			t.Fatalf("judgment %d lost history/tool interaction: %+v", n, view)
		}
	}
}

func TestRoutedModelFallsBackOnceBeforeOutputOnly(t *testing.T) {
	body := []byte(`{"model":"profile-pinned","messages":[{"role":"user","content":"hello"}]}`)
	var cloudCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cloudCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, false, body)
	defer cancel()
	judge := &scriptedJudge{decision: DecisionLocal}
	invoker := &scriptedInvoker{err: errors.New("local process fault")}
	w := httptest.NewRecorder()
	result, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: judge, LocalInvoker: invoker}}, nil, modelWriterForTest(w))
	if err != nil || cloudCalls.Load() != 1 || invoker.calls.Load() != 1 || result.Attempts != 1 || result.Destination != "cloud" {
		t.Fatalf("pre-output fallback result=%+v err=%v cloud=%d local=%d", result, err, cloudCalls.Load(), invoker.calls.Load())
	}
}

func TestRoutedModelDoesNotFallbackAfterOutputOrCancellation(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	for _, tc := range []struct {
		name       string
		cancel     bool
		invokerErr error
		response   *http.Response
	}{
		{name: "output-committed", invokerErr: errors.New("after output"), response: localChatResponse()},
		{name: "caller-canceled", cancel: true, invokerErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cloudCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cloudCalls.Add(1)
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"cloud"}}]}`)
			}))
			defer upstream.Close()
			ctx, cancelCtx := context.WithCancel(context.Background())
			defer cancelCtx()
			prepared, cancel, _ := routedRequest(t, ctx, upstream.URL, false, body)
			defer cancel()
			invoker := &scriptedInvoker{err: tc.invokerErr, response: tc.response}
			if tc.cancel {
				cancelCtx()
			}
			w := httptest.NewRecorder()
			_, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: &scriptedJudge{decision: DecisionLocal}, LocalInvoker: invoker}}, nil, modelWriterForTest(w))
			if cloudCalls.Load() != 0 {
				t.Fatalf("cloud fallback occurred after %s", tc.name)
			}
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded", tc.name)
			}
		})
	}
}

func TestRoutedCloudRedactionUnavailableDoesNotDispatch(t *testing.T) {
	body := []byte(`{"model":"profile-pinned","messages":[{"role":"user","content":"sensitive"}]}`)
	for _, tc := range []struct {
		name  string
		judge *scriptedJudge
		local LocalInvoker
	}{
		{name: "cloud-decision", judge: &scriptedJudge{decision: DecisionCloud}},
		{name: "local-capability-miss", judge: &scriptedJudge{decision: DecisionLocal}},
		{name: "judgment-failure", judge: &scriptedJudge{err: errors.New("judgment failed")}},
		{name: "local-failure", judge: &scriptedJudge{decision: DecisionLocal}, local: &scriptedInvoker{err: errors.New("local process fault")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cloudCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { cloudCalls.Add(1); w.WriteHeader(http.StatusOK) }))
			defer upstream.Close()
			prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, true, body)
			defer cancel()
			w := httptest.NewRecorder()
			_, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: tc.judge, LocalInvoker: tc.local}}, nil, modelWriterForTest(w))
			if !errors.Is(err, ErrRedactionUnavailable) || cloudCalls.Load() != 0 {
				t.Fatalf("required redaction leaked request: err=%v cloud=%d", err, cloudCalls.Load())
			}
		})
	}
}
