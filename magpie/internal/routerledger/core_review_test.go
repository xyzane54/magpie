package routerledger

import (
	"errors"
	"testing"
)

func TestReviewMissingPriceInputCannotBecomeFree(t *testing.T) {
	s, path := openTest(t, Options{})
	m := metadata("review-price")
	m.ReferencePrice = referencePrice("USD", 1, 2)
	finishStatistics(t, s, m, localOutcome(&Usage{InputTokens: integer(10), OutputTokens: integer(20)}))
	if _, err := s.db.Exec("UPDATE requests SET data=json_remove(data,'$.referencePrice.input')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if reopened != nil {
		defer reopened.Close()
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing historical price input accepted as explicit free: %v", err)
	}
}

func TestReviewPartialExecutionIsNotExtraOverhead(t *testing.T) {
	s, _ := openTest(t, Options{})
	outcome := localOutcome(&Usage{InputTokens: integer(5), OutputTokens: integer(2), TotalTokens: integer(7)})
	outcome.LocalCallID = "one-actual-execution"
	finishStatistics(t, s, metadata("review-execution"), outcome)
	call := statisticsCall("review-execution", "one-actual-execution", "execution", &Usage{InputTokens: integer(5), OutputTokens: integer(2)})
	if err := s.RecordCall(call); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	group := callStatistics(t, summary.InternalCalls, "execution")
	if group.BusinessUsageCalls != 1 || group.OverheadUsageCalls != 0 {
		t.Fatalf("same successful execution duplicated into overhead when final usage has an additional total: %+v", group)
	}
}

func TestReviewFailedPartialLocalUsageIsStillBusinessUsage(t *testing.T) {
	s, _ := openTest(t, Options{})
	outcome := localOutcome(&Usage{InputTokens: integer(5), OutputTokens: integer(2)})
	outcome.LocalCallID, outcome.Status, outcome.SafeErrorCode, outcome.OutputCommitted = "partial-execution", "failed", "router_error", true
	finishStatistics(t, s, metadata("review-partial-failure"), outcome)
	call := statisticsCall("review-partial-failure", outcome.LocalCallID, "execution", &Usage{InputTokens: integer(5), OutputTokens: integer(2)})
	call.Result, call.SafeErrorCode = "failed", "local_generation_error"
	if err := s.RecordCall(call); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	group := callStatistics(t, summary.InternalCalls, "execution")
	if group.BusinessUsageCalls != 1 || group.OverheadUsageCalls != 0 || summary.Savings.EligibleRequests != 0 {
		t.Fatalf("partial-output failed local response duplicated or counted as savings: %+v", summary)
	}
}
