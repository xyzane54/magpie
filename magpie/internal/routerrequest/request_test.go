package routerrequest

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

func TestCanonicalPathsAndEncodingRejection(t *testing.T) {
	for _, id := range []string{"codex", "claude-code", "loomy", "astudio", "workbuddy", "dsh", "custom-editor"} {
		for _, sub := range []string{"", "/models", "/responses", "/messages", "/messages/count_tokens", "/chat/completions"} {
			r := httptest.NewRequest("POST", "http://localhost/app/"+id+"/v1"+sub, nil)
			got, path, err := ParseAppPath(r)
			if err != nil || got != id || path != strings.TrimPrefix(sub, "/") {
				t.Fatalf("%s: %q %q %v", r.URL.Path, got, path, err)
			}
		}
	}
	for _, path := range []string{"/app/codex", "/app/codex/v2", "/app/Codex/v1", "/app/admin/v1", "/app/codex/v1/", "/app/codex/v1//messages", "/app/codex/v1/./messages", "/app/codex/v1/../messages", "/app/%63odex/v1", "/app/codex%2Floomy/v1", "/app/codex%252floomy/v1", "/%61pp/codex/v1", "/app/codex/v1/%72esponses", "/app/codex/v1/responses%3fmodel=x", "/app/codex/v1/responses\\x", "/app/codex/v1/v1/messages"} {
		r := httptest.NewRequest("POST", "http://localhost"+path, nil)
		if _, _, err := ParseAppPath(r); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
func TestRequestBudgetCommitAndSafeMetadata(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost/app/codex/v1/responses", nil)
	r.Header.Set("X-Request-ID", "host-controlled")
	r, err := WithRequestID(r)
	if err != nil {
		t.Fatal(err)
	}
	id := RequestID(r.Context())
	if len(id) != 32 || id == "host-controlled" {
		t.Fatal("untrusted request ID")
	}
	again, err := WithRequestID(r)
	if err != nil || RequestID(again.Context()) != id {
		t.Fatal("correlation changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &State{ID: id, AppID: "codex", Deadline: time.Now().Add(time.Minute), Context: ctx, Target: routerconfig.AppTarget{Provider: routerconfig.ResolvedProvider{APIKey: "secret-cloud-value"}, Authorization: "Bearer secret-forward-value"}}
	b, _ := json.Marshal(state)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("secret in metadata")
	}
	state.Deadline = time.Now().Add(-time.Second)
	if state.Remaining() != 0 || ClassifyError(state.BeginCloudAttempt()).Code != "budget_exhausted" {
		t.Fatal("exhausted budget attempted cloud")
	}
	if state.Canceled() {
		t.Fatal("deadline incorrectly counted as cancel")
	}
	cancel()
	if !state.Canceled() {
		t.Fatal("cancel state not observable")
	}
}
