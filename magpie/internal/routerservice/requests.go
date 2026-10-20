package routerservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
)

// Only canonical management paths enter the existing local/authentication guard.
func requestAPIPath(r *http.Request) bool {
	p := r.URL.EscapedPath()
	return p == r.URL.Path && (p == "/api/requests" || strings.HasPrefix(p, "/api/requests/"))
}

func requestQuery(r *http.Request, summary bool) (routerledger.Query, error) {
	q := routerledger.Query{}
	if len(r.URL.RawQuery) > 2048 {
		return q, routerledger.ErrInvalid
	}
	// ParseQuery reports invalid escaping, whereas URL.Query silently drops it.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, routerledger.ErrInvalid
	}
	for key, v := range values {
		if len(v) != 1 || len(v[0]) > 256 {
			return q, routerledger.ErrInvalid
		}
		switch key {
		case "limit":
			if summary {
				return q, routerledger.ErrInvalid
			}
			q.Limit, err = strconv.Atoi(v[0])
			if err != nil || q.Limit < 1 || q.Limit > 100 {
				return q, routerledger.ErrInvalid
			}
		case "before":
			if summary {
				return q, routerledger.ErrInvalid
			}
			q.Before = v[0]
		case "appId":
			if !routerconfig.ValidAppID(v[0]) {
				return q, routerledger.ErrInvalid
			}
			q.AppID = v[0]
		case "status":
			switch v[0] {
			case "processing", "succeeded", "failed", "canceled", "timed_out", "interrupted":
				q.Status = v[0]
			default:
				return q, routerledger.ErrInvalid
			}
		case "operation":
			if v[0] != "generate" && v[0] != "count_tokens" {
				return q, routerledger.ErrInvalid
			}
			q.Operation = v[0]
		case "from", "to":
			t, e := time.Parse(time.RFC3339Nano, v[0])
			if e != nil || t.Year() < 2000 || t.Year() > 2200 {
				return q, routerledger.ErrInvalid
			}
			if key == "from" {
				q.From = t
			} else {
				q.To = t
			}
		default:
			return q, routerledger.ErrInvalid
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return q, routerledger.ErrInvalid
	}
	return q, nil
}

// A bounded encoding step keeps accidental schema growth from making an
// unbounded management response. Records contain only the ledger projection.
func requestJSON(w http.ResponseWriter, r *http.Request, code int, value any) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(value); err != nil || b.Len() > 1<<20 {
		providerJSON(w, 503, map[string]any{"error": map[string]string{"code": "ledger_response_unavailable", "message": "request ledger response unavailable"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Bytes())
	}
}

func requestsAPI(w http.ResponseWriter, r *http.Request, ledger *routerledger.Store, unavailableCode string) {
	status := routerledger.Status{SchemaVersion: routerledger.SchemaVersion, ErrorCode: "ledger_unavailable"}
	if unavailableCode == "ledger_corrupt" || unavailableCode == "ledger_schema_incompatible" {
		status.ErrorCode = unavailableCode
	}
	if ledger != nil {
		status = ledger.Status()
	}
	fail := func(code int, name, message string) {
		if ledger != nil {
			status = ledger.Status()
		}
		requestJSON(w, r, code, map[string]any{"schemaVersion": 1, "ledger": status, "error": map[string]string{"code": name, "message": message}})
	}
	p := r.URL.Path
	if p != "/api/requests" && p != "/api/requests/summary" && !validRequestID(strings.TrimPrefix(p, "/api/requests/")) {
		fail(404, "request_not_found", "request record not found")
		return
	}
	if ledger == nil || !status.Available {
		fail(503, "ledger_unavailable", "request ledger unavailable")
		return
	}
	if p != "/api/requests" && p != "/api/requests/summary" {
		if r.URL.RawQuery != "" {
			fail(400, "invalid_query", "invalid request query")
			return
		}
		record, err := ledger.Get(strings.TrimPrefix(p, "/api/requests/"))
		if errors.Is(err, routerledger.ErrNotFound) {
			fail(404, "request_not_found", "request record not found")
			return
		}
		if err != nil {
			fail(503, "ledger_unavailable", "request ledger unavailable")
			return
		}
		calls, err := ledger.Calls(record.RequestID)
		if err != nil {
			fail(503, "ledger_unavailable", "request ledger unavailable")
			return
		}
		requestJSON(w, r, 200, map[string]any{"schemaVersion": 1, "request": record, "calls": calls, "ledger": ledger.Status()})
		return
	}
	q, err := requestQuery(r, p == "/api/requests/summary")
	if err != nil {
		fail(400, "invalid_query", "invalid request query")
		return
	}
	if p == "/api/requests/summary" {
		summary, err := ledger.Summary(q)
		if errors.Is(err, routerledger.ErrInvalid) {
			fail(400, "invalid_query", "invalid request query")
			return
		}
		if err != nil {
			fail(503, "ledger_unavailable", "request ledger unavailable")
			return
		}
		requestJSON(w, r, 200, map[string]any{"schemaVersion": 1, "summary": summary, "ledger": ledger.Status()})
		return
	}
	page, err := ledger.List(q)
	if errors.Is(err, routerledger.ErrInvalid) {
		fail(400, "invalid_query", "invalid request query")
		return
	}
	if err != nil {
		fail(503, "ledger_unavailable", "request ledger unavailable")
		return
	}
	requestJSON(w, r, 200, map[string]any{"schemaVersion": 1, "requests": page.Records, "total": page.Total, "nextCursor": page.NextCursor, "ledger": ledger.Status()})
}

func validRequestID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
