package routerservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerrequest"
)

func ledgerHTTPFixture(t *testing.T, options ModelOptions) (http.Handler, *routerledger.Store, *routerconfig.Store, string) {
	t.Helper()
	layout, _ := fixture(t)
	file := filepath.Join(layout.State, "requests.sqlite")
	ledger, err := routerledger.Open(file, routerledger.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	options.Ledger = ledger
	store := routerconfig.New(layout)
	return handlerWithModelOptions(Info{Address: "127.0.0.1:13145"}, cloudHTTPManagement, store, options), ledger, store, file
}

func requestRecords(t *testing.T, h http.Handler, query string) []routerledger.Record {
	t.Helper()
	w := providerRequest(t, h, "GET", "/api/requests"+query, cloudHTTPManagement, nil)
	if w.Code != 200 {
		t.Fatalf("query: %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Requests []routerledger.Record `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page.Requests
}

func TestRequestLedgerProductEntryRetrySnapshotsAndSecrets(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fake-private-provider-key" {
			t.Error("provider key missing")
		}
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			io.WriteString(w, "fake-private-upstream-error https://private.invalid/?secret=hidden")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"reply","choices":[{"index":0,"message":{"role":"assistant","content":"fake-private-output"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":0}}`)
	}))
	defer upstream.Close()
	h, ledger, store, file := ledgerHTTPFixture(t, ModelOptions{})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "fake-private-provider-key")
	input := cloudHTTPInput("chat", false, false)
	input["messages"] = []map[string]string{{"role": "user", "content": "fake-private-prompt"}}
	w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", input, "Bearer fake-private-placeholder", "")
	if w.Code != 200 || calls.Load() != 2 {
		t.Fatalf("model: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
	}
	id := w.Header().Get("X-Request-ID")
	rows := requestRecords(t, h, "")
	if len(rows) != 1 {
		t.Fatalf("business requests: %d", len(rows))
	}
	record := rows[0]
	if record.RequestID != id || record.Status != "succeeded" || record.Attempts != 2 || len(record.AttemptDetails) != 2 || record.EndedAt == nil || !record.OutputCommitted || record.Destination != "cloud" {
		t.Fatalf("record: %+v", record)
	}
	if record.Usage == nil || record.Usage.InputTokens == nil || *record.Usage.InputTokens != 7 || record.Usage.OutputTokens == nil || *record.Usage.OutputTokens != 0 || record.Usage.TotalTokens != nil {
		t.Fatalf("reported usage: %+v", record.Usage)
	}
	apps, err := store.Apps()
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.ID != "codex" {
			continue
		}
		_, err = store.SaveApp(routerconfig.AppEdit{ID: app.ID, Revision: app.Revision, Name: "Renamed", Cloud: routerconfig.AppCloud{ProviderID: app.Cloud.ProviderID, Model: "new-model"}, CredentialMode: app.CredentialMode})
		if err != nil {
			t.Fatal(err)
		}
	}
	historical, err := ledger.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if historical.Model != "profile-pinned" || historical.AppName != "codex" {
		t.Fatalf("history changed: %+v", historical)
	}
	detail := providerRequest(t, h, "GET", "/api/requests/"+id, cloudHTTPManagement, nil)
	if detail.Code != 200 {
		t.Fatal(detail.Code)
	}
	for _, secret := range []string{cloudHTTPManagement, "fake-private-provider-key", "fake-private-placeholder", "fake-private-prompt", "fake-private-output", "fake-private-upstream-error", "private.invalid"} {
		if strings.Contains(detail.Body.String(), secret) {
			t.Fatalf("secret in API: %s", secret)
		}
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{cloudHTTPManagement, "fake-private-provider-key", "fake-private-placeholder", "fake-private-prompt", "fake-private-output", "fake-private-upstream-error", "private.invalid"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret persisted: %s", secret)
		}
	}
	reopened, err := routerledger.Open(file, routerledger.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(id); err != nil {
		t.Fatalf("restart lost history: %v", err)
	}
}

func TestRequestLedgerManagementBoundaryAndQueryLimits(t *testing.T) {
	h, ledger, _, _ := ledgerHTTPFixture(t, ModelOptions{})
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("%032x", i+1)
		if err := ledger.Start(routerledger.Metadata{RequestID: id, AppID: "codex", AppName: "Codex", Operation: "generate", Path: "/v1/responses", Model: "m"}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Finish(id, routerledger.Outcome{Status: "failed", Destination: "none", SafeErrorCode: "unsupported_input", FailureStage: "preflight"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/requests", "/api/requests/summary", "/api/requests/00000000000000000000000000000001"} {
		for _, token := range []string{"", "client-placeholder", "fake-provider-key"} {
			if w := providerRequest(t, h, "GET", path, token, nil); w.Code != 401 {
				t.Fatalf("auth %s: %d", path, w.Code)
			}
		}
		for _, change := range []func(*http.Request){
			func(r *http.Request) { r.RemoteAddr = "192.0.2.1:10" }, func(r *http.Request) { r.Host = "evil.invalid" }, func(r *http.Request) { r.Header.Set("Origin", "null") }, func(r *http.Request) { r.Header.Set("Origin", "https://evil.invalid") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		} {
			r := httptest.NewRequest("GET", "http://127.0.0.1:13145"+path, nil)
			r.RemoteAddr = "127.0.0.1:50"
			r.Header.Set("Authorization", "Bearer "+cloudHTTPManagement)
			change(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("local guard %s: %d", path, w.Code)
			}
		}
	}
	for _, query := range []string{"?limit=101", "?limit=0", "?limit=-1", "?limit=1&limit=2", "?before=malformed", "?status=fake", "?operation=models", "?appId=../../secret", "?unknown=1", "?from=no", "?from=2026-10-16T00:00:00Z&to=2026-10-15T00:00:00Z", "?appId=%zz", "?appId=" + strings.Repeat("x", 300)} {
		w := providerRequest(t, h, "GET", "/api/requests"+query, cloudHTTPManagement, nil)
		if w.Code != 400 {
			t.Fatalf("invalid query %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	first := providerRequest(t, h, "GET", "/api/requests?limit=2", cloudHTTPManagement, nil)
	var page struct {
		Requests   []routerledger.Record `json:"requests"`
		NextCursor string                `json:"nextCursor"`
	}
	if json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Requests) != 2 || page.NextCursor == "" {
		t.Fatal(first.Body.String())
	}
	second := requestRecords(t, h, "?limit=2&before="+page.NextCursor)
	if len(second) != 2 || second[0].RequestID == page.Requests[0].RequestID {
		t.Fatal("cursor duplicates")
	}
	if rows := requestRecords(t, h, "?appId=codex&status=failed&operation=generate"); len(rows) != 4 {
		t.Fatal(len(rows))
	}
	if w := providerRequest(t, h, "GET", "/api/requests/ffffffffffffffffffffffffffffffff", cloudHTTPManagement, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := providerRequest(t, h, "POST", "/api/requests", cloudHTTPManagement, nil); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := providerRequest(t, h, "HEAD", "/api/requests", cloudHTTPManagement, nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if w := providerRequest(t, h, "GET", "/api/requests", cloudHTTPManagement, nil); w.Code != 503 || !strings.Contains(w.Body.String(), "ledger_unavailable") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRequestLedgerAdmissionAndAuxiliaryAccounting(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages/count_tokens" {
			io.WriteString(w, `{"input_tokens":12}`)
			return
		}
		io.WriteString(w, cloudHTTPReply("anthropic", false))
	}))
	defer upstream.Close()
	h, _, _, _ := ledgerHTTPFixture(t, ModelOptions{})
	// Registered but unconfigured is an admitted preflight failure with 0 attempts.
	cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	rows := requestRecords(t, h, "")
	if len(rows) != 1 || rows[0].Attempts != 0 || rows[0].Status != "failed" || rows[0].SafeErrorCode != "profile_unconfigured" {
		t.Fatalf("preflight: %+v", rows)
	}
	cloudHTTPBind(t, h, upstream.URL, "anthropic", "codex", "fake-key")
	for _, path := range []string{"/healthz", "/api/apps", "/app/codex/v1/models"} {
		providerRequest(t, h, "GET", path, cloudHTTPManagement, nil)
	}
	cloudHTTPCall(t, h, context.Background(), "unknown-app", "chat", cloudHTTPInput("chat", false, false), "", "")
	if len(requestRecords(t, h, "")) != 1 {
		t.Fatal("nonbusiness request admitted")
	}
	countInput := cloudHTTPInput("anthropic", false, false)
	delete(countInput, "max_tokens")
	delete(countInput, "stream")
	count := providerRequest(t, h, "POST", "/app/codex/v1/messages/count_tokens", "", countInput)
	if count.Code != 200 {
		t.Fatal(count.Code, count.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := cloudHTTPCall(t, h, context.Background(), "codex", "anthropic", cloudHTTPInput("anthropic", false, true), "", ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	rows = requestRecords(t, h, "")
	if len(rows) != 4 || calls.Load() != 3 {
		t.Fatalf("external turns: rows=%d calls=%d", len(rows), calls.Load())
	}
	summary := providerRequest(t, h, "GET", "/api/requests/summary", cloudHTTPManagement, nil)
	var out struct {
		Summary routerledger.Summary `json:"summary"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary.Total != 3 || out.Summary.Succeeded != 2 || out.Summary.Failed != 1 || out.Summary.InputTokens.UnknownRequests != 3 {
		t.Fatalf("generation accounting: %s", summary.Body.String())
	}
	for _, record := range rows {
		if record.Operation == "count_tokens" && record.Usage != nil {
			t.Fatal("count usage mixed into generation")
		}
	}
}

func TestRequestLedgerServiceRestartAndCorruptDegradation(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			layout, address := fixture(t)
			file := filepath.Join(layout.State, "requests.sqlite")
			if corrupt {
				if err := os.WriteFile(file, []byte("not a database: fake-private-storage-data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			get := func(path string) (int, []byte) {
				secret, err := os.ReadFile(TokenFile(layout))
				if err != nil {
					t.Fatal(err)
				}
				r, _ := http.NewRequest("GET", "http://"+address+path, nil)
				r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				b, _ := io.ReadAll(response.Body)
				return response.StatusCode, b
			}
			_, cancel, done := start(t, layout)
			if corrupt {
				code, b := get("/api/requests")
				if code != 503 || !strings.Contains(string(b), "ledger_corrupt") || strings.Contains(string(b), "fake-private-storage-data") {
					t.Fatalf("corrupt: %d %s", code, b)
				}
				if code, _ := get("/healthz"); code != 200 {
					t.Fatal("corrupt ledger killed service")
				}
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(file)
				if err != nil || string(b) != "not a database: fake-private-storage-data" {
					t.Fatal("corrupt history overwritten")
				}
				return
			}
			r, _ := http.NewRequest("POST", "http://"+address+"/app/codex/v1/responses", strings.NewReader(`{"input":"hello"}`))
			r.Header.Set("Content-Type", "application/json")
			response, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			id := response.Header.Get("X-Request-ID")
			if !validRequestID(id) {
				t.Fatal("missing Router ID")
			}
			if code, b := get("/api/requests/" + id); code != 200 || !strings.Contains(string(b), "profile_unconfigured") {
				t.Fatalf("live record: %d %s", code, b)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			_, cancel2, done2 := start(t, layout)
			if code, b := get("/api/requests/" + id); code != 200 || !strings.Contains(string(b), "profile_unconfigured") {
				t.Fatalf("restart record: %d %s", code, b)
			}
			cancel2()
			if err := <-done2; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRequestLedgerFailureDoesNotAppendModelResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	h, ledger, _, _ := ledgerHTTPFixture(t, ModelOptions{})
	cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "fake-key")
	ledger.Close()
	w := cloudHTTPCall(t, h, context.Background(), "codex", "chat", cloudHTTPInput("chat", false, false), "", "")
	if w.Code != 200 || w.Header().Get("X-Router-Ledger") != "unavailable" || strings.Contains(w.Body.String(), "ledger") {
		t.Fatalf("ledger damaged model response: %d %s", w.Code, w.Body.String())
	}
	var body any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("second response appended")
	}
}

func TestPreparationFailureKeepsAdmissionSnapshot(t *testing.T) {
	h, ledger, store, _ := ledgerHTTPFixture(t, ModelOptions{})
	cloudHTTPBind(t, h, "http://127.0.0.1:1", "chat", "codex", "fake-key")
	snapshot := preparationSnapshot(context.Background(), store, "codex")
	if snapshot == nil {
		t.Fatal("missing admission snapshot")
	}
	_, err := store.SaveApp(routerconfig.AppEdit{ID: snapshot.ID, Revision: snapshot.Revision, Name: "later-name", Cloud: routerconfig.AppCloud{ProviderID: snapshot.Cloud.ProviderID, Model: "later-model"}, CredentialMode: snapshot.CredentialMode})
	if err != nil {
		t.Fatal(err)
	}
	r, err := routerrequest.WithRequestID(httptest.NewRequest("POST", "http://127.0.0.1:13145/app/codex/v1/responses", nil))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	observePreparationFailure(w, r, snapshot, "responses", time.Now(), ModelOptions{Ledger: ledger}, routerconfig.ErrMissingCredential)
	record, err := ledger.Get(routerrequest.RequestID(r.Context()))
	if err != nil {
		t.Fatal(err)
	}
	if record.AppName != "codex" || record.Model != "profile-pinned" || record.Attempts != 0 || record.SafeErrorCode != "credential_missing" {
		t.Fatalf("preparation reread edited Profile: %+v", record)
	}
}
