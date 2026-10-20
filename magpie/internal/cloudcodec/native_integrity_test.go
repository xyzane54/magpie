package cloudcodec

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func reviewStream(events ...any) string {
	var s strings.Builder
	for _, v := range events {
		s.WriteString("data: " + CloudJSON(v) + "\n\n")
	}
	return s.String()
}
func TestNativeResponsesRejectContradictoryOutput(t *testing.T) {
	created := map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_review", "model": "fixture", "object": "response"}}
	item := func(id, name, args string) map[string]any {
		return map[string]any{"type": "function_call", "id": id, "call_id": "call_" + name, "name": name, "arguments": args, "status": "completed"}
	}
	done := func(index int, it any) any {
		return map[string]any{"type": "response.output_item.done", "output_index": index, "item": it}
	}
	terminal := func(items ...any) any {
		return map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_review", "model": "fixture", "object": "response", "status": "completed", "output": items}}
	}
	cases := map[string]string{
		"duplicate-id-different-index": reviewStream(created, done(0, item("same", "a", "{}")), done(1, item("same", "b", "{}")), terminal()),
		"done-terminal-conflict":       reviewStream(created, done(0, item("fc_a", "safe", "{}")), terminal(item("fc_a", "different", "{}"))),
		"malformed-function-json":      reviewStream(created, done(0, item("fc_a", "a", "not-json")), terminal(item("fc_a", "a", "not-json"))),
		"changed-id-same-index":        reviewStream(created, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item("fc_old", "a", "{}")}, done(0, item("fc_new", "b", "{}")), terminal()),
		"duplicate-done-same-index":    reviewStream(created, done(0, item("fc_a", "a", "{}")), done(0, item("fc_b", "b", "{}")), terminal()),
		"wrong-delta-item-id":          reviewStream(created, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item("fc_a", "a", "")}, map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_unrelated", "output_index": 0, "arguments": "{}"}, terminal()),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				c, _ := NewCloudCodec("responses", Raw(map[string]any{"input": "fixture", "stream": stream}))
				w := httptest.NewRecorder()
				r, e := c.Reply(w, "responses", "text/event-stream", strings.NewReader(body))
				if e == nil {
					t.Fatalf("contradictory native stream succeeded: %s", w.Body.String())
				}
				if !stream && (r.OutputCommitted || w.Body.Len() != 0) {
					t.Fatal("invalid JSON response committed")
				}
				if bytes.Contains(w.Body.Bytes(), []byte(`"type":"response.completed"`)) {
					t.Fatal("invalid stream emitted success terminal")
				}
			}
		})
	}
}
