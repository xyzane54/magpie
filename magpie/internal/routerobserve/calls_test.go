package routerobserve

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerledger"
)

type heldCalls struct {
	entered, release chan struct{}
	once             sync.Once
	failures         atomic.Int64
}

func (s *heldCalls) RecordCall(routerledger.Call) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}
func (s *heldCalls) ReportObservationFailure() { s.failures.Add(1) }

func TestCallCallbackNeverWaitsForPersistenceAndOverflowIsVisible(t *testing.T) {
	sink := &heldCalls{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewCalls(sink, 1)
	c.Emit(routerledger.Call{CallID: "first"})
	<-sink.entered
	// A held database worker cannot stop either the next callback or overflow.
	c.Emit(routerledger.Call{CallID: "second"})
	c.Emit(routerledger.Call{CallID: "overflow"})
	if sink.failures.Load() != 1 {
		t.Fatal("overflow silently lost observation")
	}
	close(sink.release)
	c.Close()
}

func TestLocalActualUsageIsRecordedWithoutCloudAttempts(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		input, output, total *int64
	}{
		{"unknown", nil, nil, nil}, {"partial", number(7), nil, nil}, {"zero", number(0), number(0), number(0)}, {"known", number(7), number(2), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := ledger(t)
			watch := begin(t, s, "local-actual")
			result := routercloud.Result{Destination: "local", Operation: "generate"}
			if tc.name != "unknown" {
				result.ReportedUsage = &cloudcodec.ReportedUsage{InputTokens: tc.input, OutputTokens: tc.output, TotalTokens: tc.total}
			}
			if err := watch.Finish(result, nil); err != nil {
				t.Fatal(err)
			}
			r := read(t, s, "local-actual")
			if r.Destination != "local" || r.Attempts != 0 {
				t.Fatalf("local became cloud: %+v", r)
			}
			if tc.name == "unknown" {
				if r.Usage != nil {
					t.Fatal("unknown became zero")
				}
				return
			}
			if r.Usage == nil {
				t.Fatal("local actual usage missing without a cloud attempt")
			}
			for _, p := range [][2]*int64{{r.Usage.InputTokens, tc.input}, {r.Usage.OutputTokens, tc.output}, {r.Usage.TotalTokens, tc.total}} {
				if (p[0] == nil) != (p[1] == nil) || p[0] != nil && *p[0] != *p[1] {
					t.Fatal("local usage presence changed")
				}
			}
		})
	}
}

func TestLateDuplicateCallsRemainOneInternalCall(t *testing.T) {
	s, _ := ledger(t)
	watch := begin(t, s, "late-business")
	if err := watch.Finish(routercloud.Result{Destination: "local"}, nil); err != nil {
		t.Fatal(err)
	}
	c := NewCalls(s, 256)
	defer c.Close()
	now := time.Now().UTC()
	v := routerledger.Call{RequestID: "late-business", CallID: "late-call", Scope: "business", Purpose: "execution", ModelID: "local-model", Mode: "formal", StartedAt: now, EndedAt: now, InferenceStarted: true, Result: "succeeded", Usage: &routerledger.Usage{InputTokens: number(3)}}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Emit(v) }()
	}
	wg.Wait()
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls, err := s.Calls("late-business")
	if err != nil || len(calls) != 1 {
		t.Fatalf("duplicate delayed calls: %+v %v", calls, err)
	}
	summary, err := s.Summary(routerledger.Query{})
	if err != nil || summary.Total != 1 {
		t.Fatalf("internal calls increased business requests: %+v %v", summary, err)
	}
}
