package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

// r1ReviewRedactor is deliberately a tiny deterministic seam. It models W5's
// prepared-body contract without introducing a test-only bypass or an entity
// extraction implementation.
type r1ReviewRedactor func(context.Context, RedactionRequest) (PreparedCloudRequest, error)

func (f r1ReviewRedactor) Prepare(ctx context.Context, req RedactionRequest) (PreparedCloudRequest, error) {
	return f(ctx, req)
}

func TestR1RedactionCoversProviderExtraBody(t *testing.T) {
	const secret = "synthetic-review-account"
	var outgoing []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outgoing, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()

	body := []byte(`{"messages":[{"role":"user","content":"synthetic-review-account"}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, true, body)
	defer cancel()
	state, ok := routerrequest.FromContext(prepared.Context())
	if !ok {
		t.Fatal("missing prepared request state")
	}
	state.Target.Provider.ExtraBody = map[string]json.RawMessage{
		"metadata": json.RawMessage(`{"owner":"synthetic-review-account"}`),
	}
	redactor := r1ReviewRedactor(func(_ context.Context, req RedactionRequest) (PreparedCloudRequest, error) {
		return PreparedCloudRequest{
			Body: bytes.ReplaceAll(req.Body, []byte(secret), []byte("[ACCOUNT_1]")),
		}, nil
	})
	w := httptest.NewRecorder()
	_, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Redactor: redactor}}, nil, modelWriterForTest(w))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(outgoing, []byte(secret)) {
		t.Fatalf("raw synthetic entity reached loopback upstream after redaction: %s", outgoing)
	}
}

func TestR2DisabledJudgmentDoesNotFallback(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	prepared, cancel, _ := routedRequest(t, context.Background(), upstream.URL, false, body)
	defer cancel()
	w := httptest.NewRecorder()
	_, err := executeRoutedModel(w, prepared, "chat", body, ModelOptions{Routing: &RoutingOptions{Judge: &scriptedJudge{err: routerruntime.ErrDisabled}}}, nil, modelWriterForTest(w))
	if calls.Load() != 0 || !errors.Is(err, routerruntime.ErrDisabled) {
		t.Fatalf("disabled judgment fell back: cloud=%d err=%v", calls.Load(), err)
	}
}
