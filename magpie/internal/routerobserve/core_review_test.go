package routerobserve

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerruntime"
)

type missingParentReviewSink struct{ failures atomic.Int64 }

func (s *missingParentReviewSink) RecordCall(routerledger.Call) error {
	return routerledger.ErrNotFound
}
func (s *missingParentReviewSink) ReportObservationFailure() { s.failures.Add(1) }

func TestReviewLateOrphanObservationFailureIsVisible(t *testing.T) {
	sink := &missingParentReviewSink{}
	calls := NewCalls(sink, 10)
	defer calls.Close()
	calls.Emit(routerledger.Call{CallID: "late-parent-pruned"})
	if err := calls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.failures.Load() == 0 {
		t.Fatal("accepted delayed event silently lost when its parent no longer exists")
	}
}

// This verifies the consumer seam, not a Manager producer or actual inference.
func TestReviewRuntimeContextSeamBindsOnlyTrustedBusinessIdentity(t *testing.T) {
	s, _ := ledger(t)
	watch := begin(t, s, "context-business")
	calls := NewCalls(s, 32)
	defer calls.Close()
	ctx := WithLocalObserver(context.Background(), calls, "context-business", false, watch)
	observer := routerruntime.LocalCallObserverFromContext(ctx)
	if observer == nil {
		t.Fatal("runtime producer cannot retrieve consumer without importing routerobserve")
	}
	now := time.Now().UTC()
	input := number(7)
	event := routerruntime.LocalCallObservation{CallID: "context-execution", Purpose: routerruntime.LocalCallExecution, Mode: routerruntime.LocalCallFormal, ModelID: "local-model", StartedAt: now, EndedAt: now, Outcome: routerruntime.LocalCallSucceeded, Attempted: true, Usage: &cloudcodec.ReportedUsage{InputTokens: input, OutputTokens: number(2)}}
	event.Settings = routerconfig.LongContextRuntimeSettings()
	event.SettingsFingerprint = event.Settings.Fingerprint()
	event.ContextUsage = &routerruntime.ContextUsage{InputTokens: 7, OutputTokens: 64, ContextTokens: 65536}
	event.HTTPStatus = 200
	observer.ObserveLocalCall(event)
	*input = 999
	event.ContextUsage.InputTokens = 999
	foreign := event
	foreign.RequestID, foreign.CallID = "another-business", "foreign-execution"
	observer.ObserveLocalCall(foreign)
	foreign.RequestID, foreign.Check = "", true
	observer.ObserveLocalCall(foreign)
	if err := watch.Finish(routercloud.Result{Destination: "local"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := calls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	record := read(t, s, "context-business")
	if record.Usage == nil || record.Usage.InputTokens == nil || *record.Usage.InputTokens != 7 || record.LocalCallID != event.CallID || record.LocalModel != event.ModelID {
		t.Fatalf("business execution ownership or immutable usage lost: %+v", record)
	}
	stored, err := s.Calls("context-business")
	if err != nil || len(stored) != 1 || stored[0].Usage == nil || *stored[0].Usage.InputTokens != 7 {
		t.Fatalf("context identity accepted a foreign call or mutated after callback: %+v %v", stored, err)
	}
	if stored[0].ContextUsage == nil || stored[0].ContextUsage.InputTokens != 7 || stored[0].Settings != event.Settings || stored[0].SettingsFingerprint != event.SettingsFingerprint || stored[0].HTTPStatus != 200 {
		t.Fatalf("local budget/capacity snapshot lost or mutated: %+v", stored[0])
	}
	if s.Status().ObservationFailures != 2 {
		t.Fatalf("foreign identity/check misuse not diagnosed: %+v", s.Status())
	}
}

func TestReviewRuntimeDiagnosticContextNeverAssignsBusiness(t *testing.T) {
	s, _ := ledger(t)
	calls := NewCalls(s, 32)
	defer calls.Close()
	ctx := WithLocalObserver(context.Background(), calls, "", true, nil)
	observer := routerruntime.LocalCallObserverFromContext(ctx)
	now := time.Now().UTC()
	event := routerruntime.LocalCallObservation{CallID: "diagnostic-context", Purpose: routerruntime.LocalCallJudgment, Mode: routerruntime.LocalCallDiagnostic, ModelID: "local-model", StartedAt: now, EndedAt: now, Outcome: routerruntime.LocalCallRejected}
	observer.ObserveLocalCall(event)
	event.CallID, event.RequestID = "diagnostic-foreign", "nearby-business"
	observer.ObserveLocalCall(event)
	if err := calls.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Calls("")
	if err != nil || len(stored) != 1 || stored[0].Scope != "check" || stored[0].RequestID != "" {
		t.Fatalf("diagnostic identity conflated with business: %+v %v", stored, err)
	}
	summary, err := s.Summary(routerledger.Query{})
	if err != nil || summary.Total != 0 || s.Status().ObservationFailures != 1 {
		t.Fatalf("diagnostic inflated business or identity loss hidden: %+v %v", summary, err)
	}
}
