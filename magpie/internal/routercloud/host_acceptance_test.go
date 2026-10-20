package routercloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// These are literal, sanitized CLI captures from the pinned executables. Native
// execution must retain every JSON value except the application-selected model;
// a different target protocol must fail before opening any outbound connection.
func TestPinnedHostRequestsNativeOrZeroOutbound(t *testing.T) {
	var requests []struct {
		name, protocol string
		body           json.RawMessage
	}
	for i := 1; i <= 4; i++ {
		path := filepath.Join("testdata", "host-requests", "codex-0.161.0", "direct-tools", fmt.Sprintf("request-%d.json", i))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var capture struct {
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(body, &capture); err != nil || len(capture.Body) == 0 {
			t.Fatalf("invalid pinned Codex capture: %v", err)
		}
		requests = append(requests, struct {
			name, protocol string
			body           json.RawMessage
		}{fmt.Sprintf("codex-0.161.0-turn-%d", i), "responses", capture.Body})
	}
	for _, scenario := range []string{"claude-code-2.1.81", "claude-code-2.1.81-signed-product", "claude-code-2.1.81-restricted"} {
		var claude []struct {
			Body json.RawMessage `json:"body"`
		}
		body, err := os.ReadFile(filepath.Join("testdata", "host-requests", scenario, "requests.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &claude); err != nil || len(claude) != 4 {
			t.Fatalf("invalid pinned Claude capture: %v", err)
		}
		for i, capture := range claude {
			requests = append(requests, struct {
				name, protocol string
				body           json.RawMessage
			}{fmt.Sprintf("%s-turn-%d", scenario, i+1), "anthropic", capture.Body})
		}
	}
	for _, capture := range requests {
		for _, target := range []string{"chat", "responses", "anthropic"} {
			t.Run(capture.name+"-to-"+target, func(t *testing.T) {
				calls := 0
				executor := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					wire, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					var got, want map[string]any
					if json.Unmarshal(wire, &got) != nil || json.Unmarshal(capture.body, &want) != nil {
						t.Fatal("invalid capture or native request")
					}
					want["model"] = "pinned"
					if !reflect.DeepEqual(got, want) {
						t.Error("native forwarding changed host instructions, tools, history or metadata")
					}
					return response(200, whole(target, true)), nil
				})}
				w := httptest.NewRecorder()
				result, err := executor.Execute(context.Background(), w, Request{capture.protocol, capture.body, configured("http://fixture.invalid/v1", target)})
				if target == capture.protocol {
					if err != nil || calls != 1 || result.Attempts != 1 || !result.OutputCommitted {
						t.Fatalf("native pinned host rejected: calls=%d error=%v", calls, err)
					}
				} else if err == nil || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
					t.Fatalf("lossy pinned host conversion escaped zero-outbound boundary: calls=%d error=%v", calls, err)
				}
			})
		}
	}
}

func TestPinnedCodexUndeclaredDefaultAndCustomToolsRejectBeforeOutbound(t *testing.T) {
	for _, scenario := range []string{"direct-default", "direct-custom"} {
		t.Run(scenario, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", "host-requests", "codex-0.161.0", scenario, "request-1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Body json.RawMessage `json:"body"`
			}
			if err := json.Unmarshal(body, &capture); err != nil || len(capture.Body) == 0 {
				t.Fatalf("invalid pinned Codex capture: %v", err)
			}
			for _, protocol := range []string{"responses", "chat", "anthropic"} {
				calls := 0
				executor := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response(200, whole(protocol, true)), nil
				})}
				w := httptest.NewRecorder()
				result, err := executor.Execute(context.Background(), w, Request{"responses", capture.Body, configured("http://fixture.invalid/v1", protocol)})
				failure, ok := err.(*Error)
				if !ok || failure.Code != "capability_namespace" || calls != 0 || result.Attempts != 0 || result.OutputCommitted || w.Body.Len() != 0 {
					t.Fatalf("undeclared native or lossy translated namespace capture escaped capability boundary: calls=%d error=%v", calls, err)
				}
			}
		})
	}
}
