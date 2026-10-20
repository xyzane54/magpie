package routercloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestLoomyAgentCommentNativeOrExplicitConversionBoundary(t *testing.T) {
	body := []byte(`{"$comment":"fixture Loomy agent metadata","messages":[{"role":"user","content":"hello"}],"stream":false}`)
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
					t.Error("native Chat changed Loomy request metadata")
				}
				return response(200, whole(target, false)), nil
			})}
			w := httptest.NewRecorder()
			result, err := e.Execute(context.Background(), w, Request{"chat", body, configured("http://fixture.invalid/v1", target)})
			if target == "chat" {
				if err != nil || calls != 1 || result.Attempts != 1 {
					t.Fatalf("installed agent comment rejected: calls=%d error=%v", calls, err)
				}
			} else {
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_request_comment" || calls != 0 || result.Attempts != 0 || w.Body.Len() != 0 {
					t.Fatalf("agent comment lost across protocol: calls=%d result=%+v error=%v", calls, result, err)
				}
			}
		})
	}
}

func TestLoomyAgentCommentWrongTypesAndDuplicatesStayRejected(t *testing.T) {
	for _, value := range []string{`null`, `42`, `true`, `{}`, `[]`} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"$comment":`+value+`,"messages":[{"role":"user","content":"hello"}]}`))
	}
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"$comment":"a","$comment":"b","messages":[{"role":"user","content":"hello"}]}`))
	workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(`{"$comment":"verified type","unknown":true,"messages":[{"role":"user","content":"hello"}]}`))
}

func TestSchemaCommentRemainsOpaqueBusinessData(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"$comment is business text"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","$comment":"schema note","properties":{"$comment":{"type":"string"}}}}}]}`)
	if err := ValidateInput("chat", body); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"chat", "responses", "anthropic"} {
		calls := 0
		e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, whole(target, false)), nil })}
		_, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", body, configured("http://fixture.invalid/v1", target)})
		if err != nil || calls != 1 {
			t.Fatalf("schema/business comment misclassified for %s: %v", target, err)
		}
	}
}
