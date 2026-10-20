package routerservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routerconfig"
)

// Native Responses takes a separate reply path to preserve Codex's extended
// tools and reasoning. Actual nullable usage must still reach the common ledger.
func TestIntegratedCodexNativeRepliesRetainReportedLedgerUsage(t *testing.T) {
	for _, upstreamStream := range []bool{false, true} {
		for _, clientStream := range []bool{false, true} {
			for _, usage := range []string{"null", `{"input_tokens":7}`, `{"input_tokens":7,"output_tokens":0,"total_tokens":7}`} {
				t.Run(fmt.Sprintf("upstream=%t/client=%t/usage=%s", upstreamStream, clientStream, usage), func(t *testing.T) {
					body := codexFixture(t, "default")
					body["stream"] = clientStream
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var got map[string]any
						if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, body) || r.Header.Get("Authorization") != "Bearer bound-provider-key" {
							t.Error("native captured request or bound authorization changed")
						}
						reply := strings.TrimSuffix(cloudHTTPReply("responses", true), `,"usage":{"input_tokens":10,"output_tokens":3}}`) + `,"usage":` + usage + `}`
						if !upstreamStream {
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, reply)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if err := cloudcodec.ResponsesWholeEventsEach([]byte(reply), func(event cloudcodec.WholeEvent) error {
							data, err := json.Marshal(event.Data)
							if err != nil {
								return err
							}
							_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, data)
							return err
						}); err != nil {
							t.Error(err)
						}
					}))
					defer upstream.Close()
					var completed ModelCompletion
					var commits int
					h, _, _, _ := ledgerHTTPFixture(t, ModelOptions{Complete: func(c ModelCompletion) { completed = c }, Observe: func(e ModelEvent) {
						if e.Event.Kind == "output_committed" {
							commits++
						}
					}})
					cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
					codexHTTPAuthorize(t, h, codexToolCapabilities)
					w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "Bearer placeholder", "")
					if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), "call_new") || commits != 1 || completed.Stage != "completed" || completed.Purpose != "generation" {
						t.Fatalf("native operation: status=%d calls=%d commits=%d completion=%+v", w.Code, calls.Load(), commits, completed)
					}
					rows := requestRecords(t, h, "")
					if len(rows) != 1 {
						t.Fatalf("ledger records: %d", len(rows))
					}
					r := rows[0]
					if r.Status != "succeeded" || r.Attempts != 1 || !r.OutputCommitted || r.RequestID != w.Header().Get("X-Request-ID") {
						t.Fatalf("ledger result: %+v", r)
					}
					if usage == "null" {
						if r.Usage != nil {
							t.Fatal("unknown usage became known")
						}
					} else {
						if r.Usage == nil || r.Usage.InputTokens == nil || *r.Usage.InputTokens != 7 {
							t.Fatalf("input usage lost: %+v", r.Usage)
						}
						if strings.Contains(usage, "output_tokens") {
							if r.Usage.OutputTokens == nil || *r.Usage.OutputTokens != 0 || r.Usage.TotalTokens == nil || *r.Usage.TotalTokens != 7 {
								t.Fatalf("explicit usage lost: %+v", r.Usage)
							}
						} else if r.Usage.OutputTokens != nil || r.Usage.TotalTokens != nil {
							t.Fatal("missing counts fabricated")
						}
					}
				})
			}
		}
	}
}

func TestIntegratedCapabilityAndPermissionFailuresKeepSafeLedgerCodes(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"message":"fake-private-upstream-permission-details"}}`)
	}))
	defer upstream.Close()
	h, _, _, _ := ledgerHTTPFixture(t, ModelOptions{})
	cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
	body := codexFixture(t, "default")
	w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
	rows := requestRecords(t, h, "")
	if w.Code != 400 || calls.Load() != 0 || len(rows) != 1 || rows[0].SafeErrorCode != "capability_namespace" || rows[0].FailureStage != "validation" || rows[0].Attempts != 0 {
		t.Fatalf("capability rejection: status=%d calls=%d rows=%+v", w.Code, calls.Load(), rows)
	}
	codexHTTPAuthorize(t, h, codexToolCapabilities)
	w = cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
	rows = requestRecords(t, h, "")
	if w.Code != 403 || calls.Load() != 1 || len(rows) != 2 || rows[0].SafeErrorCode != "cloud_permission_error" || rows[0].Attempts != 1 {
		t.Fatalf("permission rejection: status=%d calls=%d rows=%+v", w.Code, calls.Load(), rows)
	}
}

func TestIntegratedProfileCheckExcludesBusinessStatisticsWithoutSavingDraft(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("responses", false))
	}))
	defer upstream.Close()
	var completion ModelCompletion
	h, _, store, _ := ledgerHTTPFixture(t, ModelOptions{Complete: func(c ModelCompletion) { completion = c }})
	cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
	apps, err := store.Apps()
	if err != nil {
		t.Fatal(err)
	}
	var draft routerconfig.AppEdit
	for _, app := range apps {
		if app.ID == "codex" {
			draft = routerconfig.AppEdit{ID: app.ID, Revision: app.Revision, Name: "Unsaved editor snapshot", Cloud: app.Cloud, CredentialMode: app.CredentialMode}
		}
	}
	w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
	rows := requestRecords(t, h, "")
	if w.Code != 200 || len(rows) != 0 || w.Header().Get("X-Router-Ledger") != "diagnostic" || completion.Purpose != "connection_check" {
		t.Fatalf("management check entered business statistics: status=%d rows=%+v completion=%+v", w.Code, rows, completion)
	}
	apps, err = store.Apps()
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.ID == "codex" && (app.Revision != draft.Revision || app.Name == draft.Name) {
			t.Fatal("check saved editor draft")
		}
	}
}
