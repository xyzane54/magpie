package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

func TestLoomyCredentialStoredCheckAndExplicitSessionBoundary(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer exact-bound-key" || r.Header.Get("x-api-key") != "" {
			t.Error("management or host credential replaced the bound provider key")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cloudHTTPReply("chat", false))
	}))
	defer upstream.Close()
	h, store, draft, configPath := checkFixture(t, upstream.URL, "chat", ModelOptions{})
	draft.ID, draft.Name = "loomy", "Loomy"
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	w := providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, draft)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("stored Loomy check must use its provider key: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	var checked struct {
		Verified bool   `json:"verified"`
		AppID    string `json:"appId"`
	}
	if json.Unmarshal(w.Body.Bytes(), &checked) != nil || !checked.Verified || checked.AppID != "loomy" {
		t.Fatal("stored Loomy check did not verify the explicit application")
	}
	session := draft
	session.CredentialMode = routerconfig.ForwardAuthorization
	w = providerRequest(t, h, "POST", "/api/apps/check", cloudHTTPManagement, session)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "connection_check_unsupported") || calls.Load() != 1 {
		t.Fatal("management check invented or borrowed a session credential")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Loomy connection checks changed configuration")
	}
	for _, app := range mustLoomyApps(t, store) {
		if app.ID == "loomy" && app.Configured {
			t.Fatal("checking Loomy saved its Profile")
		}
	}
	if _, err := store.SaveApp(draft); err != nil {
		t.Fatal(err)
	}
	body := cloudHTTPInput("chat", false, false)
	body["model"] = draft.Cloud.Model
	w = cloudHTTPCall(t, h, context.Background(), "loomy", "chat", body, "Bearer fake-session", "fake-client-key")
	if w.Code != 200 || calls.Load() != 2 {
		t.Fatalf("stored Loomy call: status=%d calls=%d", w.Code, calls.Load())
	}
	body["model"] = "other-agent-model"
	w = cloudHTTPCall(t, h, context.Background(), "loomy", "chat", body, "Bearer fake-other-key", "fake-client-key")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "target_mismatch") || calls.Load() != 2 {
		t.Fatal("host request changed Loomy's bound target")
	}
}

func mustLoomyApps(t *testing.T, store *routerconfig.Store) []routerconfig.AppProfile {
	t.Helper()
	apps, err := store.Apps()
	if err != nil {
		t.Fatal(err)
	}
	return apps
}
