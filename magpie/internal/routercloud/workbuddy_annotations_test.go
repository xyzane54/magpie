package routercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkBuddyAssistantEmptyAnnotationsNativeOrZeroOutbound(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"fixture"},{"role":"assistant","content":[{"type":"text","text":"I updated the fixture.","annotations":[]}]}]}`)
	for _, target := range []string{"chat", "responses", "anthropic"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wire, _ := io.ReadAll(r.Body)
				var got, want map[string]any
				_ = json.Unmarshal(wire, &got)
				_ = json.Unmarshal(body, &want)
				want["model"] = "pinned"
				if !reflect.DeepEqual(got, want) {
					t.Error("native Chat lost typed text annotations")
				}
				return response(200, whole(target, false)), nil
			})}
			w := httptest.NewRecorder()
			result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			if target == "chat" {
				if err != nil || calls != 1 || result.Attempts != 1 {
					t.Fatalf("installed assistant text block rejected: calls=%d err=%v", calls, err)
				}
			} else {
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_content_annotations" || calls != 0 || result.Attempts != 0 || w.Body.Len() != 0 {
					t.Fatalf("annotations lost crossing protocol: calls=%d result=%+v err=%v", calls, result, err)
				}
			}
		})
	}
}

func TestWorkBuddyAssistantAnnotationsStayStrict(t *testing.T) {
	for _, role := range []string{"system", "developer", "user", "tool"} {
		m := map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": "fixture", "annotations": []any{}}}}
		if role == "tool" {
			m["tool_call_id"] = "call_a"
		}
		body, _ := json.Marshal(map[string]any{"messages": []any{m}})
		workBuddyAssertInvalidMetadataZeroOutbound(t, body)
	}
	for _, annotations := range []string{`null`, `{}`, `""`, `[{}]`, `["citation"]`} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"fixture","annotations":`+annotations+`}]}]}`))
	}
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"fixture","annotations":[],"annotations":[]}]}]}`))
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"fixture","annotations":[],"unknown":true}]}]}`))
}

func TestWorkBuddyCapturedAssistantAnnotationsNativeOrZeroOutbound(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "workbuddy", "workbuddy-assistant-annotations.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"chat", "responses", "anthropic"} {
		calls := 0
		e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			wire, _ := io.ReadAll(r.Body)
			var got, want map[string]any
			_ = json.Unmarshal(wire, &got)
			_ = json.Unmarshal(body, &want)
			want["model"] = "pinned"
			if !reflect.DeepEqual(got, want) {
				t.Error("complete captured history changed")
			}
			return response(200, whole(target, false)), nil
		})}
		w := httptest.NewRecorder()
		result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
		if target == "chat" {
			if err != nil || calls != 1 {
				t.Fatalf("capture rejected: %v", err)
			}
		} else {
			failure, ok := err.(*Error)
			if !ok || failure.Code != "capability_message_agent" || calls != 0 || result.Attempts != 0 {
				t.Fatalf("capture conversion boundary changed: %v", err)
			}
		}
	}
}
