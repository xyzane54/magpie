package routerledger

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

func filters(q Query, cursor bool) (string, []any, error) {
	if q.Limit < 0 || q.Limit > 100 || q.Offset < 0 || q.Offset > 100000 || q.AppID != "" && !identifier(q.AppID, 64, false) || q.Status != "" && !validStatus(q.Status, true) || q.Operation != "" && q.Operation != "generate" && q.Operation != "count_tokens" || !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) || len(q.Before) > 256 {
		return "", nil, ErrInvalid
	}
	where := " WHERE 1=1"
	args := []any{}
	for _, f := range []struct{ column, value string }{{"app_id", q.AppID}, {"status", q.Status}, {"operation", q.Operation}} {
		if f.value != "" {
			where += " AND " + f.column + "=?"
			args = append(args, f.value)
		}
	}
	if !q.From.IsZero() {
		where += " AND started_ns>=?"
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		where += " AND started_ns<?"
		args = append(args, q.To.UnixNano())
	}
	if q.Before != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Before)
		if err != nil {
			return "", nil, ErrInvalid
		}
		parts := strings.SplitN(string(raw), ":", 2)
		if len(parts) != 2 || !identifier(parts[1], 128, false) {
			return "", nil, ErrInvalid
		}
		ns, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || ns <= 0 {
			return "", nil, ErrInvalid
		}
		if cursor {
			where += " AND (started_ns < ? OR (started_ns = ? AND request_id < ?))"
			args = append(args, ns, ns, parts[1])
		}
	}
	return where, args, nil
}

func (s *Store) Get(id string) (Record, error) {
	if !identifier(id, 128, false) {
		return Record{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Record{}, err
	}
	var data string
	err := s.db.QueryRow("SELECT data FROM requests WHERE request_id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, s.failure(ErrUnavailable)
	}
	r, err := decodeRecord(data)
	if err != nil {
		return Record{}, s.failure(err)
	}
	return s.overlay(r), nil
}

// List returns at most 100 rows ordered by immutable start time then request ID.
// Before is an opaque keyset cursor: concurrent newer requests cannot shift the
// next page. Total counts the complete filter, without the cursor or offset.
func (s *Store) List(q Query) (Page, error) {
	where, args, err := filters(q, true)
	if err != nil {
		return Page{}, err
	}
	allWhere, allArgs, _ := filters(q, false)
	if q.Limit == 0 {
		q.Limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Page{}, err
	}
	page := Page{Records: []Record{}}
	if err := s.db.QueryRow("SELECT count(*) FROM requests"+allWhere, allArgs...).Scan(&page.Total); err != nil {
		return Page{}, s.failure(ErrUnavailable)
	}
	args = append(args, q.Limit+1, q.Offset)
	rows, err := s.db.Query("SELECT data FROM requests"+where+" ORDER BY started_ns DESC,request_id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return Page{}, s.failure(ErrUnavailable)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return Page{}, s.failure(ErrUnavailable)
		}
		r, err := decodeRecord(data)
		if err != nil {
			return Page{}, s.failure(err)
		}
		page.Records = append(page.Records, s.overlay(r))
	}
	if err := rows.Err(); err != nil {
		return Page{}, s.failure(ErrUnavailable)
	}
	if len(page.Records) > q.Limit {
		page.Records = page.Records[:q.Limit]
		last := page.Records[q.Limit-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.StartedAt.UnixNano(), 10) + ":" + last.RequestID))
	}
	return page, nil
}

// Summary counts generation by default. Auxiliary operations require an
// explicit operation filter and never silently enter generation totals. Usage
// is the final reported request usage once, not an estimate of retry billing.
func (s *Store) Summary(q Query) (Summary, error) {
	if q.Operation == "" {
		q.Operation = "generate"
	}
	where, args, err := filters(q, false)
	if err != nil {
		return Summary{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Summary{}, err
	}
	out := Summary{Operation: q.Operation}
	query := `SELECT count(*),
coalesce(sum(status='processing'),0),coalesce(sum(status='succeeded'),0),coalesce(sum(status='failed'),0),
coalesce(sum(status='canceled'),0),coalesce(sum(status='timed_out'),0),coalesce(sum(status='interrupted'),0),
coalesce(sum(json_extract(data,'$.attempts')),0),min(started_ns),max(started_ns)`
	for _, field := range []string{"inputTokens", "outputTokens", "totalTokens"} {
		path := "'$.usage." + field + "'"
		query += ",coalesce(sum(json_extract(data," + path + ")),0),count(json_extract(data," + path + "))"
	}
	var oldest, newest sql.NullInt64
	err = s.db.QueryRow(query+" FROM requests"+where, args...).Scan(&out.Total, &out.Processing, &out.Succeeded, &out.Failed, &out.Canceled, &out.TimedOut, &out.Interrupted, &out.Attempts, &oldest, &newest, &out.InputTokens.KnownTokens, &out.InputTokens.KnownRequests, &out.OutputTokens.KnownTokens, &out.OutputTokens.KnownRequests, &out.TotalTokens.KnownTokens, &out.TotalTokens.KnownRequests)
	if err != nil {
		return Summary{}, s.failure(ErrUnavailable)
	}
	for _, tokens := range []*TokenSummary{&out.InputTokens, &out.OutputTokens, &out.TotalTokens} {
		tokens.UnknownRequests = out.Total - tokens.KnownRequests
	}
	if oldest.Valid {
		t := time.Unix(0, oldest.Int64).UTC()
		out.OldestStartedAt = &t
	}
	if newest.Valid {
		t := time.Unix(0, newest.Int64).UTC()
		out.NewestStartedAt = &t
	}
	// The database mutex covers both business rows and call rows. A completed
	// call cannot arrive between the two parts of this retained-window snapshot.
	rows, err := s.db.Query("SELECT data FROM requests"+where, args...)
	if err != nil {
		return Summary{}, s.failure(ErrUnavailable)
	}
	records := []Record{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return Summary{}, s.failure(ErrUnavailable)
		}
		r, err := decodeRecord(data)
		if err != nil {
			rows.Close()
			return Summary{}, s.failure(err)
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Summary{}, s.failure(ErrUnavailable)
	}
	calls, err := s.callsLocked(q)
	if err != nil {
		return Summary{}, err
	}
	checks, err := s.checkCallsLocked(q)
	if err != nil {
		return Summary{}, err
	}
	out.CoreStatistics, err = buildCoreStatistics(records, calls, checks)
	if err != nil {
		return Summary{}, s.failure(err)
	}
	return out, nil
}
