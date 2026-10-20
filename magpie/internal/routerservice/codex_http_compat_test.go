package routerservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

// The shapes are from Codex 0.161.0's isolated real CLI capture, including the
// item ID on tool output. No host field is stripped to make conversion pass.
func TestCodexHTTPNativeToolHistoryOrZeroOutbound(t *testing.T) {
	const history = `{"model":"profile-pinned","input":[{"role":"user","content":[{"type":"input_text","text":"Run pwd"}]},{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},{"type":"function_call_output","id":"fco_fixture","call_id":"call_fixture","output":"/synthetic/project"}],"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],"reasoning":{"summary":"auto"},"include":["reasoning.encrypted_content"],"client_metadata":{"session_id":"fixture","x-codex-turn-metadata":"{\"model\":\"fixture\"}"},"stream":false,"store":false}`
	for _, target := range []string{"responses", "chat"} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			var body map[string]any
			if err := json.Unmarshal([]byte(history), &body); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wire, _ := io.ReadAll(r.Body)
				var got map[string]any
				if json.Unmarshal(wire, &got) != nil || !reflect.DeepEqual(got, body) {
					t.Error("native host metadata or tool history changed")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, cloudHTTPReply(target, false))
			}))
			defer upstream.Close()
			h := cloudHTTPHandler(t)
			cloudHTTPBind(t, h, upstream.URL, target, "codex", "bound-provider-key")
			w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
			if target == "responses" {
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatalf("native Codex tool output ID rejected: status=%d calls=%d %s", w.Code, calls.Load(), w.Body.String())
				}
			} else if w.Code != 400 || calls.Load() != 0 {
				t.Fatalf("lossy Codex to Chat conversion allowed: status=%d calls=%d", w.Code, calls.Load())
			}
		})
	}
}

// The real Qwen response, echoed by Codex in turn two, includes both nulls.
// They express absent reasoning content and must survive native forwarding.
func TestCodexHTTPNullReasoningHistoryIsPreserved(t *testing.T) {
	var calls atomic.Int32
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("responses", false))
	}))
	defer upstream.Close()
	h := cloudHTTPHandler(t)
	cloudHTTPBind(t, h, upstream.URL, "responses", "codex", "bound-provider-key")
	body := map[string]any{"model": "profile-pinned", "input": []any{map[string]any{"type": "reasoning", "id": "rs_fixture", "summary": []any{}, "content": nil, "encrypted_content": nil}, map[string]any{"role": "user", "content": "hello"}}}
	w := cloudHTTPCall(t, h, context.Background(), "codex", "responses", body, "", "")
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("legal reasoning history rejected: status=%d calls=%d %s", w.Code, calls.Load(), w.Body.String())
	}
	encoded, _ := json.Marshal(body)
	var want map[string]any
	json.Unmarshal(encoded, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("native reasoning nulls changed")
	}
}
