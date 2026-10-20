package routerledger

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const tableSQL = `CREATE TABLE requests (request_id TEXT PRIMARY KEY, started_ns INTEGER NOT NULL, app_id TEXT NOT NULL, operation TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL)`

// Store has one database connection and no background workers. Start and Finish
// are synchronous durability boundaries; Observe callbacks must stay in memory.
// The service's existing process lock must exclude a second owner of this file.
type Store struct {
	mu                  sync.Mutex
	progressMu          sync.RWMutex
	progress            map[string]ProgressState
	db                  *sql.DB
	opts                Options
	status              Status
	closed              bool
	observationFailures atomic.Int64
}

func Open(path string, opts Options) (*Store, error) {
	if opts.MaxRecords == 0 {
		opts.MaxRecords = 10000
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = 30 * 24 * time.Hour
	}
	if opts.MaxCalls == 0 {
		opts.MaxCalls = 50000
	}
	if opts.MaxCallsPerRequest == 0 {
		opts.MaxCallsPerRequest = 128
	}
	if opts.MaxCheckCalls == 0 {
		opts.MaxCheckCalls = 5000
	}
	if !filepath.IsAbs(path) || opts.MaxRecords < 1 || opts.MaxRecords > 100000 || opts.MaxAge < time.Second || opts.MaxAge > 365*24*time.Hour || opts.MaxCalls < 1 || opts.MaxCalls > 100000 || opts.MaxCallsPerRequest < 1 || opts.MaxCallsPerRequest > 1000 || opts.MaxCheckCalls < 1 || opts.MaxCheckCalls > 10000 {
		return nil, ErrInvalid
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnavailable
	}
	created := false
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, ErrUnavailable
		}
		if e = f.Close(); e != nil {
			return nil, ErrUnavailable
		}
		created = true
	} else if err != nil {
		return nil, ErrUnavailable
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnavailable
	} else if info.Size() == 0 {
		return nil, ErrCorrupt
	} else if info.Size() > 128*1024*1024 {
		return nil, ErrCapacity
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, ErrUnavailable
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, opts: opts, progress: make(map[string]ProgressState), status: Status{Available: true, SchemaVersion: SchemaVersion, MaxRecords: opts.MaxRecords, MaxAgeSeconds: int64(opts.MaxAge / time.Second), MaxCalls: opts.MaxCalls, MaxCallsPerRequest: opts.MaxCallsPerRequest, MaxCheckCalls: opts.MaxCheckCalls}}
	if err := s.initialize(created); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(created bool) error {
	var integrity string
	if err := s.db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return ErrCorrupt
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return ErrCorrupt
	}
	if !created && version != 1 && version != SchemaVersion {
		return ErrSchema
	}
	var pageSize int
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return ErrCorrupt
	}
	if pageSize != 4096 {
		return ErrSchema
	}
	if !created {
		var schema string
		if err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='requests'").Scan(&schema); err != nil || schema != tableSQL {
			return ErrSchema
		}
		var unexpected int
		allowed := "name != 'requests'"
		if version == SchemaVersion {
			allowed += " AND name != 'calls'"
		}
		if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type IN ('view','trigger') OR (type='table' AND " + allowed + ")").Scan(&unexpected); err != nil || unexpected != 0 {
			return ErrSchema
		}
		if version == SchemaVersion {
			if err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='calls'").Scan(&schema); err != nil || schema != callsTableSQL {
				return ErrSchema
			}
		}
	}
	// Validate before updating interrupted records or applying retention. A bad
	// row must never be hidden by cleanup and mistaken for an empty ledger.
	var records []Record
	count := 0
	if !created {
		rows, err := s.db.Query("SELECT request_id,started_ns,app_id,operation,status,data FROM requests ORDER BY started_ns,request_id")
		if err != nil {
			return ErrSchema
		}
		for rows.Next() {
			var id, app, op, status, data string
			var ns int64
			if err := rows.Scan(&id, &ns, &app, &op, &status, &data); err != nil {
				rows.Close()
				return ErrCorrupt
			}
			r, e := decodeRecordVersion(data, version)
			if e != nil || r.RequestID != id || r.StartedAt.UnixNano() != ns || r.AppID != app || r.Operation != op || r.Status != status {
				rows.Close()
				return ErrCorrupt
			}
			if r.Status == "processing" || version == 1 {
				records = append(records, r)
			}
			count++
			if count > 100000 {
				rows.Close()
				return ErrCorrupt
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ErrCorrupt
		}
		rows.Close()
		if version == SchemaVersion {
			if err := s.validateCalls(); err != nil {
				return err
			}
		}
	}
	// All schema and row checks above precede every persistent mutation. Schema 1
	// upgrades, restart outcomes and retention share the transaction below.
	for _, statement := range []string{"PRAGMA busy_timeout=2000", "PRAGMA synchronous=FULL", "PRAGMA journal_mode=DELETE", "PRAGMA secure_delete=ON", "PRAGMA max_page_count=32768"} {
		if _, err := s.db.Exec(statement); err != nil {
			return ErrUnavailable
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if created {
		for _, statement := range []string{tableSQL, "CREATE INDEX requests_order ON requests(started_ns DESC, request_id DESC)", "CREATE INDEX requests_filter ON requests(app_id, operation, status, started_ns DESC, request_id DESC)"} {
			if _, err := tx.Exec(statement); err != nil {
				return ErrUnavailable
			}
		}
	}
	if created || version == 1 {
		for _, statement := range []string{callsTableSQL, "CREATE INDEX calls_request ON calls(request_id,started_ns,call_id)", "CREATE INDEX calls_order ON calls(started_ns,call_id)", "PRAGMA user_version=2"} {
			if _, err := tx.Exec(statement); err != nil {
				return ErrUnavailable
			}
		}
	}
	now := time.Now().UTC()
	for _, r := range records {
		r.SchemaVersion = SchemaVersion
		if r.Status == "processing" {
			r.EndedAt = &now
			r.DurationMS = max(0, now.Sub(r.StartedAt).Milliseconds())
			r.Status = "interrupted"
			r.ExecutionKnown = false
			r.Destination = ""
			r.SafeErrorCode = "request_interrupted"
			r.FailureStage = "interrupted"
		}
		if err := update(tx, r); err != nil {
			return ErrUnavailable
		}
	}
	if err := s.prune(tx, now, 0); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func decodeRecord(data string) (Record, error) {
	return decodeRecordVersion(data, SchemaVersion)
}

func decodeRecordVersion(data string, version int) (Record, error) {
	var r Record
	decoder := json.NewDecoder(bytes.NewBufferString(data))
	decoder.DisallowUnknownFields()
	if len(data) > 8192 || !json.Valid([]byte(data)) || decoder.Decode(&r) != nil || r.SchemaVersion != version || !validMetadata(r.Metadata) || !validOutcome(r.Outcome, true) || r.StartedAt.IsZero() || r.DurationMS < 0 || (r.Status == "processing") != (r.EndedAt == nil) {
		return r, ErrCorrupt
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &fields) != nil {
		return r, ErrCorrupt
	}
	if price := fields["referencePrice"]; len(price) > 0 && !bytes.Equal(bytes.TrimSpace(price), []byte("null")) {
		var snapshot map[string]json.RawMessage
		if json.Unmarshal(price, &snapshot) != nil || len(snapshot) != 6 {
			return r, ErrCorrupt
		}
		for _, field := range []string{"currency", "unit", "input", "output", "version", "source"} {
			if len(snapshot[field]) == 0 || bytes.Equal(bytes.TrimSpace(snapshot[field]), []byte("null")) {
				return r, ErrCorrupt
			}
		}
	}
	if version == 1 {
		if fields["referencePrice"] != nil || fields["localModel"] != nil || fields["localCallId"] != nil {
			return r, ErrCorrupt
		}
	}
	if r.EndedAt != nil && r.EndedAt.Before(r.StartedAt) {
		return r, ErrCorrupt
	}
	return r, nil
}

func update(tx *sql.Tx, r Record) error {
	data, err := json.Marshal(r)
	if err != nil || len(data) > 8192 {
		return ErrInvalid
	}
	_, err = tx.Exec("UPDATE requests SET status=?,data=? WHERE request_id=?", r.Status, string(data), r.RequestID)
	return err
}

func (s *Store) prune(tx *sql.Tx, now time.Time, reserve int) error {
	if _, err := tx.Exec("DELETE FROM requests WHERE status != 'processing' AND started_ns < ?", now.Add(-s.opts.MaxAge).UnixNano()); err != nil {
		return ErrUnavailable
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM requests").Scan(&count); err != nil {
		return ErrUnavailable
	}
	remove := count + reserve - s.opts.MaxRecords
	if remove <= 0 {
		return s.pruneCalls(tx, now)
	}
	result, err := tx.Exec("DELETE FROM requests WHERE request_id IN (SELECT request_id FROM requests WHERE status != 'processing' ORDER BY started_ns,request_id LIMIT ?)", remove)
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if int(n) < remove {
		return ErrCapacity
	}
	return s.pruneCalls(tx, now)
}

func (s *Store) failure(err error) error {
	if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrCapacity) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
		s.status.Available = false
	}
	s.status.ErrorCode = err.Error()
	s.status.LastErrorCode = err.Error()
	s.status.FailureCount++
	return err
}

func (s *Store) recovered() {
	s.status.Available = true
	s.status.ErrorCode = ""
}

func (s *Store) ready() error {
	if s.closed {
		return s.failure(ErrUnavailable)
	}
	return nil
}

// Start commits a new processing record before returning. Duplicate IDs cannot
// replace snapshots. Capacity occupied entirely by active requests rejects Start.
func (s *Store) Start(m Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if !validMetadata(m) {
		return s.failure(ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return s.failure(ErrUnavailable)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow("SELECT count(*) FROM requests WHERE request_id=?", m.RequestID).Scan(&exists); err != nil {
		return s.failure(ErrUnavailable)
	}
	if exists != 0 {
		return ErrConflict
	}
	now := time.Now().UTC()
	started := now
	if !m.AcceptedAt.IsZero() {
		if m.AcceptedAt.After(now) || m.AcceptedAt.Year() < 2000 {
			return s.failure(ErrInvalid)
		}
		started = m.AcceptedAt.UTC()
	}
	m.AcceptedAt = time.Time{}
	if err := s.prune(tx, now, 1); err != nil {
		return s.failure(err)
	}
	r := Record{SchemaVersion: SchemaVersion, Metadata: m, StartedAt: started, Outcome: Outcome{Status: "processing", ExecutionKnown: true, Destination: "none", AttemptDetails: []Attempt{}}}
	data, _ := json.Marshal(r)
	if _, err := tx.Exec("INSERT INTO requests(request_id,started_ns,app_id,operation,status,data) VALUES(?,?,?,?,?,?)", m.RequestID, started.UnixNano(), m.AppID, m.Operation, r.Status, string(data)); err != nil {
		return s.failure(ErrUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return s.failure(ErrUnavailable)
	}
	s.progressMu.Lock()
	s.progress[m.RequestID] = ProgressState{Destination: "none"}
	s.progressMu.Unlock()
	s.recovered()
	return nil
}

// Finish is first-writer-wins: repeated terminal updates succeed without changing
// the stored result. Callers therefore cannot double count retries or usage.
func (s *Store) Finish(id string, o Outcome) error {
	if o.AttemptDetails == nil {
		o.AttemptDetails = []Attempt{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if !identifier(id, 128, false) || !validOutcome(o, false) {
		return s.failure(ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return s.failure(ErrUnavailable)
	}
	defer tx.Rollback()
	var data string
	err = tx.QueryRow("SELECT data FROM requests WHERE request_id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return s.failure(ErrUnavailable)
	}
	r, err := decodeRecord(data)
	if err != nil {
		return s.failure(err)
	}
	if r.Status != "processing" {
		return nil
	}
	now := time.Now().UTC()
	r.EndedAt = &now
	r.DurationMS = max(0, now.Sub(r.StartedAt).Milliseconds())
	if (o.ResolvedProviderID != "" || o.ResolvedModel != "") && identifier(o.ResolvedProviderID, 128, true) && display(o.ResolvedModel, 256) && validReferencePrice(o.ResolvedReferencePrice) {
		r.ProviderID = o.ResolvedProviderID
		r.Model = o.ResolvedModel
		if o.ResolvedReferencePrice != nil {
			r.ReferencePrice = o.ResolvedReferencePrice
		}
	}
	r.Outcome = o
	r.ExecutionKnown = true
	if err := update(tx, r); err != nil {
		return s.failure(ErrUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return s.failure(ErrUnavailable)
	}
	s.progressMu.Lock()
	delete(s.progress, id)
	s.progressMu.Unlock()
	s.recovered()
	return nil
}

func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.status
	out.ObservationFailures = s.observationFailures.Load()
	return out
}

// ReportObservationFailure is safe for a callback while the model owns a lock.
// The count remains visible even if a later successful write restores storage.
func (s *Store) ReportObservationFailure() {
	if s != nil {
		s.observationFailures.Add(1)
	}
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.progressMu.Lock()
	clear(s.progress)
	s.progressMu.Unlock()
	s.status.Available = false
	s.status.ErrorCode = ErrUnavailable.Error()
	if err := s.db.Close(); err != nil {
		return ErrUnavailable
	}
	return nil
}
