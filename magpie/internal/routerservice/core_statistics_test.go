package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

// These producers implement the shared completion contract with synthetic
// events. They exercise the real HTTP/ledger/API path, never a real model.
type statisticsJudge struct {
	decision Decision
	err      error
}

func (j statisticsJudge) Judge(ctx context.Context, _ routerrequest.JudgmentView) (Decision, error) {
	now := time.Now().UTC()
	result := routerruntime.LocalCallSucceeded
	if j.err != nil {
		result = routerruntime.LocalCallFailed
	}
	if observer := routerobserve.LocalObserver(ctx); observer != nil {
		observer.ObserveLocalCall(routerruntime.LocalCallObservation{CallID: "fixture-judgment", Purpose: routerruntime.LocalCallJudgment, Mode: routerruntime.LocalCallFormal, ModelID: "actual-local", StartedAt: now, EndedAt: now, Outcome: result, Attempted: true})
	}
	return j.decision, j.err
}

type statisticsInvoker struct{ fail bool }

func (i statisticsInvoker) Invoke(ctx context.Context, _ routerruntime.Purpose, _ []byte, consume func(*http.Response) error) error {
	now := time.Now().UTC()
	input, output := int64(7), int64(0)
	v := routerruntime.LocalCallObservation{CallID: "fixture-execution", Purpose: routerruntime.LocalCallExecution, Mode: routerruntime.LocalCallFormal, ModelID: "actual-local", ModelFingerprint: strings.Repeat("a", 64), RuntimeID: "fixed-runtime", RuntimeFingerprint: strings.Repeat("b", 64), MaterialVersion: "execution-v1", PromptVersion: "prompt-v1", Suite: "suite-v1", StartedAt: now, EndedAt: now, Outcome: routerruntime.LocalCallSucceeded, Attempted: true, Usage: &cloudcodec.ReportedUsage{InputTokens: &input, OutputTokens: &output}}
	var err error
	if i.fail {
		err = errors.New("fake-private-model-failure")
		v.Outcome = routerruntime.LocalCallFailed
	} else {
		err = consume(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"local","choices":[{"index":0,"message":{"role":"assistant","content":"fake-private-local-reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":0}}`))})
	}
	if observer := routerobserve.LocalObserver(ctx); observer != nil {
		observer.ObserveLocalCall(v)
		observer.ObserveLocalCall(v)
	}
	return err
}

func TestCoreStatisticsContractConsumerThroughHTTPAndRealLedger(t *testing.T) {
	for _, scenario := range []string{"local-success", "judgment-fallback", "execution-fallback"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, cloudHTTPReply("chat", false))
			}))
			defer upstream.Close()
			h, ledger, store, _ := ledgerHTTPFixture(t, ModelOptions{})
			cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "fake-provider-key")
			profiles, err := store.Apps()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range profiles {
				if p.ID == "codex" {
					_, err = store.SaveApp(routerconfig.AppEdit{ID: p.ID, Revision: p.Revision, Name: p.Name, Cloud: p.Cloud, CredentialMode: p.CredentialMode, ReferencePrice: &routerconfig.ReferencePrice{Currency: "USD", Unit: "million-tokens", Input: 0.1, Output: 0.2, Version: "old-v1", Source: "manual"}})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := routerobserve.NewCalls(ledger, 32)
			defer calls.Close()
			judge := statisticsJudge{decision: DecisionLocal}
			if scenario == "judgment-fallback" {
				judge.err = errors.New("fake-private-judge-error")
			}
			routing := &RoutingOptions{Judge: judge, LocalInvoker: statisticsInvoker{fail: scenario == "execution-fallback"}}
			h = handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store, ModelOptions{Ledger: ledger, LocalCalls: calls, Routing: routing})
			w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
			if w.Code != 200 {
				t.Fatalf("model %d %s", w.Code, w.Body.String())
			}
			if err := calls.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows := requestRecords(t, h, "")
			if len(rows) != 1 {
				t.Fatalf("business count %d", len(rows))
			}
			r := rows[0]
			if r.ReferencePrice == nil || r.ReferencePrice.Version != "old-v1" {
				t.Fatal("request-time price missing")
			}
			local := scenario == "local-success"
			if local && (r.Destination != "local" || r.Attempts != 0 || r.Usage == nil || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 7 || r.Usage.OutputTokens == nil || *r.Usage.OutputTokens != 0 || r.Usage.TotalTokens != nil || r.LocalModel != "actual-local" || r.Model == r.LocalModel) {
				t.Fatalf("local actual outcome %+v", r)
			}
			if !local && (r.Destination != "cloud" || r.Attempts != 1) {
				t.Fatalf("fallback %+v", r)
			}
			detail := providerRequest(t, h, "GET", "/api/requests/"+r.RequestID, cloudHTTPManagement, nil)
			var d struct {
				Calls []routerledger.Call `json:"calls"`
			}
			if json.Unmarshal(detail.Body.Bytes(), &d) != nil || len(d.Calls) < 1 {
				t.Fatalf("detail calls %d %s", detail.Code, detail.Body.String())
			}
			wantCalls := 2
			if scenario == "judgment-fallback" {
				wantCalls = 1
			}
			if len(d.Calls) != wantCalls {
				t.Fatal("duplicate execution completion counted")
			}
			response := providerRequest(t, h, "GET", "/api/requests/summary", cloudHTTPManagement, nil)
			var s struct {
				Summary routerledger.Summary `json:"summary"`
			}
			if json.Unmarshal(response.Body.Bytes(), &s) != nil || s.Summary.Total != 1 {
				t.Fatalf("summary %d %s", response.Code, response.Body.String())
			}
			wantEligible := 0
			if local {
				wantEligible = 1
			}
			if s.Summary.Savings.EligibleRequests != wantEligible {
				t.Fatal("fallback incorrectly saved tokens")
			}
		})
	}
}

func TestUnsafeLegacyPriceMetadataDoesNotDropBusinessHistory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.TrimSuffix(cloudHTTPReply("chat", false), "}")+`,"usage":{"prompt_tokens":7,"completion_tokens":0}}`)
	}))
	defer upstream.Close()
	h, ledger, store, file := ledgerHTTPFixture(t, ModelOptions{})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "fake-provider-key")
	profiles, err := store.Apps()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.ID == "codex" {
			_, err = store.SaveApp(routerconfig.AppEdit{ID: p.ID, Revision: p.Revision, Name: p.Name, Cloud: p.Cloud, CredentialMode: p.CredentialMode, ReferencePrice: &routerconfig.ReferencePrice{Currency: "USD", Unit: "million-tokens", Input: 1, Output: 2, Version: "v1", Source: "https://fake-private-price.invalid/secret"}})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	rows := requestRecords(t, h, "")
	if w.Code != 200 || len(rows) != 1 || rows[0].ReferencePrice != nil || rows[0].Usage == nil || ledger.Status().ObservationFailures != 1 {
		t.Fatalf("unsafe price dropped business: %d %+v %+v", w.Code, rows, ledger.Status())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fake-private-price") {
		t.Fatal("unsafe source persisted")
	}
}
