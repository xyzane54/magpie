package routerledger

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

func callFixture(requestID, callID string) Call {
	started := time.Now().Add(-time.Second).UTC()
	return Call{RequestID: requestID, CallID: callID, Scope: "business", Purpose: "judgment", ModelID: "spark-x2.5-1.7B", Fingerprint: "abcdef", RuntimeID: "llama-cpp", RuntimeFingerprint: "runtime-sha256", MaterialVersion: "judgment-v1:abcdef", PromptVersion: "prompt-v1", Suite: "judgment-v1:abcdef", Mode: "formal", StartedAt: started, EndedAt: started.Add(time.Second), Result: "succeeded", InferenceStarted: true, Usage: &Usage{InputTokens: integer(13), OutputTokens: integer(0)}, Timings: CallTimings{QueueLoadMS: integer(0), TotalMS: integer(1000)}}
}

func TestLocalBudgetAndCapacitySnapshotPersistsSafely(t *testing.T) {
	s, _ := openTest(t, Options{})
	if err := s.Start(metadata("capacity")); err != nil {
		t.Fatal(err)
	}
	call := callFixture("capacity", "checked-context")
	call.Settings = routerconfig.LongContextRuntimeSettings()
	call.SettingsFingerprint = call.Settings.Fingerprint()
	call.ContextUsage = &CallContextUsage{InputTokens: 70000, OutputTokens: 32, ContextTokens: 65536}
	call.Result, call.SafeErrorCode, call.HTTPStatus = "failed", "local_context_exceeded", 400
	if err := s.RecordCall(call); err != nil {
		t.Fatal(err)
	}
	calls, err := s.Calls("capacity")
	if err != nil || len(calls) != 1 || calls[0].Settings != call.Settings || calls[0].SettingsFingerprint != call.SettingsFingerprint || calls[0].ContextUsage == nil || *calls[0].ContextUsage != *call.ContextUsage || calls[0].HTTPStatus != 400 || calls[0].SafeErrorCode != call.SafeErrorCode {
		t.Fatalf("lost safe capacity/budget snapshot: %+v %v", calls, err)
	}
	for _, mutate := range []func(*Call){func(c *Call) { c.SettingsFingerprint = "Authorization: Bearer secret" }, func(c *Call) { c.Settings.ContextTokens = 8192 }, func(c *Call) { c.Settings.RequestSeconds = -1 }, func(c *Call) {
		c.ContextUsage = &CallContextUsage{InputTokens: -1, OutputTokens: 32, ContextTokens: 65536}
	}, func(c *Call) { c.HTTPStatus = 999 }} {
		bad := call
		bad.CallID = "invalid-snapshot"
		mutate(&bad)
		if err := s.RecordCall(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe snapshot accepted: %+v %v", bad, err)
		}
	}
}

func TestZeroRemainingCapacityPersistsOnlyBeforeFailedAdmission(t *testing.T) {
	s, _ := openTest(t, Options{})
	if err := s.Start(metadata("zero-capacity")); err != nil {
		t.Fatal(err)
	}
	call := callFixture("zero-capacity", "rejected-with-no-capacity")
	call.Result, call.SafeErrorCode, call.InferenceStarted, call.Usage = "failed", "local_context_exceeded", false, nil
	call.ContextUsage = &CallContextUsage{InputTokens: 70000, OutputTokens: 0, ContextTokens: 65536}
	if err := s.RecordCall(call); err != nil {
		t.Fatalf("valid exhausted-capacity snapshot lost: %v", err)
	}
	stored, err := s.Calls("zero-capacity")
	if err != nil || len(stored) != 1 || stored[0].ContextUsage == nil || stored[0].ContextUsage.OutputTokens != 0 {
		t.Fatalf("zero capacity did not roundtrip: %+v %v", stored, err)
	}
	for name, mutate := range map[string]func(*Call){
		"negative": func(c *Call) {
			c.ContextUsage = &CallContextUsage{InputTokens: 70000, OutputTokens: -1, ContextTokens: 65536}
		},
		"successful":        func(c *Call) { c.Result = "succeeded"; c.SafeErrorCode = "" },
		"inference started": func(c *Call) { c.InferenceStarted = true },
	} {
		bad := call
		bad.CallID = "invalid-capacity-" + strings.ReplaceAll(name, " ", "-")
		mutate(&bad)
		if err := s.RecordCall(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid %s capacity accepted: %v", name, err)
		}
	}
}

func TestInternalCallsConcurrentDuplicateLateAndRestart(t *testing.T) {
	s, path := openTest(t, Options{})
	if err := s.Start(metadata("business")); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("business", success()); err != nil {
		t.Fatal(err)
	}
	first := callFixture("business", "same-call")
	if err := s.RecordCall(first); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			c := first
			c.Result = "failed"
			c.Usage = &Usage{InputTokens: integer(int64(i + 100))}
			errs <- s.RecordCall(c)
		}(i)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	calls, err := s.Calls("business")
	if err != nil || len(calls) != 1 || calls[0].Result != "succeeded" || *calls[0].Usage.InputTokens != 13 {
		t.Fatalf("duplicate terminal changed first result: %+v %v", calls, err)
	}
	if summary, err := s.Summary(Query{}); err != nil || summary.Total != 1 || summary.Attempts != 1 || summary.InputTokens.KnownTokens != 13 {
		t.Fatalf("calls increased business count/usage: %+v %v", summary, err)
	}
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first.Result = "failed"
	if err := s.RecordCall(first); err != nil {
		t.Fatal(err)
	}
	calls, err = s.Calls("business")
	if err != nil || len(calls) != 1 || calls[0].Result != "succeeded" || calls[0].RuntimeID != "llama-cpp" || calls[0].RuntimeFingerprint != "runtime-sha256" || calls[0].PromptVersion != "prompt-v1" || calls[0].Suite != "judgment-v1:abcdef" {
		t.Fatalf("restart lost deduplication: %+v %v", calls, err)
	}
	other := first
	other.Scope = "check"
	other.RequestID = ""
	if err := s.RecordCall(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity reassigned to diagnosis: %v", err)
	}
}

func TestInternalCallsUnknownZeroAndChecksStaySeparate(t *testing.T) {
	s, _ := openTest(t, Options{})
	if err := s.Start(metadata("business")); err != nil {
		t.Fatal(err)
	}
	unknown := callFixture("business", "unknown")
	unknown.Usage = nil
	unknown.Timings = CallTimings{}
	zero := callFixture("business", "zero")
	zero.Usage = &Usage{InputTokens: integer(0), OutputTokens: integer(0), TotalTokens: integer(0)}
	rejected := callFixture("business", "not-started")
	rejected.Result = "rejected"
	rejected.InferenceStarted = false
	rejected.Usage = nil
	rejected.Timings = CallTimings{QueueLoadMS: integer(0)}
	check := callFixture("", "diagnostic")
	check.Scope = "check"
	check.Mode = "diagnostic"
	for _, c := range []Call{unknown, zero, rejected, check} {
		if err := s.RecordCall(c); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := s.Calls("business")
	if err != nil || len(calls) != 3 {
		t.Fatalf("parent calls=%+v %v", calls, err)
	}
	byID := map[string]Call{}
	for _, c := range calls {
		byID[c.CallID] = c
	}
	if byID["unknown"].Usage != nil || byID["unknown"].Timings.TotalMS != nil || byID["zero"].Usage.TotalTokens == nil || *byID["zero"].Usage.TotalTokens != 0 || byID["not-started"].InferenceStarted {
		t.Fatal("unknown/zero/not-started collapsed")
	}
	checks, err := s.Calls("")
	if err != nil || len(checks) != 1 || checks[0].RequestID != "" {
		t.Fatalf("diagnostic attributed to recent request: %+v %v", checks, err)
	}
	rejected.CallID = "invented-usage"
	rejected.Usage = &Usage{TotalTokens: integer(0)}
	if err := s.RecordCall(rejected); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not-started reported fabricated usage: %v", err)
	}
	orphan := callFixture("missing", "orphan")
	if err := s.RecordCall(orphan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown parent admitted: %v", err)
	}
	unknown.CallID = "wrong-scope-diagnostic"
	unknown.Mode = "diagnostic"
	if err := s.RecordCall(unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("diagnostic counted as business call: %v", err)
	}
}

func TestInternalCallCapacityRetentionAndParentCleanup(t *testing.T) {
	s, _ := openTest(t, Options{MaxRecords: 1, MaxCalls: 3, MaxCallsPerRequest: 2, MaxCheckCalls: 1})
	if err := s.Start(metadata("old")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := s.RecordCall(callFixture("old", id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordCall(callFixture("old", "excess")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("per-parent cap=%v", err)
	}
	if err := s.RecordCall(callFixture("old", "one")); err != nil {
		t.Fatalf("duplicate at cap=%v", err)
	}
	check := callFixture("", "check-one")
	check.Scope = "check"
	if err := s.RecordCall(check); err != nil {
		t.Fatal(err)
	}
	check.CallID = "check-excess"
	if err := s.RecordCall(check); !errors.Is(err, ErrCapacity) {
		t.Fatalf("diagnosis/global cap=%v", err)
	}
	if err := s.Finish("old", success()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(metadata("new")); err != nil {
		t.Fatal(err)
	}
	if calls, err := s.Calls("old"); err != nil || len(calls) != 0 {
		t.Fatalf("evicted parent leaked internal records: %+v %v", calls, err)
	}
	if err := s.RecordCall(callFixture("old", "late-old")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late event resurrected evicted request: %v", err)
	}
	if err := s.RecordCall(callFixture("new", "new-call")); err != nil {
		t.Fatal(err)
	}
	// Independent diagnostic retention uses the same age cap, by actual call start.
	if _, err := s.db.Exec("UPDATE calls SET started_ns=1 WHERE scope='check'"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(check); err != nil {
		t.Fatal(err)
	}
	checks, err := s.Calls("")
	if err != nil || len(checks) != 1 || checks[0].CallID != "check-excess" {
		t.Fatalf("diagnostic retention failed: %+v %v", checks, err)
	}
}

func TestInternalCallRejectsSensitiveFieldsAndCorruptRestart(t *testing.T) {
	s, path := openTest(t, Options{})
	if err := s.Start(metadata("safe")); err != nil {
		t.Fatal(err)
	}
	secret := "Bearer fake-secret-never-persist"
	for i, mutate := range []func(*Call){func(c *Call) { c.SafeErrorCode = secret }, func(c *Call) { c.ModelID = secret }, func(c *Call) { c.Fingerprint = secret }, func(c *Call) { c.MaterialVersion = secret }, func(c *Call) { c.MaterialVersion = "https://private.example/prompt" }, func(c *Call) { c.Mode = secret }, func(c *Call) { c.RuntimeID = secret }, func(c *Call) { c.RuntimeFingerprint = secret }, func(c *Call) { c.PromptVersion = secret }, func(c *Call) { c.Suite = secret }} {
		c := callFixture("safe", fmt.Sprintf("bad-%d", i))
		mutate(&c)
		if err := s.RecordCall(c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe metric accepted: %v", err)
		}
	}
	if err := s.RecordCall(callFixture("safe", "valid")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("sensitive observation reached SQLite")
	}
	if _, err := s.db.Exec(`UPDATE calls SET data=json_set(data,'$.body','fake-secret')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path, Options{MaxAge: time.Second}); err == nil {
		reopened.Close()
		t.Fatal("invalid internal record hidden by cleanup")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("bad internal database modified on reopen")
	}
}

func TestReferencePriceAndLocalIdentitySnapshotsPersist(t *testing.T) {
	s, path := openTest(t, Options{})
	m := metadata("local")
	m.ReferencePrice = &ReferencePrice{Currency: "CNY", Unit: "million-tokens", Input: 1.25, Output: 2.5, Version: "2026-10", Source: "user reference"}
	if err := s.Start(m); err != nil {
		t.Fatal(err)
	}
	m.ReferencePrice.Input = 999
	m.ReferencePrice.Version = "later"
	o := Outcome{Status: "succeeded", Destination: "local", LocalModel: "spark-x2.5-1.7B", LocalCallID: "final-local-execution", Usage: &Usage{InputTokens: integer(0), OutputTokens: integer(20)}}
	if err := s.Finish("local", o); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := get(t, s, "local")
	if r.ReferencePrice == nil || r.ReferencePrice.Input != 1.25 || r.ReferencePrice.Version != "2026-10" || r.Model != "model-at-request-time" || r.LocalModel != "spark-x2.5-1.7B" || r.LocalCallID != "final-local-execution" {
		t.Fatalf("request-time price or cloud/local identity overwritten: %+v", r)
	}
	bad := metadata("bad-price")
	bad.ReferencePrice = &ReferencePrice{Currency: "CNY", Unit: "million-tokens", Input: 1, Output: 2, Version: "v1", Source: "https://private.example/key"}
	if err := s.Start(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sensitive price source admitted: %v", err)
	}
}

func TestIncompleteReferencePriceNeverBecomesFreeOnRestart(t *testing.T) {
	for _, statement := range []string{
		`UPDATE requests SET data=json_remove(data,'$.referencePrice.input')`,
		`UPDATE requests SET data=json_remove(data,'$.referencePrice.output')`,
		`UPDATE requests SET data=json_set(data,'$.referencePrice.input',NULL)`,
		`UPDATE requests SET data=json_set(data,'$.referencePrice.output',NULL)`,
		`UPDATE requests SET data=json_remove(data,'$.referencePrice.currency')`,
		`UPDATE requests SET data=json_set(data,'$.referencePrice.unit',NULL)`,
	} {
		t.Run(statement, func(t *testing.T) {
			s, path := openTest(t, Options{})
			m := metadata("priced")
			m.ReferencePrice = &ReferencePrice{Currency: "USD", Unit: "million-tokens", Input: 0, Output: 0, Version: "v1", Source: "manual"}
			if err := s.Start(m); err != nil {
				t.Fatal(err)
			}
			if err := s.Finish(m.RequestID, success()); err != nil {
				t.Fatal(err)
			}
			if r := get(t, s, m.RequestID); r.ReferencePrice == nil || r.ReferencePrice.Input != 0 || r.ReferencePrice.Output != 0 {
				t.Fatal("explicit zero reference price lost")
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			s.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path, Options{MaxAge: time.Second}); !errors.Is(err, ErrCorrupt) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("incomplete price became free instead of corrupt: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("incomplete-price database changed on rejected restart")
			}
		})
	}
}

func TestObservationFailureNeverWaitsForStorage(t *testing.T) {
	s, _ := openTest(t, Options{})
	s.mu.Lock()
	done := make(chan struct{})
	go func() { s.ReportObservationFailure(); close(done) }()
	select {
	case <-done:
		s.mu.Unlock()
	case <-time.After(5 * time.Second):
		s.mu.Unlock()
		<-done
		t.Fatal("observation failure blocked on SQL lock")
	}
	if status := s.Status(); status.ObservationFailures != 1 {
		t.Fatalf("dropped event hidden: %+v", status)
	}
}
