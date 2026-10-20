package cloudcodec

import (
	"bytes"
	"encoding/json"
	"strings"
)

// upstreamStop is why a reply (a JSON body, or one event of its stream)
// says it ended, in the upstream's own words: an Anthropic stop_reason, a
// Chat finish_reason, a Responses reply's status and the reason it was
// incomplete. Kept with the request, so a reply that ended too soon shows
// what the upstream said of it — a relay's "end_turn" after a few words,
// or nothing at all (蓝猫 on Discord). "" when this one says none.
func UpstreamStop(b []byte) string {
	if !bytes.Contains(b, []byte(`stop_reason"`)) && !bytes.Contains(b, []byte(`finish_reason"`)) && !bytes.Contains(b, []byte(`"response.`)) {
		return ""
	}
	var v struct {
		Type       string `json:"type"`
		StopReason string `json:"stop_reason"`
		Delta      struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Response struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	switch {
	case v.Delta.StopReason != "":
		return v.Delta.StopReason
	case v.StopReason != "":
		return v.StopReason
	case strings.HasPrefix(v.Type, "response.") && v.Response.Status != "" && v.Response.Status != "in_progress" && v.Response.Status != "queued":
		if d := v.Response.IncompleteDetails; d != nil && d.Reason != "" {
			return v.Response.Status + ": " + d.Reason
		}
		return v.Response.Status
	}
	for _, c := range v.Choices {
		if c.FinishReason != "" {
			return c.FinishReason
		}
	}
	return ""
}

// eventType is an event's own "type", "" when it names none.
func EventType(data string) string {
	var v struct {
		Type json.RawMessage `json:"type"`
	}
	var t string
	if json.Unmarshal([]byte(data), &v) != nil || json.Unmarshal(v.Type, &t) != nil {
		return ""
	}
	return t
}

// carriedError is the error an upstream's event carries where its
// protocol's decoder can't read it: an error that is a string,
// {"type":"error","error":"…"} (#1933), or one whose other fields aren't
// the shape the protocol gives them. Read as nothing, the stream went on
// to end as a whole reply with nothing in it, and the agent didn't try
// again. ok is false for an event that carries no error.
func CarriedError(data string) (ev Event, ok bool) {
	var v struct {
		Type     json.RawMessage `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return Event{}, false
	}
	var t string
	json.Unmarshal(v.Type, &t)
	var r struct {
		Error json.RawMessage `json:"error"`
	}
	json.Unmarshal(v.Response, &r)
	if t != "error" && t != "response.failed" && !Carries(v.Error) && !Carries(r.Error) {
		return Event{}, false
	}
	return Event{Kind: KError, Text: StreamErrorText(data, r.Error, v.Error), Code: RefusedCode(data)}, true
}

// streamErrorText is what an upstream's error event says: the message of
// the first of errs that has one (an object's message or type, or a
// string), else the event as provider.APIError reads it. Never "": an
// error said with no words is still told as one.
func StreamErrorText(data string, errs ...json.RawMessage) string {
	for _, raw := range errs {
		if !Carries(raw) {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			continue
		}
		var e struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(raw, &e) == nil {
			if m := strings.TrimSpace(e.Message); m != "" {
				return m
			}
			if e.Type != "" {
				return e.Type
			}
		}
	}
	return APIError([]byte(data), "the upstream reported an error")
}

// carries says whether a JSON value is there and not null.
func Carries(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && string(s) != "null"
}
