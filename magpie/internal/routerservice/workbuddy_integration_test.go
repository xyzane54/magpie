package routerservice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
)

// These are explicit compatible Chat fixtures through the product HTTP stack.
// They are not captures from WorkBuddy or evidence of real host tool execution.
func workBuddySocketCall(t *testing.T, product *httptest.Server, app string, body any, auth string) (int, http.Header, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, product.URL+"/app/"+app+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:13145"
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := product.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

func TestWorkBuddyHTTPToolConversationKeepsApplicationAndProviderIsolation(t *testing.T) {
	var workBuddyCalls, otherCalls atomic.Int32
	const toolResult = "fixture read next.txt: retained contents"
	workBuddyCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := workBuddyCalls.Add(1)
		data, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer workbuddy-provider-fixture" || r.Header.Get("x-api-key") != "" || json.Unmarshal(data, &body) != nil || string(body["model"]) != `"profile-pinned"` {
			t.Error("WorkBuddy request escaped its exact path, provider or model")
		}
		for _, private := range []string{cloudHTTPManagement, "client-placeholder", "other-provider-fixture"} {
			if bytes.Contains(data, []byte(private)) || strings.Contains(r.Header.Get("Authorization"), private) {
				t.Error("WorkBuddy forwarded a credential from another boundary")
			}
		}
		if turn == 2 {
			for _, part := range []string{"call_new", "next.txt", toolResult, "tools"} {
				if !bytes.Contains(data, []byte(part)) {
					t.Errorf("second WorkBuddy turn dropped %s", part)
				}
			}
			var messages []map[string]any
			if json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 || !reflect.DeepEqual(messages[len(messages)-1]["toolResult"], map[string]any{"content": toolResult, "title": "Read next.txt", "renderer": map[string]any{"type": "text"}}) {
				t.Error("second WorkBuddy turn lost installed toolResult metadata")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", turn == 1))
	}))
	defer workBuddyCloud.Close()
	otherCloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer other-provider-fixture" {
			t.Error("other application used WorkBuddy's provider")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer otherCloud.Close()
	h := cloudHTTPHandler(t)
	cloudHTTPBind(t, h, workBuddyCloud.URL, "chat", "workbuddy", "workbuddy-provider-fixture")
	cloudHTTPBind(t, h, otherCloud.URL, "chat", "other-editor", "other-provider-fixture")
	product := httptest.NewServer(h)
	defer product.Close()
	body := cloudHTTPInput("chat", false, true)
	status, header, first := workBuddySocketCall(t, product, "workbuddy", body, "Bearer client-placeholder")
	if status != 200 || header.Get("X-Request-ID") == "" {
		t.Fatalf("WorkBuddy tool request failed: %d %s", status, first)
	}
	var reply struct {
		Choices []struct{ Message map[string]any }
	}
	if json.Unmarshal(first, &reply) != nil || len(reply.Choices) != 1 {
		t.Fatal("invalid first tool reply")
	}
	message := reply.Choices[0].Message
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_new" {
		t.Fatal("first reply lost tool call identity")
	}
	body["messages"] = append(body["messages"].([]any), message, map[string]any{"role": "tool", "tool_call_id": "call_new", "content": toolResult,
		"toolResult": map[string]any{"content": toolResult, "title": "Read next.txt", "renderer": map[string]any{"type": "text"}}})
	status, _, final := workBuddySocketCall(t, product, "workbuddy", body, "")
	if status != 200 || !bytes.Contains(final, []byte("hello")) {
		t.Fatalf("WorkBuddy second tool turn failed: %d %s", status, final)
	}
	status, _, other := workBuddySocketCall(t, product, "other-editor", cloudHTTPInput("chat", false, false), "")
	if status != 200 || !bytes.Contains(other, []byte("hello")) || workBuddyCalls.Load() != 2 || otherCalls.Load() != 1 {
		t.Fatal("application paths crossed their separately bound providers")
	}
	wrongModel := cloudHTTPInput("chat", false, false)
	wrongModel["model"] = "custom-local:profile-pinned"
	if status, _, _ := workBuddySocketCall(t, product, "workbuddy", wrongModel, ""); status != 400 {
		t.Fatal("UI selection prefix escaped onto the wire model")
	}
	if status, _, _ := workBuddySocketCall(t, product, "workbuddy", cloudHTTPInput("chat", false, false), "Bearer "+cloudHTTPManagement); status != 403 {
		t.Fatal("management token was accepted as a WorkBuddy business credential")
	}
	if workBuddyCalls.Load() != 2 || otherCalls.Load() != 1 {
		t.Fatal("rejected WorkBuddy request reached either provider")
	}
}

func TestWorkBuddyHTTPStreamingDisconnectCancelsSelectedProvider(t *testing.T) {
	var calls atomic.Int32
	canceled := make(chan struct{})
	complete := make(chan ModelCompletion, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") != "Bearer workbuddy-stream-fixture" || r.URL.Path != "/v1/chat/completions" {
			t.Error("stream used wrong provider or path")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial text\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	h := cloudHTTPObservedHandler(t, ModelOptions{Budget: 10 * time.Second, Complete: func(c ModelCompletion) { complete <- c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "workbuddy", "workbuddy-stream-fixture")
	product := httptest.NewServer(h)
	defer product.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(cloudHTTPInput("chat", true, false))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, product.URL+"/app/workbuddy/v1/chat/completions", bytes.NewReader(body))
	req.Host = "127.0.0.1:13145"
	req.Header.Set("Content-Type", "application/json")
	resp, err := product.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(line, "partial text") {
		t.Fatalf("WorkBuddy did not receive partial SSE text: %v %s", err, line)
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("WorkBuddy client cancellation did not reach selected upstream")
	}
	select {
	case done := <-complete:
		if done.AppID != "workbuddy" || done.Stage != "canceled" || done.ErrorCode != "request_canceled" || done.Result.Attempts != 1 || !done.Result.OutputCommitted || calls.Load() != 1 {
			t.Fatalf("stream cancellation state lost application identity or retried: %+v", done)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing WorkBuddy canceled completion")
	}
}

func TestWorkBuddyHTTPProviderErrorsStaySafeWithoutFallback(t *testing.T) {
	var calls, otherCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"workbuddy-error-fixture private.invalid tool arguments"}}`)
	}))
	defer upstream.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer other.Close()
	complete := make(chan ModelCompletion, 1)
	h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { complete <- c }})
	cloudHTTPBind(t, h, upstream.URL, "chat", "workbuddy", "workbuddy-error-fixture")
	cloudHTTPBind(t, h, other.URL, "chat", "unrelated-editor", "unrelated-provider-fixture")
	product := httptest.NewServer(h)
	defer product.Close()
	status, _, data := workBuddySocketCall(t, product, "workbuddy", cloudHTTPInput("chat", true, false), "")
	var done ModelCompletion
	select {
	case done = <-complete:
	case <-time.After(5 * time.Second):
		t.Fatal("missing WorkBuddy error completion")
	}
	if status != http.StatusUnauthorized || done.AppID != "workbuddy" || done.ErrorCode != "cloud_auth_error" || done.Result.Attempts != 1 || done.Result.OutputCommitted || calls.Load() != 1 || otherCalls.Load() != 0 {
		t.Fatalf("WorkBuddy provider error was retried or counted as success: %d %+v", status, done)
	}
	encoded, _ := json.Marshal(done)
	for _, private := range []string{"workbuddy-error-fixture", "private.invalid", "tool arguments", cloudHTTPManagement} {
		if bytes.Contains(data, []byte(private)) || bytes.Contains(encoded, []byte(private)) {
			t.Fatal("upstream private error entered output or observations")
		}
	}
}

func TestWorkBuddyHTTPCapturedAgentMetadataNativeOrZeroOutbound(t *testing.T) {
	for _, fixture := range []string{"workbuddy-5.3.8-real-task1.request.json", "workbuddy-5.3.8-real-tool-history.request.json", "workbuddy-5.3.8-real-completed-tools.request.json", "workbuddy-5.3.8-real-cancel.request.json"} {
		t.Run(fixture, func(t *testing.T) { workBuddyHTTPReplayCapture(t, fixture) })
	}
}

func TestWorkBuddyHTTPWebSearchResultContinuation(t *testing.T) {
	// Reduced history reconstructed from the failed session by the installed
	// WorkBuddy serializer, with business and identity values sanitized.
	workBuddyHTTPReplayCapture(t, "workbuddy-websearch-tool-result.request.json")
}

func TestWorkBuddyHTTPWebSearchHistoryWithRoutingEnabled(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "routercloud", "testdata", "workbuddy", "workbuddy-websearch-tool-result.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, judgmentFails := range []bool{false, true} {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			wire, _ := io.ReadAll(r.Body)
			var got, want map[string]any
			_ = json.Unmarshal(wire, &got)
			_ = json.Unmarshal(body, &want)
			want["model"] = "profile-pinned"
			if !reflect.DeepEqual(got, want) {
				t.Error("routed WebSearch continuation changed native history or metadata")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, cloudHTTPReply("chat", false))
		}))
		judge := &scriptedJudge{decision: DecisionCloud, views: make(chan routerrequest.JudgmentView, 1)}
		if judgmentFails {
			judge.err = ErrJudgmentFailed
		}
		h := cloudHTTPObservedHandler(t, ModelOptions{Routing: &RoutingOptions{Judge: judge}})
		cloudHTTPBind(t, h, upstream.URL, "chat", "workbuddy", "workbuddy-routed-fixture")
		product := httptest.NewServer(h)
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		request["model"] = "profile-pinned"
		status, _, reply := workBuddySocketCall(t, product, "workbuddy", request, "")
		product.Close()
		upstream.Close()
		if status != 200 || calls.Load() != 1 || judge.calls.Load() != 1 || !bytes.Contains(reply, []byte("[DONE]")) {
			t.Fatalf("WebSearch routed continuation failed (judgment failure=%v): status=%d cloud=%d judgment=%d", judgmentFails, status, calls.Load(), judge.calls.Load())
		}
		view := <-judge.views
		if len(view.History) != 3 || view.History[2].ToolResult == nil || view.History[2].ToolResult.Text != "fixture-text-12" {
			t.Fatal("routed tool content lost before judgment")
		}
	}
}

func workBuddyHTTPReplayCapture(t *testing.T, fixture string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "routercloud", "testdata", "workbuddy", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var capture map[string]any
	if json.Unmarshal(body, &capture) != nil {
		t.Fatal("invalid full sanitized WorkBuddy capture")
	}
	model, ok := capture["model"].(string)
	if !ok || model == "" {
		t.Fatal("capture is missing its actual wire model")
	}
	for _, target := range []string{"chat", "responses", "anthropic"} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				data, _ := io.ReadAll(r.Body)
				var got map[string]any
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer workbuddy-capture-fixture" || json.Unmarshal(data, &got) != nil || !reflect.DeepEqual(got, capture) {
					t.Error("native product forwarding changed complete WorkBuddy request values, identity, tools or controls")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, strings.ReplaceAll(cloudHTTPReply("chat", false), "profile-pinned", model))
			}))
			defer upstream.Close()
			complete := make(chan ModelCompletion, 1)
			h := cloudHTTPObservedHandler(t, ModelOptions{Complete: func(c ModelCompletion) { complete <- c }})
			draft := providerInputFor(upstream.URL + "/v1")
			key := "workbuddy-capture-fixture"
			draft.Protocol, draft.Model, draft.APIKey = target, "", &key
			provider := providerSaved(t, providerRequest(t, h, http.MethodPost, "/api/cloud-providers", cloudHTTPManagement, draft))
			bound := providerRequest(t, h, http.MethodPost, "/api/apps", cloudHTTPManagement, routerconfig.AppEdit{
				ID: "workbuddy", Name: "workbuddy", CredentialMode: routerconfig.StoredCredential,
				Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: model},
			})
			if bound.Code != 200 {
				t.Fatal("capture Profile could not be bound")
			}
			product := httptest.NewServer(h)
			defer product.Close()
			status, header, data := workBuddySocketCall(t, product, "workbuddy", json.RawMessage(body), "Bearer client-placeholder")
			var done ModelCompletion
			select {
			case done = <-complete:
			case <-time.After(5 * time.Second):
				t.Fatal("missing captured WorkBuddy request completion")
			}
			if done.AppID != "workbuddy" || header.Get("X-Request-ID") == "" {
				t.Fatal("captured request lost its WorkBuddy application identity")
			}
			if target == "chat" {
				if status != 200 || calls.Load() != 1 || done.ErrorCode != "" || done.Result.Attempts != 1 || !done.Result.OutputCommitted || !strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") || !bytes.Contains(data, []byte("[DONE]")) {
					t.Fatalf("full native WorkBuddy capture failed: status=%d calls=%d completion=%+v", status, calls.Load(), done)
				}
			} else if status != 400 || calls.Load() != 0 || done.ErrorCode != "capability_message_agent" || done.Result.Attempts != 0 || done.Result.OutputCommitted || bytes.Contains(data, []byte("[DONE]")) {
				t.Fatalf("captured WorkBuddy identity was silently translated: status=%d calls=%d completion=%+v", status, calls.Load(), done)
			}
		})
	}
}
