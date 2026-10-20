package routerledger

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// schemaOneFixture writes the literal previous schema and previous projection,
// rather than creating schema 2 then pretending its version header was older.
func schemaOneFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema-one.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{tableSQL, "CREATE INDEX requests_order ON requests(started_ns DESC, request_id DESC)", "CREATE INDEX requests_filter ON requests(app_id, operation, status, started_ns DESC, request_id DESC)", "PRAGMA user_version=1"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(-time.Hour).UTC()
	for _, id := range []string{"old-success", "old-processing"} {
		r := Record{SchemaVersion: 1, Metadata: metadata(id), StartedAt: now, Outcome: Outcome{Status: "processing", Destination: "none", ExecutionKnown: true, AttemptDetails: []Attempt{}}}
		if id == "old-success" {
			r.Outcome = success()
			r.ExecutionKnown = true
			ended := now.Add(time.Second)
			r.EndedAt = &ended
			r.DurationMS = 1000
		}
		encoded, _ := json.Marshal(r)
		var previous map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &previous); err != nil {
			t.Fatal(err)
		}
		delete(previous, "referencePrice")
		delete(previous, "localModel")
		delete(previous, "localCallId")
		encoded, _ = json.Marshal(previous)
		if _, err := db.Exec("INSERT INTO requests VALUES(?,?,?,?,?,?)", id, now.UnixNano(), r.AppID, r.Operation, r.Status, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestSchemaOneMigrationPreservesUnknownsAndHistory(t *testing.T) {
	path := schemaOneFixture(t)
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := get(t, s, "old-success")
	if old.SchemaVersion != 2 || old.ReferencePrice != nil || old.LocalModel != "" || old.Status != "succeeded" || old.Usage == nil || old.Usage.InputTokens == nil || *old.Usage.InputTokens != 13 || old.Usage.TotalTokens != nil {
		t.Fatalf("migration rewrote history or fabricated values: %+v", old)
	}
	interrupted := get(t, s, "old-processing")
	if interrupted.Status != "interrupted" || interrupted.ExecutionKnown || interrupted.Destination != "" || interrupted.EndedAt == nil {
		t.Fatalf("processing migration claimed known execution: %+v", interrupted)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema header=%d %v", version, err)
	}
	calls, err := s.Calls("old-success")
	if err != nil || len(calls) != 0 {
		t.Fatalf("migration fabricated internal history: %+v %v", calls, err)
	}
	if err := s.RecordCall(callFixture("old-success", "late-migrated")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	calls, err = reopened.Calls("old-success")
	if err != nil || len(calls) != 1 {
		t.Fatalf("migrated storage did not persist calls: %+v %v", calls, err)
	}
}

func TestSchemaOneBadRowsValidateBeforeMigrationOrRetention(t *testing.T) {
	for _, statement := range []string{
		`UPDATE requests SET data=json_set(data,'$.schemaVersion',99)`,
		`UPDATE requests SET data=json_set(data,'$.prompt','Bearer fake-secret')`,
		`UPDATE requests SET data=json_set(data,'$.referencePrice',NULL)`,
		`UPDATE requests SET started_ns=1 WHERE request_id='old-success'`,
		`CREATE TABLE calls (secret TEXT)`,
		`CREATE TRIGGER requests AFTER INSERT ON requests BEGIN SELECT 1; END`,
		`PRAGMA user_version=99`,
	} {
		t.Run(statement, func(t *testing.T) {
			path := schemaOneFixture(t)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if s, err := Open(path, Options{MaxAge: time.Second, MaxRecords: 1}); err == nil {
				s.Close()
				t.Fatal("invalid legacy store migrated/cleaned")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("validation failure changed legacy bytes")
			}
		})
	}
}

func TestSchemaOneMigrationWriteFailureRollsBack(t *testing.T) {
	path := schemaOneFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, opts: Options{MaxRecords: 10000, MaxAge: 30 * 24 * time.Hour, MaxCalls: 50000, MaxCallsPerRequest: 128, MaxCheckCalls: 5000}}
	if err := s.initialize(false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write failure not reported: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed migration partially changed schema/history")
	}
	var version, calls int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='calls'").Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if version != 1 || calls != 0 {
		t.Fatalf("partial migration version=%d calls=%d", version, calls)
	}
}
