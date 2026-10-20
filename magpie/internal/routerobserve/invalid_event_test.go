package routerobserve

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerruntime"
)

func TestReviewInvalidLocalObservationCannotPoisonBusiness(t *testing.T) {
	for _, field := range []string{"modelId", "callId", "materialVersion"} {
		t.Run(field, func(t *testing.T) {
			s, path := ledger(t)
			watch := begin(t, s, "valid-business")
			calls := NewCalls(s, 32)
			defer calls.Close()
			ctx := WithLocalObserver(context.Background(), calls, "valid-business", false, watch)
			observer := routerruntime.LocalCallObserverFromContext(ctx)
			now := time.Now().UTC()
			event := routerruntime.LocalCallObservation{CallID: "valid-call", Purpose: routerruntime.LocalCallExecution, Mode: routerruntime.LocalCallFormal, ModelID: "local-model", StartedAt: now, EndedAt: now, Outcome: routerruntime.LocalCallSucceeded, Attempted: true, Usage: &cloudcodec.ReportedUsage{InputTokens: number(7), OutputTokens: number(2)}}
			const forbidden = "Authorization: Bearer synthetic-not-a-real-key"
			switch field {
			case "modelId":
				event.ModelID = forbidden
			case "callId":
				event.CallID = forbidden
			case "materialVersion":
				event.MaterialVersion = forbidden
			}
			observer.ObserveLocalCall(event)
			if err := calls.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishErr := watch.Finish(routercloud.Result{Destination: "local"}, nil)
			record := read(t, s, "valid-business")
			if finishErr != nil || record.Status != "succeeded" || record.Destination != "local" || record.Usage != nil || record.LocalModel != "" || record.LocalCallID != "" {
				t.Errorf("invalid %s event poisoned business observation: finish=%v status=%s destination=%s usage=%+v model=%q callId=%q", field, finishErr, record.Status, record.Destination, record.Usage, record.LocalModel, record.LocalCallID)
			}
			if s.Status().ObservationFailures == 0 {
				t.Error("invalid event was not diagnosed")
			}
			stored, err := s.Calls("valid-business")
			if err != nil || len(stored) != 0 {
				t.Errorf("invalid internal call persisted: count=%d err=%v", len(stored), err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(forbidden)) {
				t.Fatal("forbidden observation text entered SQLite")
			}
		})
	}
}
