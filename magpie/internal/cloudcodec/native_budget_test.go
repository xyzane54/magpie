package cloudcodec

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeResponsesJSONExpansionIsBoundedBeforeOutput(t *testing.T) {
	// A valid snapshot fits the input limit but its delta/part/item/terminal
	// events exceed the wire budget. The JSON client can still receive it.
	body := Raw(map[string]any{
		"id": "resp_budget", "object": "response", "status": "completed",
		"output": []any{map[string]any{
			"id": "msg_budget", "type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": strings.Repeat("x", 7<<20), "annotations": []any{}}},
		}},
	})
	if len(body) >= CloudReplyLimit {
		t.Fatal("fixture must fit the JSON input budget")
	}
	codec, _ := NewCloudCodec("responses", []byte(`{"input":"fixture","stream":true}`))
	w := httptest.NewRecorder()
	reply, err := codec.Reply(w, "responses", "application/json", strings.NewReader(string(body)))
	if err == nil || reply.OutputCommitted || w.Body.Len() != 0 {
		t.Fatalf("expanded stream exceeded budget: err=%v committed=%v outputBytes=%d", err, reply.OutputCommitted, w.Body.Len())
	}
	codec, _ = NewCloudCodec("responses", []byte(`{"input":"fixture","stream":false}`))
	w = httptest.NewRecorder()
	if _, err := codec.Reply(w, "responses", "application/json", strings.NewReader(string(body))); err != nil || w.Body.String() != string(body) {
		t.Fatal("valid native JSON snapshot was changed or rejected", err)
	}
}

func TestResponsesWholeEventsStopOnConsumerFailure(t *testing.T) {
	stopped := errors.New("consumer stopped")
	calls := 0
	err := ResponsesWholeEventsEach([]byte(enhancedNativeReply), func(event WholeEvent) error {
		calls++
		return stopped
	})
	if !errors.Is(err, stopped) || calls != 1 {
		t.Fatalf("event producer continued after consumer failure: calls=%d err=%v", calls, err)
	}
}
