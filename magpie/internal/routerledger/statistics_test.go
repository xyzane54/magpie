package routerledger

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func referencePrice(currency string, input, output float64) *ReferencePrice {
	return &ReferencePrice{Currency: currency, Unit: "million-tokens", Input: input, Output: output, Version: "user-v1", Source: "manual"}
}

func localOutcome(usage *Usage) Outcome {
	return Outcome{ExecutionKnown: true, Status: "succeeded", Destination: "local", LocalModel: "local-17b", Usage: usage}
}

func finishStatistics(t *testing.T, s *Store, m Metadata, o Outcome) {
	t.Helper()
	if err := s.Start(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(m.RequestID, o); err != nil {
		t.Fatal(err)
	}
}

func destinationStatistics(t *testing.T, summary Summary, destination string) DestinationStatistics {
	t.Helper()
	for _, group := range summary.Destinations {
		if group.Destination == destination {
			return group
		}
	}
	t.Fatalf("missing destination %s", destination)
	return DestinationStatistics{}
}

func callStatistics(t *testing.T, groups []CallStatistics, purpose string) CallStatistics {
	t.Helper()
	for _, group := range groups {
		if group.Purpose == purpose {
			return group
		}
	}
	t.Fatalf("missing purpose %s", purpose)
	return CallStatistics{}
}

func tokenValue(t *testing.T, aggregate TokenAggregate, value int64, known, unknown int) {
	t.Helper()
	if aggregate.KnownTokens == nil || *aggregate.KnownTokens != value || aggregate.KnownRequests != known || aggregate.UnknownRequests != unknown {
		t.Fatalf("token coverage: %+v, want %d (%d known / %d unknown)", aggregate, value, known, unknown)
	}
}

func TestCoreStatisticsDestinationsUnknownAndSavingsEligibility(t *testing.T) {
	s, _ := openTest(t, Options{})
	cases := []struct {
		id      string
		outcome Outcome
	}{
		{"local-known", localOutcome(&Usage{InputTokens: integer(7), OutputTokens: integer(3), TotalTokens: integer(10)})},
		{"local-partial", localOutcome(&Usage{InputTokens: integer(2)})},
		{"local-zero", localOutcome(&Usage{InputTokens: integer(0), OutputTokens: integer(0), TotalTokens: integer(0)})},
		{"local-failed", Outcome{Status: "failed", Destination: "local", Usage: &Usage{InputTokens: integer(9), OutputTokens: integer(4), TotalTokens: integer(13)}, SafeErrorCode: "router_error"}},
		{"local-canceled", Outcome{Status: "canceled", Destination: "local", SafeErrorCode: "request_canceled"}},
		{"cloud-retried", Outcome{Status: "succeeded", Destination: "cloud", Attempts: 2, Usage: &Usage{InputTokens: integer(11), OutputTokens: integer(5), TotalTokens: integer(16)}}},
		{"no-dispatch", Outcome{Status: "failed", Destination: "none", SafeErrorCode: "profile_unconfigured"}},
		{"unknown", Outcome{Status: "interrupted", SafeErrorCode: "request_interrupted"}},
	}
	for _, tc := range cases {
		m := metadata(tc.id)
		m.ReferencePrice = referencePrice("USD", .1, .2)
		finishStatistics(t, s, m, tc.outcome)
	}
	if err := s.Start(metadata("processing")); err != nil {
		t.Fatal(err)
	}
	aux := metadata("auxiliary")
	aux.Operation, aux.Path = "count_tokens", "/v1/messages/count_tokens"
	finishStatistics(t, s, aux, localOutcome(&Usage{InputTokens: integer(900), OutputTokens: integer(900)}))
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 9 || summary.Attempts != 2 || len(summary.Destinations) != 4 {
		t.Fatalf("business denominator changed: %+v", summary)
	}
	local := destinationStatistics(t, summary, "local")
	if local.Total != 5 || local.Share == nil || *local.Share != 5.0/9 || local.Succeeded != 3 || local.Failed != 1 || local.Canceled != 1 {
		t.Fatalf("local statuses or denominator: %+v", local)
	}
	tokenValue(t, local.InputTokens, 18, 4, 1)
	tokenValue(t, local.OutputTokens, 7, 3, 2)
	cloud := destinationStatistics(t, summary, "cloud")
	if cloud.Total != 1 || cloud.Succeeded != 1 {
		t.Fatalf("retries counted as business requests: %+v", cloud)
	}
	tokenValue(t, cloud.TotalTokens, 16, 1, 0)
	unknown := destinationStatistics(t, summary, "unknown")
	if unknown.Total != 1 || unknown.InputTokens.KnownTokens != nil || unknown.InputTokens.UnknownRequests != 1 {
		t.Fatalf("unknown became zero: %+v", unknown)
	}
	if summary.Savings.EligibleRequests != 3 {
		t.Fatalf("non-success/auxiliary counted as savings: %+v", summary.Savings)
	}
	tokenValue(t, summary.Savings.AvoidedTokens, 10, 2, 1)
	if len(summary.Savings.Money) != 1 {
		t.Fatalf("price groups: %+v", summary.Savings)
	}
	money := summary.Savings.Money[0]
	if money.Currency != "USD" || money.KnownAmount == nil || *money.KnownAmount != "0.0000013" || money.KnownRequests != 2 || money.UnknownRequests != 1 {
		t.Fatalf("partial usage priced as zero: %+v", money)
	}
	filtered, err := s.Summary(Query{Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	local = destinationStatistics(t, filtered, "local")
	if filtered.Total != 4 || local.Share == nil || *local.Share != .75 {
		t.Fatalf("filter denominator ignored: %+v", filtered)
	}
	auxiliary, err := s.Summary(Query{Operation: "count_tokens"})
	if err != nil || auxiliary.Total != 1 || auxiliary.Savings.EligibleRequests != 0 {
		t.Fatalf("auxiliary savings invented: %+v %v", auxiliary, err)
	}
}

func TestCoreStatisticsPriceSnapshotsCurrenciesAndRestart(t *testing.T) {
	s, path := openTest(t, Options{})
	m := metadata("old-price")
	m.ReferencePrice = referencePrice("USD", .1, .2)
	if err := s.Start(m); err != nil {
		t.Fatal(err)
	}
	// Mutating the editor's price after admission cannot reprice this request.
	m.ReferencePrice.Input, m.ReferencePrice.Output, m.ReferencePrice.Version = .9, 1.8, "user-v2"
	if err := s.Finish(m.RequestID, localOutcome(&Usage{InputTokens: integer(1), OutputTokens: integer(2)})); err != nil {
		t.Fatal(err)
	}
	m.RequestID = "new-price"
	finishStatistics(t, s, m, localOutcome(&Usage{InputTokens: integer(1), OutputTokens: integer(2)}))
	cny := metadata("cny-price")
	cny.ReferencePrice = referencePrice("CNY", 1.25, 2.5)
	finishStatistics(t, s, cny, localOutcome(&Usage{InputTokens: integer(3), OutputTokens: integer(4)}))
	finishStatistics(t, s, metadata("missing-price"), localOutcome(&Usage{InputTokens: integer(6), OutputTokens: integer(7)}))
	unknown := metadata("missing-usage")
	unknown.ReferencePrice = referencePrice("EUR", 1, 1)
	finishStatistics(t, s, unknown, localOutcome(nil))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	old := get(t, reopened, "old-price")
	if old.ReferencePrice == nil || old.ReferencePrice.Input != .1 || old.ReferencePrice.Version != "user-v1" {
		t.Fatalf("historical price overwritten: %+v", old.ReferencePrice)
	}
	summary, err := reopened.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Savings.UnknownPriceRequests != 1 || len(summary.Savings.Money) != 3 {
		t.Fatalf("missing prices/currencies merged: %+v", summary.Savings)
	}
	groups := summary.Savings.Money
	if groups[0].Currency != "CNY" || groups[0].KnownAmount == nil || *groups[0].KnownAmount != "0.00001375" || groups[1].Currency != "EUR" || groups[1].KnownAmount != nil || groups[1].UnknownRequests != 1 || groups[2].Currency != "USD" || groups[2].KnownAmount == nil || *groups[2].KnownAmount != "0.000005" {
		t.Fatalf("price precision/snapshot: %+v", groups)
	}
}

func TestCoreStatisticsExactLargeAmountAndEmptyUnknown(t *testing.T) {
	s, _ := openTest(t, Options{})
	empty, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range empty.Destinations {
		if group.Share != nil || group.InputTokens.KnownTokens != nil {
			t.Fatalf("empty unknown became zero: %+v", group)
		}
	}
	m := metadata("large-exact")
	m.ReferencePrice = referencePrice("USD", .1, .2)
	finishStatistics(t, s, m, localOutcome(&Usage{InputTokens: integer(math.MaxInt64), OutputTokens: integer(0)}))
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if amount := summary.Savings.Money[0].KnownAmount; amount == nil || *amount != "922337203685.4775807" {
		t.Fatalf("amount lost decimal precision: %v", amount)
	}
	encoded, _ := json.Marshal(empty)
	if !strings.Contains(string(encoded), `"knownTokens":null`) || !strings.Contains(string(encoded), `"share":null`) {
		t.Fatalf("null contract: %s", encoded)
	}
}

func statisticsCall(request, id, purpose string, usage *Usage) Call {
	now := time.Now().UTC()
	return Call{RequestID: request, CallID: id, Scope: "business", Purpose: purpose, ModelID: "local-17b", Fingerprint: "sha256.1234", MaterialVersion: "suite-1", Mode: "formal", StartedAt: now.Add(-time.Millisecond), EndedAt: now, Result: "succeeded", InferenceStarted: true, Usage: usage, Timings: CallTimings{TotalMS: integer(1)}}
}

func TestCoreStatisticsInternalCallsDiagnosticsAndBusinessUsageSeparation(t *testing.T) {
	s, _ := openTest(t, Options{})
	localUsage := &Usage{InputTokens: integer(5), OutputTokens: integer(2), TotalTokens: integer(7)}
	finishStatistics(t, s, metadata("local"), localOutcome(localUsage))
	finishStatistics(t, s, metadata("fallback"), Outcome{Status: "succeeded", Destination: "cloud", Attempts: 2, Usage: &Usage{InputTokens: integer(9), OutputTokens: integer(1), TotalTokens: integer(10)}})
	localExecution := statisticsCall("local", "execute-local", "execution", localUsage)
	failedExecution := statisticsCall("fallback", "execute-failed", "execution", &Usage{InputTokens: integer(3)})
	failedExecution.Result, failedExecution.SafeErrorCode = "failed", "local_generation_error"
	judgment := statisticsCall("fallback", "judge", "judgment", &Usage{InputTokens: integer(1), OutputTokens: integer(0), TotalTokens: integer(1)})
	unknownJudgment := statisticsCall("local", "judge-unknown", "judgment", nil)
	rejected := statisticsCall("fallback", "redaction-rejected", "redaction", nil)
	rejected.InferenceStarted, rejected.Result, rejected.SafeErrorCode = false, "rejected", "local_model_queue_full"
	rejected.Timings = CallTimings{QueueLoadMS: integer(12), TotalMS: integer(12)}
	diagnostic := statisticsCall("", "diagnostic", "execution", &Usage{InputTokens: integer(20), OutputTokens: integer(10), TotalTokens: integer(30)})
	diagnostic.Scope = "check"
	for _, call := range []Call{localExecution, failedExecution, judgment, unknownJudgment, rejected, diagnostic, localExecution} {
		if err := s.RecordCall(call); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Attempts != 2 || summary.InputTokens.KnownTokens != 14 || summary.Savings.EligibleRequests != 1 {
		t.Fatalf("internal/diagnostic/retry inflated business: %+v", summary)
	}
	execution := callStatistics(t, summary.InternalCalls, "execution")
	if execution.Total != 2 || execution.InferenceStarted != 2 || execution.BusinessUsageCalls != 1 || execution.OverheadUsageCalls != 1 || execution.Results["failed"] != 1 {
		t.Fatalf("execution counted twice: %+v", execution)
	}
	tokenValue(t, execution.InputTokens, 8, 2, 0)
	tokenValue(t, execution.OverheadInputTokens, 3, 1, 0)
	if execution.OverheadOutputTokens.KnownTokens != nil || execution.OverheadOutputTokens.UnknownRequests != 1 {
		t.Fatalf("failed actual usage fabricated: %+v", execution)
	}
	judgments := callStatistics(t, summary.InternalCalls, "judgment")
	tokenValue(t, judgments.InputTokens, 1, 1, 1)
	redaction := callStatistics(t, summary.InternalCalls, "redaction")
	if redaction.NotStarted != 1 || redaction.InferenceStarted != 0 || redaction.InputTokens.KnownRequests != 0 || redaction.InputTokens.UnknownRequests != 0 || redaction.Timings.QueueLoadMS.KnownMS == nil || *redaction.Timings.QueueLoadMS.KnownMS != 12 || redaction.Timings.QueueMS.KnownMS != nil || redaction.Timings.LoadMS.KnownMS != nil {
		t.Fatalf("admission rejection became inference or fabricated split: %+v", redaction)
	}
	checks := callStatistics(t, summary.CheckCalls, "execution")
	if checks.Total != 1 {
		t.Fatalf("diagnostics not separate: %+v", checks)
	}
	tokenValue(t, checks.InputTokens, 20, 1, 0)
	filtered, err := s.Summary(Query{AppID: "codex", Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if callStatistics(t, filtered.CheckCalls, "execution").Total != 0 {
		t.Fatal("unassociated diagnostic attributed to app filter")
	}
}

func TestCoreStatisticsWindowUsesBusinessStartAndHalfOpenRange(t *testing.T) {
	s, _ := openTest(t, Options{})
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	for i, id := range []string{"before", "included", "at-end"} {
		m := metadata(id)
		m.AcceptedAt = base.Add(time.Duration(i) * time.Second)
		finishStatistics(t, s, m, localOutcome(&Usage{InputTokens: integer(1), OutputTokens: integer(1)}))
		call := statisticsCall(id, "call-"+id, "judgment", &Usage{InputTokens: integer(2)})
		if err := s.RecordCall(call); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := s.Summary(Query{From: base.Add(time.Second), To: base.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.OldestStartedAt == nil || !summary.OldestStartedAt.Equal(base.Add(time.Second)) || callStatistics(t, summary.InternalCalls, "judgment").Total != 1 {
		t.Fatalf("calls filtered on completion instead of parent request: %+v", summary)
	}
}

func TestCoreStatisticsOverflowFailsInsteadOfInventingEstimate(t *testing.T) {
	for _, field := range []string{"avoided", "internal-time", "internal-usage"} {
		t.Run(field, func(t *testing.T) {
			s, _ := openTest(t, Options{})
			usage := &Usage{InputTokens: integer(1), OutputTokens: integer(1)}
			if field == "avoided" {
				usage = &Usage{InputTokens: integer(math.MaxInt64), OutputTokens: integer(1)}
			}
			finishStatistics(t, s, metadata("local"), localOutcome(usage))
			if field != "avoided" {
				for _, id := range []string{"a", "b"} {
					call := statisticsCall("local", id, "judgment", nil)
					if field == "internal-time" {
						call.Timings.TotalMS = integer(math.MaxInt64)
					} else {
						call.Usage = &Usage{InputTokens: integer(math.MaxInt64)}
					}
					if err := s.RecordCall(call); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.Summary(Query{}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("overflow silently represented: %v", err)
			}
			if s.Status().Available {
				t.Fatal("overflow was not diagnosed")
			}
		})
	}
}
