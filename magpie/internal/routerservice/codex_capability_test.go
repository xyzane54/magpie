package routerservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodexHTTPCapabilityErrorsBeforeOutbound(t *testing.T) {
	cases := []struct {
		name, code string
		body       map[string]any
	}{
		{"namespace", "capability_namespace", map[string]any{"input": "hello", "tools": []any{map[string]any{"type": "namespace", "name": "multi_agent_v1", "description": "agents", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object"}}}}}}},
		{"custom", "capability_custom", map[string]any{"input": "hello", "tools": []any{map[string]any{"type": "custom", "name": "freeform", "format": map[string]any{"type": "text"}}}}},
		{"search", "capability_web_search", map[string]any{"input": "hello", "tools": []any{map[string]any{"type": "web_search"}}}},
		{"result-citation", "capability_web_search", map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "fixture", "output": []any{map[string]any{"type": "output_text", "text": "source", "annotations": []any{map[string]any{"type": "url_citation", "url": "https://example.com", "title": "source", "start_index": 0, "end_index": 6}}}}}}}},
		{"additional", "capability_additional_tools", map[string]any{"input": []any{map[string]any{"type": "additional_tools", "id": "tools_fixture", "role": "developer", "tools": []any{map[string]any{"type": "function", "name": "run", "parameters": map[string]any{"type": "object"}}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
			tc.body["model"] = "profile-pinned"
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", tc.body, "", "")
			if w.Code != 400 || calls.Load() != 0 || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("specific capability error absent: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		name, code string
		body       map[string]any
	}{
		{"reasoning", "capability_reasoning_configuration", map[string]any{"input": "hello", "reasoning": map[string]any{"summary": "auto"}}},
		{"opaque", "capability_reasoning_history", map[string]any{"input": []any{map[string]any{"type": "reasoning", "id": "rs_fixture", "summary": []any{}, "encrypted_content": "opaque"}, map[string]any{"role": "user", "content": "hello"}}}},
		{"metadata", "capability_metadata", map[string]any{"input": "hello", "client_metadata": map[string]any{"session_id": "fixture"}}},
		{"phase", "capability_message_phase", map[string]any{"input": []any{map[string]any{"role": "assistant", "content": "still working", "phase": "commentary"}, map[string]any{"role": "user", "content": "continue"}}}},
	} {
		t.Run("chat/"+tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, "chat", "codex", "bound-provider-key")
			tc.body["model"] = "profile-pinned"
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", tc.body, "", "")
			if w.Code != 400 || calls.Load() != 0 || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("lossy target error not actionable: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
}

func TestCodexHTTPVersionedSearchAndToolChoiceRequireCapabilities(t *testing.T) {
	for _, kind := range []string{"web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26"} {
		for _, location := range []string{"tools", "tool_choice"} {
			t.Run(kind+"/"+location, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer upstream.Close()
				h := cloudHTTPHandler(t)
				cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
				tool := map[string]any{"type": kind}
				body := map[string]any{"model": "profile-pinned", "input": "hello", location: tool}
				if location == "tools" {
					body[location] = []any{tool}
				}
				w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
				if w.Code != 400 || calls.Load() != 0 || !strings.Contains(w.Body.String(), "capability_web_search") {
					t.Fatalf("versioned/forced search bypassed capability: %d calls=%d %s", w.Code, calls.Load(), w.Body.String())
				}
			})
		}
	}
}

func TestCodexHTTPPreviewDoesNotSilentlyIgnoreSearchRestrictions(t *testing.T) {
	for field, value := range map[string]any{"external_web_access": false, "filters": map[string]any{"allowed_domains": []string{"example.com"}}, "return_token_budget": "unlimited"} {
		t.Run(field, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
			codexHTTPAuthorize(t, h, []string{"web_search"})
			body := map[string]any{"model": "profile-pinned", "input": "hello", "tools": []any{map[string]any{"type": "web_search_preview", field: value}}}
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
			if w.Code != 400 || calls.Load() != 0 {
				t.Fatalf("preview ignored a search restriction: %d calls=%d", w.Code, calls.Load())
			}
		})
	}
}
