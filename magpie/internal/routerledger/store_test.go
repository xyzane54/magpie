package routerledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func openTest(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.sqlite")
	s, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func metadata(id string) Metadata {
	return Metadata{RequestID: id, AppID: "codex", AppName: "Codex 原始名称", Operation: "generate", Path: "/v1/responses", ProviderID: "provider-a", Model: "model-at-request-time"}
}

func integer(n int64) *int64 { return &n }

func success() Outcome {
	return Outcome{Status: "succeeded", Destination: "cloud", Attempts: 1, OutputCommitted: true, Usage: &Usage{InputTokens: integer(13), OutputTokens: integer(0)}}
}

func TestRequestLatencyIncludesPreparation(t *testing.T) {
	s, _ := openTest(t, Options{})
	m := metadata("accepted-before-preparation")
	m.AcceptedAt = time.Now().Add(-5 * time.Second).UTC()
	if err := s.Start(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(m.RequestID, success()); err != nil {
		t.Fatal(err)
	}
	r := get(t, s, m.RequestID)
	if !r.StartedAt.Equal(m.AcceptedAt) || r.DurationMS < 5000 {
		t.Fatalf("preparation missing from latency: %+v", r)
	}
	m.RequestID = "invalid-future"
	m.AcceptedAt = time.Now().Add(24 * time.Hour)
	if err := s.Start(m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future acceptance: %v", err)
	}
}

func get(t *testing.T, s *Store, id string) Record {
	t.Helper()
	r, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDurableStartFinishSnapshotsAndUsagePresence(t *testing.T) {
	s, path := openTest(t, Options{})
	m := metadata("request-one")
	if err := s.Start(m); err != nil {
		t.Fatal(err)
	}
	r := get(t, s, m.RequestID)
	if r.Status != "processing" || r.EndedAt != nil || r.Attempts != 0 || r.Usage != nil || r.SchemaVersion != SchemaVersion {
		t.Fatalf("not a durable active request: %+v", r)
	}
	m.AppName = "Renamed"
	m.Model = "another-model"
	if err := s.Finish(m.RequestID, success()); err != nil {
		t.Fatal(err)
	}
	r = get(t, s, m.RequestID)
	if r.AppName == m.AppName || r.Model == m.Model || r.EndedAt == nil || r.EndedAt.Before(r.StartedAt) || r.Status != "succeeded" || r.DurationMS < 0 || r.Usage == nil || *r.Usage.InputTokens != 13 || *r.Usage.OutputTokens != 0 || r.Usage.TotalTokens != nil {
		t.Fatalf("snapshot or presence lost: %+v", r)
	}
	b, _ := json.Marshal(r)
	if !bytes.Contains(b, []byte(`"outputTokens":0`)) || !bytes.Contains(b, []byte(`"totalTokens":null`)) {
		t.Fatalf("unknown became zero: %s", b)
	}
	if err := s.Finish(m.RequestID, Outcome{Status: "failed", SafeErrorCode: "router_error"}); err != nil {
		t.Fatal(err)
	}
	if get(t, s, m.RequestID).Status != "succeeded" {
		t.Fatal("duplicate Finish changed terminal outcome")
	}
	if err := s.Start(m); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Start=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if get(t, reopened, m.RequestID).Model != "model-at-request-time" {
		t.Fatal("history changed across restart")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode=%o", info.Mode().Perm())
		}
	}
}

func TestRestartMarksOnlyUnfinishedRequestsInterrupted(t *testing.T) {
	s, path := openTest(t, Options{})
	for _, id := range []string{"finished", "interrupted"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish("finished", success()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := get(t, s, "interrupted")
	if r.Status != "interrupted" || r.EndedAt == nil || r.SafeErrorCode != "request_interrupted" || r.FailureStage != "interrupted" || r.Usage != nil || r.Attempts != 0 {
		t.Fatalf("interrupted request misrepresented: %+v", r)
	}
	if get(t, s, "finished").Status != "succeeded" {
		t.Fatal("finished request changed on restart")
	}
}

func TestSummarySeparatesOperationsAndUnknownFields(t *testing.T) {
	s, _ := openTest(t, Options{})
	cases := []struct {
		id, operation string
		outcome       Outcome
	}{
		{"known", "generate", success()},
		{"missing", "generate", Outcome{Status: "failed", Destination: "cloud", Attempts: 2, SafeErrorCode: "cloud_upstream_error", FailureStage: "cloud", AttemptDetails: []Attempt{{Number: 1, StartedAt: time.Now(), HTTPStatus: 503, ErrorCode: "cloud_upstream_error", Retried: true}, {Number: 2, StartedAt: time.Now(), HTTPStatus: 503, ErrorCode: "cloud_upstream_error"}}}},
		{"partial", "generate", Outcome{Status: "canceled", Destination: "cloud", Attempts: 1, OutputCommitted: true, SafeErrorCode: "request_canceled", Usage: &Usage{OutputTokens: integer(7)}}},
		{"auxiliary", "count_tokens", Outcome{Status: "succeeded", Destination: "cloud", Attempts: 1, Usage: &Usage{InputTokens: integer(100)}}},
		{"preflight", "generate", Outcome{Status: "failed", Destination: "none", SafeErrorCode: "profile_unconfigured", FailureStage: "preflight"}},
		{"timeout", "generate", Outcome{Status: "timed_out", SafeErrorCode: "cloud_timeout"}},
	}
	for _, tc := range cases {
		m := metadata(tc.id)
		m.Operation = tc.operation
		if tc.operation == "count_tokens" {
			m.Path = "/v1/messages/count_tokens"
		}
		if err := s.Start(m); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(tc.id, tc.outcome); err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
	}
	if err := s.Start(metadata("active")); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 6 || summary.Succeeded != 1 || summary.Failed != 2 || summary.Canceled != 1 || summary.TimedOut != 1 || summary.Processing != 1 || summary.Attempts != 4 || summary.InputTokens.KnownTokens != 13 || summary.InputTokens.KnownRequests != 1 || summary.InputTokens.UnknownRequests != 5 || summary.OutputTokens.KnownTokens != 7 || summary.OutputTokens.KnownRequests != 2 || summary.OutputTokens.UnknownRequests != 4 || summary.TotalTokens.KnownRequests != 0 || summary.OldestStartedAt == nil || summary.NewestStartedAt == nil {
		t.Fatalf("bad business/usage summary: %+v", summary)
	}
	aux, err := s.Summary(Query{Operation: "count_tokens"})
	if err != nil || aux.Total != 1 || aux.InputTokens.KnownTokens != 100 {
		t.Fatalf("aux=%+v,%v", aux, err)
	}
	page, err := s.List(Query{Status: "failed", AppID: "codex", Operation: "generate"})
	if err != nil || page.Total != 2 {
		t.Fatalf("filter=%+v,%v", page, err)
	}
}

func TestConcurrentRequestsAndRepeatedFinishRemainDistinct(t *testing.T) {
	s, _ := openTest(t, Options{})
	const count = 64
	var wg sync.WaitGroup
	failures := make(chan error, count*3)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("request-%03d", i)
			if err := s.Start(metadata(id)); err != nil {
				failures <- err
				return
			}
			for range 2 {
				if err := s.Finish(id, success()); err != nil {
					failures <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	summary, err := s.Summary(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != count || summary.Succeeded != count || summary.Attempts != count || summary.InputTokens.KnownTokens != 13*count {
		t.Fatalf("concurrent requests lost or duplicated: %+v", summary)
	}
	var endings sync.WaitGroup
	for range 12 {
		endings.Add(1)
		go func() {
			defer endings.Done()
			if err := s.Finish("request-000", Outcome{Status: "failed", SafeErrorCode: "router_error"}); err != nil {
				t.Error(err)
			}
		}()
	}
	endings.Wait()
	if get(t, s, "request-000").Status != "succeeded" {
		t.Fatal("racing endings replaced result")
	}
}

func TestCapacityPreservesActiveAndEvictsOldestTerminal(t *testing.T) {
	s, _ := openTest(t, Options{MaxRecords: 2})
	for _, id := range []string{"active", "completed"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start(metadata("overflow")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("active capacity=%v", err)
	}
	if s.Status().ErrorCode != ErrCapacity.Error() {
		t.Fatal("admission failure was not diagnosable")
	}
	if err := s.Finish("completed", success()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(metadata("replacement")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("completed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal not evicted: %v", err)
	}
	if get(t, s, "active").Status != "processing" {
		t.Fatal("active request evicted")
	}
	page, err := s.List(Query{})
	if err != nil || page.Total != 2 {
		t.Fatalf("capacity=%+v,%v", page, err)
	}
}

func TestRetentionPrunesExpiredTerminalAndRestartedActive(t *testing.T) {
	s, path := openTest(t, Options{MaxAge: time.Hour})
	for _, id := range []string{"old-finished", "old-active", "recent"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Finish("old-finished", success()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-finished", "old-active"} {
		r := get(t, s, id)
		r.StartedAt = r.StartedAt.Add(-2 * time.Hour)
		if r.EndedAt != nil {
			end := r.EndedAt.Add(-2 * time.Hour)
			r.EndedAt = &end
		}
		data, _ := json.Marshal(r)
		if _, err := s.db.Exec("UPDATE requests SET started_ns=?,data=? WHERE request_id=?", r.StartedAt.UnixNano(), string(data), id); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err := Open(path, Options{MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.List(Query{})
	if err != nil || page.Total != 1 || page.Records[0].RequestID != "recent" || page.Records[0].Status != "interrupted" {
		t.Fatalf("retention=%+v,%v", page, err)
	}
}

func TestStableCursorAndBounds(t *testing.T) {
	s, _ := openTest(t, Options{})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List(Query{Limit: 2})
	if err != nil || len(first.Records) != 2 || first.NextCursor == "" || first.Total != 5 {
		t.Fatalf("first=%+v,%v", first, err)
	}
	if err := s.Start(metadata("newer")); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range first.Records {
		seen[r.RequestID] = true
	}
	cursor := first.NextCursor
	for cursor != "" {
		page, err := s.List(Query{Limit: 2, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Records {
			if seen[r.RequestID] || r.RequestID == "newer" {
				t.Fatalf("cursor shifted: %s", r.RequestID)
			}
			seen[r.RequestID] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("cursor lost rows: %v", seen)
	}
	for _, q := range []Query{{Limit: 101}, {Limit: -1}, {Offset: -1}, {Offset: 100001}, {Status: "sql-injection'"}, {Operation: "models"}, {Before: "not-a-cursor"}, {Before: strings.Repeat("x", 257)}, {AppID: "../other"}, {From: time.Now(), To: time.Now().Add(-time.Hour)}} {
		if _, err := s.List(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unbounded query %+v = %v", q, err)
		}
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent=%v", err)
	}
}

func TestUnsafeFieldsNeverPersisted(t *testing.T) {
	s, path := openTest(t, Options{})
	const secret = "sk-test-only-secret-123"
	for _, field := range []string{"path", "model", "appName", "requestId", "providerId"} {
		m := metadata("unsafe")
		switch field {
		case "path":
			m.Path = "/v1/responses?api_key=" + secret
		case "model":
			m.Model = "https://user:" + secret + "@private.invalid"
		case "appName":
			m.AppName = "Authorization: Bearer " + secret
		case "requestId":
			m.RequestID = "../" + secret
		case "providerId":
			m.ProviderID = "provider?key=" + secret
		}
		if err := s.Start(m); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe %s accepted: %v", field, err)
		}
	}
	if err := s.Start(metadata("safe")); err != nil {
		t.Fatal(err)
	}
	o := success()
	o.SafeErrorCode = "transport failed with Bearer " + secret
	if err := s.Finish("safe", o); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe error persisted: %v", err)
	}
	o = success()
	o.AttemptDetails = []Attempt{{Number: 1, StartedAt: time.Now(), ErrorCode: secret}}
	if err := s.Finish("safe", o); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe attempt persisted: %v", err)
	}
	if SafeCode(secret) != "router_error" {
		t.Fatal("unrecognized error did not collapse")
	}
	page, err := s.List(Query{})
	if err != nil {
		t.Fatal(err)
	}
	projection, _ := json.Marshal(page)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(projection, []byte(secret)) {
		t.Fatal("fake secret escaped into SQLite or query")
	}
	if get(t, s, "safe").Status != "processing" {
		t.Fatal("rejected finish changed history")
	}
}

func TestCorruptionAndSchemaAreNeverReinitialized(t *testing.T) {
	for _, fixture := range []string{"not sqlite: fake-secret", ""} {
		path := filepath.Join(t.TempDir(), "requests.sqlite")
		if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corruption=%v", err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != fixture {
			t.Fatal("corruption overwritten")
		}
	}
	for _, statement := range []string{"PRAGMA user_version=99", "DROP TABLE requests", "CREATE TABLE alien (secret TEXT)", `UPDATE requests SET data='{"requestId":"incomplete"}'`, `UPDATE requests SET data=json_set(data,'$.unknown','fake-secret')`, `UPDATE requests SET data=data || '{}'`, `UPDATE requests SET data=data || 'trailing-garbage'`} {
		t.Run(statement, func(t *testing.T) {
			s, path := openTest(t, Options{})
			if err := s.Start(metadata("valid")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			s.Close()
			before, _ := os.ReadFile(path)
			_, err := Open(path, Options{})
			if err == nil {
				t.Fatal("invalid ledger reopened as valid")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid ledger was modified")
			}
		})
	}
}

func TestUnavailableStoreAndDiskFailureAreExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-parent", "requests.sqlite")
	if _, err := Open(path, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing parent=%v", err)
	}
	if _, err := Open("relative.sqlite", Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path=%v", err)
	}
	s, _ := openTest(t, Options{})
	if err := s.Start(metadata("disk-failure")); err != nil {
		t.Fatal(err)
	}
	// query_only injects an actual SQLite write failure without relying on
	// chmod semantics (which root and Windows can bypass).
	if _, err := s.db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("disk-failure", success()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed write=%v", err)
	}
	if s.Status().Available || s.Status().ErrorCode != ErrUnavailable.Error() || get(t, s, "disk-failure").Status != "processing" {
		t.Fatal("write failure masqueraded as persisted success")
	}
	s.Close()
	if _, err := s.Get("disk-failure"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed get=%v", err)
	}
	if err := s.Start(metadata("closed")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed start=%v", err)
	}
}

func TestSymlinkStoreRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require Windows privileges")
	}
	s, path := openTest(t, Options{})
	s.Close()
	link := filepath.Join(t.TempDir(), "ledger.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("symlink=%v", err)
	}
}

func TestLiveProgressIsMonotonicBoundedAndNotDurable(t *testing.T) {
	s, path := openTest(t, Options{})
	if err := s.Start(metadata("live")); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress("live", ProgressState{Attempts: 1, Destination: "cloud", OutputCommitted: true, FailureStage: "response"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress("live", ProgressState{Destination: "none"}); err != nil {
		t.Fatal(err)
	}
	r := get(t, s, "live")
	if r.Attempts != 1 || !r.OutputCommitted || r.Destination != "cloud" || !r.ExecutionKnown {
		t.Fatalf("progress not monotonic: %+v", r)
	}
	page, err := s.List(Query{})
	if err != nil || page.Records[0].Attempts != 1 {
		t.Fatalf("list progress=%+v,%v", page, err)
	}
	var persisted string
	if err := s.db.QueryRow("SELECT data FROM requests WHERE request_id='live'").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	stored, err := decodeRecord(persisted)
	if err != nil || stored.Attempts != 0 || stored.OutputCommitted {
		t.Fatalf("Observe unexpectedly wrote disk: %+v,%v", stored, err)
	}
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r = get(t, s, "live")
	if r.Status != "interrupted" || r.ExecutionKnown || r.Destination != "" {
		t.Fatalf("crash claimed execution knowledge: %+v", r)
	}
	if err := s.Progress("live", ProgressState{Attempts: 2, Destination: "cloud"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recovered progress resurrected: %v", err)
	}
	if err := s.Start(metadata("late")); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("late", success()); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress("late", ProgressState{Attempts: 2, Destination: "cloud"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late event resurrected finished record: %v", err)
	}
	if r := get(t, s, "late"); r.Attempts != 1 || r.Status != "succeeded" || !r.ExecutionKnown {
		t.Fatalf("late progress changed result: %+v", r)
	}
}

func TestProgressNeverWaitsForDatabaseMutex(t *testing.T) {
	s, _ := openTest(t, Options{})
	if err := s.Start(metadata("active")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.Progress("active", ProgressState{Attempts: 1, Destination: "cloud"}) }()
	// The timeout is only a deadlock watchdog. Success is the operation
	// completing while the database lock remains held, not elapsed speed.
	select {
	case err := <-done:
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.mu.Unlock()
		<-done
		t.Fatal("Progress blocked on database mutex")
	}
}

func TestAvailabilityRecoversButKeepsFailureDiagnosis(t *testing.T) {
	s, _ := openTest(t, Options{MaxRecords: 1})
	if err := s.Start(metadata("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(metadata("over-capacity")); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if err := s.Finish("one", success()); err != nil {
		t.Fatal(err)
	}
	status := s.Status()
	if !status.Available || status.ErrorCode != "" || status.FailureCount != 1 || status.LastErrorCode != ErrCapacity.Error() {
		t.Fatalf("availability never recovered or history vanished: %+v", status)
	}
	if err := s.Start(metadata("task-model")); err != nil {
		t.Fatal(err)
	}
}

func TestSameTimestampCursorUsesRequestIDTieBreak(t *testing.T) {
	s, _ := openTest(t, Options{})
	started := time.Now().UTC()
	for _, id := range []string{"a", "c", "b"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
		r := get(t, s, id)
		r.StartedAt = started
		data, _ := json.Marshal(r)
		if _, err := s.db.Exec("UPDATE requests SET started_ns=?,data=? WHERE request_id=?", started.UnixNano(), string(data), id); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List(Query{Limit: 1})
	if err != nil || first.Records[0].RequestID != "c" {
		t.Fatalf("first=%+v,%v", first, err)
	}
	second, err := s.List(Query{Limit: 1, Before: first.NextCursor})
	if err != nil || second.Records[0].RequestID != "b" {
		t.Fatalf("second=%+v,%v", second, err)
	}
	third, err := s.List(Query{Limit: 1, Before: second.NextCursor})
	if err != nil || third.Records[0].RequestID != "a" || third.NextCursor != "" {
		t.Fatalf("third=%+v,%v", third, err)
	}
}

func TestUsageValidationAndSafeNames(t *testing.T) {
	s, _ := openTest(t, Options{})
	m := metadata("usage")
	m.Model = "task-model/large:latest"
	m.AppName = "Task-model 开发助手"
	if err := s.Start(m); err != nil {
		t.Fatalf("valid configured snapshot rejected: %v", err)
	}
	for _, n := range []int64{-1} {
		o := success()
		o.Usage = &Usage{InputTokens: &n}
		if err := s.Finish("usage", o); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid usage %d accepted: %v", n, err)
		}
	}
	o := success()
	o.Usage = &Usage{InputTokens: integer(0), OutputTokens: integer(0), TotalTokens: integer(0)}
	if err := s.Finish("usage", o); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(Query{})
	if err != nil || summary.TotalTokens.KnownRequests != 1 || summary.TotalTokens.KnownTokens != 0 || summary.InputTokens.UnknownRequests != 0 {
		t.Fatalf("explicit zero lost: %+v,%v", summary, err)
	}
}

func TestActualLargeUsageSurvivesAndAggregateOverflowIsExplicit(t *testing.T) {
	s, _ := openTest(t, Options{})
	for _, id := range []string{"large-a", "large-b"} {
		if err := s.Start(metadata(id)); err != nil {
			t.Fatal(err)
		}
		o := success()
		o.Usage.InputTokens = integer(math.MaxInt64)
		if err := s.Finish(id, o); err != nil {
			t.Fatalf("actual nonnegative usage rejected: %v", err)
		}
		if *get(t, s, id).Usage.InputTokens != math.MaxInt64 {
			t.Fatal("actual usage was altered")
		}
	}
	if _, err := s.Summary(Query{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("overflow total invented: %v", err)
	}
	if s.Status().Available || s.Status().ErrorCode != ErrUnavailable.Error() {
		t.Fatal("aggregate failure not diagnosed")
	}
}

func TestSQLitePageLimitIsAVisibleWriteFailure(t *testing.T) {
	s, _ := openTest(t, Options{})
	var current int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", current+1)); err != nil {
		t.Fatal(err)
	}
	failed := false
	for i := range 200 {
		m := metadata(fmt.Sprintf("bounded-%d", i))
		m.Model = strings.Repeat("x", 256)
		if err := s.Start(m); err != nil {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			failed = true
			break
		}
		if err := s.Finish(m.RequestID, success()); err != nil {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			failed = true
			if get(t, s, m.RequestID).Status != "processing" {
				t.Fatal("full disk persisted fake success")
			}
			break
		}
	}
	if !failed || s.Status().Available || s.Status().ErrorCode != ErrUnavailable.Error() {
		t.Fatal("SQLite page cap failed without diagnosis")
	}
	var final int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&final); err != nil || final > current+1 {
		t.Fatalf("disk growth exceeded cap: %d,%v", final, err)
	}
}

func TestIncompatiblePageSizeAndOversizedStoreRejected(t *testing.T) {
	s, path := openTest(t, Options{})
	if _, err := s.db.Exec("PRAGMA page_size=8192"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, _ := os.ReadFile(path)
	if _, err := Open(path, Options{}); !errors.Is(err, ErrSchema) {
		t.Fatalf("unsupported page size=%v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("incompatible page layout modified")
	}
	large := filepath.Join(t.TempDir(), "oversized.sqlite")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(128*1024*1024 + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := Open(large, Options{}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("oversized store=%v", err)
	}
}

func TestCallerErrorsDoNotMarkStoreUnavailable(t *testing.T) {
	s, _ := openTest(t, Options{MaxRecords: 1})
	if err := s.Start(Metadata{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
	if !s.Status().Available || s.Status().ErrorCode != ErrInvalid.Error() || s.Status().FailureCount != 1 {
		t.Fatalf("ErrInvalid should keep Available=true and record diagnosis: %+v", s.Status())
	}
	if err := s.Start(metadata("active-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(metadata("active-1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if !s.Status().Available {
		t.Fatalf("ErrConflict should keep Available=true: %+v", s.Status())
	}
	if err := s.Start(metadata("active-2")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got %v", err)
	}
	if !s.Status().Available || s.Status().ErrorCode != ErrCapacity.Error() || s.Status().FailureCount != 2 {
		t.Fatalf("ErrCapacity should keep Available=true and record diagnosis: %+v", s.Status())
	}
	if _, err := s.Get("nonexistent-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !s.Status().Available {
		t.Fatalf("ErrNotFound should keep Available=true: %+v", s.Status())
	}
}
