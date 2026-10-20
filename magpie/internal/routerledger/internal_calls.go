package routerledger

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

const callsTableSQL = `CREATE TABLE calls (call_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, started_ns INTEGER NOT NULL, scope TEXT NOT NULL, purpose TEXT NOT NULL, data TEXT NOT NULL)`

// Call is a safe terminal observation of one internal invocation. It is not a
// business request and its usage must not be added to final business usage.
type Call struct {
	RequestID           string                       `json:"requestId"`
	CallID              string                       `json:"callId"`
	Scope               string                       `json:"scope"`
	Purpose             string                       `json:"purpose"`
	ModelID             string                       `json:"modelId"`
	Fingerprint         string                       `json:"fingerprint"`
	RuntimeID           string                       `json:"runtimeId"`
	RuntimeFingerprint  string                       `json:"runtimeFingerprint"`
	MaterialVersion     string                       `json:"materialVersion"`
	PromptVersion       string                       `json:"promptVersion"`
	Suite               string                       `json:"suite"`
	Mode                string                       `json:"mode"`
	StartedAt           time.Time                    `json:"startedAt"`
	EndedAt             time.Time                    `json:"endedAt"`
	Result              string                       `json:"result"`
	SafeErrorCode       string                       `json:"safeErrorCode"`
	InferenceStarted    bool                         `json:"inferenceStarted"`
	Usage               *Usage                       `json:"usage"`
	Timings             CallTimings                  `json:"timings"`
	SettingsFingerprint string                       `json:"settingsFingerprint,omitempty"`
	Settings            routerconfig.RuntimeSettings `json:"settings,omitzero"`
	ContextUsage        *CallContextUsage            `json:"contextUsage,omitempty"`
	HTTPStatus          int                          `json:"httpStatus,omitempty"`
}

// CallContextUsage is an exact local tokenizer capacity check, not cloud usage
// or an estimate. It can exceed the window when admission rejected that input.
// OutputTokens may be zero for a failed admission with no remaining capacity.
type CallContextUsage struct {
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
	ContextTokens int `json:"contextTokens"`
}

// CallTimings preserves unknown stages. QueueLoadMS is a measured combination,
// never a source from which queue/load durations can be fabricated.
type CallTimings struct {
	QueueLoadMS      *int64 `json:"queueLoadMs"`
	QueueMS          *int64 `json:"queueMs"`
	LoadMS           *int64 `json:"loadMs"`
	InferenceMS      *int64 `json:"inferenceMs"`
	RequestHeadersMS *int64 `json:"requestHeadersMs"`
	ConsumeMS        *int64 `json:"consumeMs"`
	TotalMS          *int64 `json:"totalMs"`
}

// SafeCallCode is an exact allowlist. Unknown errors collapse before storage,
// and the store rejects callers that pass their raw error as a code.
func SafeCallCode(code string) string {
	if SafeCode(code) == code {
		return code
	}
	switch code {
	case "local_judgment_queue_timeout", "local_judgment_inference_timeout", "local_execution_queue_timeout", "local_execution_timeout", "local_context_exceeded", "local_judgment_input_exceeded", "local_token_count_unavailable", "local_runtime_settings_changed":
		return code
	case "judgment_queue_timeout", "judgment_inference_timeout", "judgment_stage_timeout", "execution_queue_timeout", "execution_timeout", "context_limit_exceeded", "context_exceeded", "token_count_unavailable", "judgment_input_exceeded", "local_budget_reserved", "local_generation_failed", "local_model_load_timeout":
		return code
	case "local_model_disabled", "local_model_not_installed", "local_model_queue_full", "local_model_process_fault", "local_model_busy", "local_model_capability_unavailable", "local_generation_http_error", "local_generation_error", "local_generation_timeout", "local_generation_invalid_result", "redaction_invalid_result", "redaction_budget_exhausted", "judgment_invalid_result", "execution_invalid_result", "context_canceled", "context_deadline_exceeded":
		return code
	}
	return "router_error"
}

func validCall(c Call) bool {
	if !identifier(c.CallID, 128, false) || !identifier(c.RequestID, 128, c.Scope == "check") || !identifier(c.ModelID, 128, true) || !display(c.ModelID, 128) || !identifier(strings.ReplaceAll(c.Fingerprint, ":", "."), 256, true) || !display(c.Fingerprint, 256) || !identifier(strings.ReplaceAll(c.MaterialVersion, ":", "."), 128, true) || !display(c.MaterialVersion, 128) || c.StartedAt.IsZero() || c.EndedAt.IsZero() || c.EndedAt.Before(c.StartedAt) || !validUsage(c.Usage) || SafeCallCode(c.SafeErrorCode) != c.SafeErrorCode {
		return false
	}
	if c.Settings.Validate() != nil || c.SettingsFingerprint != "" && c.SettingsFingerprint != c.Settings.Fingerprint() || !c.Settings.IsZero() && c.SettingsFingerprint == "" || c.HTTPStatus != 0 && (c.HTTPStatus < 100 || c.HTTPStatus > 599) {
		return false
	}
	if u := c.ContextUsage; u != nil && (u.InputTokens < 0 || u.OutputTokens < 0 || u.ContextTokens <= 0 || u.InputTokens > 1<<30 || u.OutputTokens > 1<<30 || u.ContextTokens > 1<<30 || u.OutputTokens == 0 && (c.Result == "succeeded" || c.InferenceStarted)) {
		return false
	}
	for _, value := range []string{c.RuntimeID, c.RuntimeFingerprint, c.PromptVersion, c.Suite} {
		if !identifier(strings.ReplaceAll(value, ":", "."), 256, true) || !display(value, 256) {
			return false
		}
	}
	if c.Scope != "business" && c.Scope != "check" || c.Scope == "check" && c.RequestID != "" {
		return false
	}
	if c.Purpose != "judgment" && c.Purpose != "execution" && c.Purpose != "redaction" {
		return false
	}
	if c.Mode != "formal" && c.Mode != "experimental" && !(c.Mode == "diagnostic" && c.Scope == "check") {
		return false
	}
	switch c.Result {
	case "succeeded", "failed", "canceled", "timed_out", "rejected", "interrupted":
	default:
		return false
	}
	if !c.InferenceStarted && c.Usage != nil {
		return false
	}
	for _, p := range []*int64{c.Timings.QueueLoadMS, c.Timings.QueueMS, c.Timings.LoadMS, c.Timings.InferenceMS, c.Timings.RequestHeadersMS, c.Timings.ConsumeMS, c.Timings.TotalMS} {
		if p != nil && *p < 0 {
			return false
		}
	}
	return true
}

func decodeCall(data string) (Call, error) {
	var c Call
	decoder := json.NewDecoder(bytes.NewBufferString(data))
	decoder.DisallowUnknownFields()
	if len(data) > 4096 || !json.Valid([]byte(data)) || decoder.Decode(&c) != nil || !validCall(c) {
		return c, ErrCorrupt
	}
	return c, nil
}

// RecordCall commits one terminal event. A repeated identity preserves the first
// terminal result, even after business completion or a process restart.
func (s *Store) RecordCall(c Call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if !validCall(c) || err != nil || len(data) > 4096 {
		return s.failure(ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return s.failure(ErrUnavailable)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT data FROM calls WHERE call_id=?", c.CallID).Scan(&existing)
	if err == nil {
		first, decodeErr := decodeCall(existing)
		if decodeErr != nil {
			return s.failure(decodeErr)
		}
		if first.RequestID != c.RequestID || first.Scope != c.Scope || first.Purpose != c.Purpose {
			return s.failure(ErrConflict)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return s.failure(ErrUnavailable)
	}
	if c.Scope == "business" {
		var exists int
		if err := tx.QueryRow("SELECT count(*) FROM requests WHERE request_id=?", c.RequestID).Scan(&exists); err != nil {
			return s.failure(ErrUnavailable)
		}
		if exists == 0 {
			return ErrNotFound
		}
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM calls WHERE request_id=?", c.RequestID).Scan(&count); err != nil {
			return s.failure(ErrUnavailable)
		}
		// Refuse excess observations rather than evicting their deduplication
		// identities while the parent request still exists.
		if count >= s.opts.MaxCallsPerRequest {
			return s.failure(ErrCapacity)
		}
	}
	if err := s.pruneCalls(tx, time.Now().UTC()); err != nil {
		return s.failure(err)
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM calls").Scan(&count); err != nil {
		return s.failure(ErrUnavailable)
	}
	if count >= s.opts.MaxCalls {
		return s.failure(ErrCapacity)
	}
	if c.Scope == "check" {
		if err := tx.QueryRow("SELECT count(*) FROM calls WHERE scope='check'").Scan(&count); err != nil {
			return s.failure(ErrUnavailable)
		}
		if count >= s.opts.MaxCheckCalls {
			return s.failure(ErrCapacity)
		}
	}
	if _, err := tx.Exec("INSERT INTO calls(call_id,request_id,started_ns,scope,purpose,data) VALUES(?,?,?,?,?,?)", c.CallID, c.RequestID, c.StartedAt.UnixNano(), c.Scope, c.Purpose, string(data)); err != nil {
		return s.failure(ErrUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return s.failure(ErrUnavailable)
	}
	s.recovered()
	return nil
}

// Calls returns one parent's bounded history. An empty ID selects only explicit
// management checks; unassociated checks are never assigned to a nearby request.
func (s *Store) Calls(requestID string) ([]Call, error) {
	if !identifier(requestID, 128, true) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.readCallsLocked("SELECT data FROM calls WHERE request_id=? ORDER BY started_ns,call_id", requestID)
}

func (s *Store) readCallsLocked(query string, args ...any) ([]Call, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, s.failure(ErrUnavailable)
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, s.failure(ErrUnavailable)
		}
		c, err := decodeCall(data)
		if err != nil {
			return nil, s.failure(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, s.failure(ErrUnavailable)
	}
	return out, nil
}

func (s *Store) callsLocked(q Query) ([]Call, error) {
	where, args, err := filters(q, false)
	if err != nil {
		return nil, err
	}
	return s.readCallsLocked("SELECT c.data FROM calls c JOIN (SELECT request_id FROM requests"+where+") r ON r.request_id=c.request_id WHERE c.scope='business' ORDER BY c.started_ns,c.call_id", args...)
}

func (s *Store) checkCallsLocked(q Query) ([]Call, error) {
	// A check has no business attribution. App/status/operation filters cannot
	// turn it into a matching user's request.
	if q.AppID != "" || q.Status != "" || q.Operation != "" && q.Operation != "generate" {
		return []Call{}, nil
	}
	query := "SELECT data FROM calls WHERE scope='check'"
	args := []any{}
	if !q.From.IsZero() {
		query += " AND started_ns>=?"
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		query += " AND started_ns<?"
		args = append(args, q.To.UnixNano())
	}
	return s.readCallsLocked(query+" ORDER BY started_ns,call_id", args...)
}

func (s *Store) pruneCalls(tx *sql.Tx, now time.Time) error {
	// Business observations live exactly as long as their retained parent.
	if _, err := tx.Exec("DELETE FROM calls WHERE scope='business' AND request_id NOT IN (SELECT request_id FROM requests)"); err != nil {
		return ErrUnavailable
	}
	if _, err := tx.Exec("DELETE FROM calls WHERE scope='check' AND started_ns<?", now.Add(-s.opts.MaxAge).UnixNano()); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) validateCalls() error {
	rows, err := s.db.Query("SELECT c.call_id,c.request_id,c.started_ns,c.scope,c.purpose,c.data,r.request_id FROM calls c LEFT JOIN requests r ON r.request_id=c.request_id ORDER BY c.started_ns,c.call_id")
	if err != nil {
		return ErrSchema
	}
	defer rows.Close()
	count, checks := 0, 0
	perRequest := map[string]int{}
	for rows.Next() {
		var id, requestID, scope, purpose, data string
		var started int64
		var parent sql.NullString
		if err := rows.Scan(&id, &requestID, &started, &scope, &purpose, &data, &parent); err != nil {
			return ErrCorrupt
		}
		c, err := decodeCall(data)
		if err != nil || c.CallID != id || c.RequestID != requestID || c.StartedAt.UnixNano() != started || c.Scope != scope || c.Purpose != purpose || scope == "business" && !parent.Valid {
			return ErrCorrupt
		}
		count++
		if scope == "check" {
			checks++
		} else {
			perRequest[requestID]++
			if perRequest[requestID] > 1000 {
				return ErrCorrupt
			}
		}
		if count > 100000 || checks > 10000 {
			return ErrCorrupt
		}
	}
	if rows.Err() != nil {
		return ErrCorrupt
	}
	return nil
}
