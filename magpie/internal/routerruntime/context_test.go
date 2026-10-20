package routerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
)

func TestGenerationCountsCompleteRequestAndRetainsExplicitOutputLimit(t *testing.T) {
	var counted, generated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body["messages"]), "original-message") || !strings.Contains(string(body["tools"]), "original-tool") {
			t.Error("capacity check or generation lost request semantics")
		}
		switch r.URL.Path {
		case "/v1/chat/completions/input_tokens":
			counted.Add(1)
			_, _ = io.WriteString(w, `{"input_tokens":38000}`)
		case "/v1/chat/completions":
			generated.Add(1)
			if string(body["max_tokens"]) != "2048" {
				t.Errorf("explicit output limit changed: %s", body["max_tokens"])
			}
			_, _ = io.WriteString(w, `{"choices":[]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.endpoint = server.URL })
	settings := routerconfig.LongContextRuntimeSettings()
	if err := m.UpdateSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	var usage ContextUsage
	err := m.Generate(WithContextUsage(context.Background(), &usage), Execution, []byte(`{"messages":[{"role":"user","content":"original-message"}],"tools":[{"type":"function","function":{"name":"original-tool","parameters":{"type":"object"}}}],"max_tokens":2048}`), func(r *http.Response) error { _, err := io.Copy(io.Discard, r.Body); return err })
	if err != nil {
		t.Fatal(err)
	}
	if usage != (ContextUsage{InputTokens: 38000, OutputTokens: 2048, ContextTokens: 65536}) || counted.Load() != 1 || generated.Load() != 1 {
		t.Fatalf("precise check missing or repeated: %+v count=%d generate=%d", usage, counted.Load(), generated.Load())
	}
}

func TestExhaustedContextCapacityObservationCanPersistWithoutInference(t *testing.T) {
	for _, input := range []int{65536, 70000} {
		t.Run(strconv.Itoa(input), func(t *testing.T) {
			var generated atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/chat/completions/input_tokens" {
					_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": input})
					return
				}
				generated.Add(1)
				_, _ = io.WriteString(w, `{"choices":[]}`)
			}))
			defer server.Close()
			calls := make(chan LocalCallObservation, 1)
			m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
				f.endpoint = server.URL
				o.LocalObserver = LocalCallObserverFunc(func(v LocalCallObservation) { calls <- v })
			})
			if err := m.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
				t.Fatal(err)
			}
			ctx := WithLocalCallContext(context.Background(), LocalCallContext{Check: true, Mode: LocalCallDiagnostic})
			err := m.Generate(ctx, Execution, []byte(`{"messages":[{"role":"user","content":"unchanged"}]}`), func(*http.Response) error { t.Error("exhausted context reached consumer"); return nil })
			if !errors.Is(err, ErrContextExceeded) || generated.Load() != 0 {
				t.Fatalf("exhausted context reached inference: err=%v calls=%d", err, generated.Load())
			}
			observation := <-calls
			if observation.Attempted || observation.ContextUsage == nil || observation.ContextUsage.InputTokens != input || observation.ContextUsage.OutputTokens != 0 || observation.ErrorCode != ErrContextExceeded.Error() {
				t.Fatalf("capacity observation is missing or invalid: %+v", observation)
			}
			store, err := routerledger.Open(filepath.Join(t.TempDir(), "capacity.sqlite"), routerledger.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			u := observation.ContextUsage
			call := routerledger.Call{CallID: observation.CallID, Scope: "check", Purpose: string(observation.Purpose), ModelID: observation.ModelID, Fingerprint: observation.ModelFingerprint, RuntimeID: observation.RuntimeID, RuntimeFingerprint: observation.RuntimeFingerprint, MaterialVersion: observation.MaterialVersion, Mode: string(observation.Mode), StartedAt: observation.StartedAt, EndedAt: observation.EndedAt, Result: string(observation.Outcome), SafeErrorCode: observation.ErrorCode, InferenceStarted: observation.Attempted, Settings: observation.Settings, SettingsFingerprint: observation.SettingsFingerprint, ContextUsage: &routerledger.CallContextUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, ContextTokens: u.ContextTokens}}
			if err := store.RecordCall(call); err != nil {
				t.Fatalf("exhausted context observation was discarded: %v", err)
			}
		})
	}
}

func TestGenerationRejectsContextAndJudgmentOverflowBeforeInference(t *testing.T) {
	for _, test := range []struct {
		name    string
		purpose Purpose
		tokens  int
		want    error
	}{{"full execution", Execution, 65000, ErrContextExceeded}, {"bounded judgment", Judgment, 2049, ErrJudgmentInputExceeded}} {
		t.Run(test.name, func(t *testing.T) {
			var generated atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/chat/completions/input_tokens" {
					_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": test.tokens})
					return
				}
				generated.Add(1)
				_, _ = io.WriteString(w, `{"choices":[]}`)
			}))
			defer server.Close()
			m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.endpoint = server.URL })
			if err := m.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
				t.Fatal(err)
			}
			err := m.Generate(context.Background(), test.purpose, []byte(`{"messages":[{"role":"user","content":"retained"}],"max_tokens":1024}`), func(*http.Response) error { t.Fatal("over-limit request reached response consumer"); return nil })
			if !errors.Is(err, test.want) || generated.Load() != 0 {
				t.Fatalf("overflow reached inference: %v calls=%d", err, generated.Load())
			}
		})
	}
}

func TestUnboundedOutputUsesRemainingWindowAndTokenFailureFailsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/chat/completions/input_tokens" {
				if fail {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"secret":"not logged"}`)
					return
				}
				_, _ = io.WriteString(w, `{"input_tokens":64000}`)
				return
			}
			var body struct {
				MaxTokens int `json:"max_tokens"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.MaxTokens != 1536 {
				t.Errorf("remaining window was not enforced: %d", body.MaxTokens)
			}
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
		m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.endpoint = server.URL })
		if err := m.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
			t.Fatal(err)
		}
		err := m.Generate(context.Background(), Execution, []byte(`{"messages":[]}`), func(r *http.Response) error { _, e := io.Copy(io.Discard, r.Body); return e })
		if fail && !errors.Is(err, ErrTokenCountUnavailable) {
			t.Fatalf("unavailable tokenizer did not fail closed: %v", err)
		}
		if !fail && err != nil {
			t.Fatal(err)
		}
		server.Close()
	}
}

func TestLocalHTTPContextErrorKeepsSafeStatusWithoutBody(t *testing.T) {
	err := newGenerationHTTPError(400, []byte(`{"error":{"type":"exceed_context_size_error","message":"synthetic-secret"}}`), false)
	if !errors.Is(err, ErrContextExceeded) || err.HTTPStatus != 400 || len(err.DiagnosticBody()) != 0 {
		t.Fatal("typed context overflow lost safe classification")
	}
	if strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("context error exposed body")
	}
	if errors.Is(newGenerationHTTPError(400, []byte(`{"error":{"type":"invalid_request_error"}}`), false), ErrContextExceeded) {
		t.Fatal("generic HTTP 400 mislabeled as context overflow")
	}
}

func TestOutputReserveUsesPinnedRuntimeAliasPriorityAndNullRules(t *testing.T) {
	for _, test := range []struct {
		body    string
		want    int
		invalid bool
	}{
		{`{"n_predict":512,"max_completion_tokens":256,"max_tokens":128}`, 512, false},
		{`{"n_predict":null,"max_completion_tokens":256,"max_tokens":128}`, 256, false},
		{`{"max_completion_tokens":null,"max_tokens":128}`, 128, false},
		{`{"max_tokens":null}`, 0, false},
		{`{"n_predict":-1,"max_tokens":128}`, 0, true},
		{`{"n_predict":0}`, 0, true},
	} {
		var request map[string]json.RawMessage
		if err := json.Unmarshal([]byte(test.body), &request); err != nil {
			t.Fatal(err)
		}
		got, err := requestOutputReserve(request)
		if test.invalid {
			if !errors.Is(err, ErrContextExceeded) {
				t.Fatalf("unsafe native budget accepted: %s: %v", test.body, err)
			}
		} else if err != nil || got != test.want {
			t.Fatalf("runtime output alias mismatch: %s got=%d err=%v", test.body, got, err)
		}
	}
}
