package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerhost"
)

func portServiceFixture(t *testing.T) (dirs.Layout, string, map[string]string, *atomic.Int32) {
	t.Helper()
	l, address := fixture(t)
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"1"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	store := routerconfig.New(l)
	provider, err := store.SaveCloudProvider(providerInputFor(upstream.URL + "/v1"))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, host := range []string{"workbuddy", "loomy"} {
		if _, err := store.SaveApp(routerconfig.AppEdit{ID: host, Name: host, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "fixture-model"}, CredentialMode: routerconfig.StoredCredential}); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), host)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if host == "workbuddy" {
			t.Setenv("WORKBUDDY_CONFIG_DIR", root)
			paths[host] = filepath.Join(root, "models.json")
			raw, _ = json.Marshal([]any{map[string]any{"id": "fixture-model", "name": "API Router", "url": "http://" + address + "/app/workbuddy/v1", "apiKey": routerhost.PlaceholderAPIKey, "supportsToolCall": true, "useCustomProtocol": false, "later": "keep"}})
			if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"model":"custom-local:fixture-model","theme":"dark"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Setenv("LOOMY_CONFIG_DIR", root)
			paths[host] = filepath.Join(root, "opencode.json")
			raw = []byte(fmt.Sprintf(`{// keep comment
"provider":{"api_router":{"npm":"@ai-sdk/openai-compatible","name":"API Router","options":{"baseURL":%q,"apiKey":%q,"useSessionAuth":false,"timeout":1200000},"models":{"fixture-model":{"name":"fixture-model","tool_call":true}}}},"model":"api_router/fixture-model","later":"keep"}`, "http://"+address+"/app/loomy/v1", routerhost.PlaceholderAPIKey))
		}
		if err := os.WriteFile(paths[host], raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return l, address, paths, calls
}

func TestOccupiedPortAutomaticallyPersistsAndSynchronizesBothHosts(t *testing.T) {
	l, address, paths, calls := portServiceFixture(t)
	occupied, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	credentials, err := os.ReadFile(l.CredentialsFile())
	if err != nil {
		t.Fatal(err)
	}
	info, stop, done := start(t, l)
	if info.Address == address || info.PreviousAddress != address {
		t.Fatalf("busy address was not replaced: %+v", info)
	}
	d, err := routerconfig.New(l).Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved, _ := Address(d); saved != info.Address {
		t.Fatalf("bound address was not persisted: %q vs %q", saved, info.Address)
	}
	if calls.Load() != 0 {
		t.Fatal("startup made unsolicited cloud probes")
	}
	for host, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(raw, []byte("http://"+info.Address+"/app/"+host+"/v1")) || !bytes.Contains(raw, []byte(`"later"`)) {
			t.Fatalf("%s was not synchronized before readiness: %s, %v", host, raw, err)
		}
		response, err := http.Post("http://"+info.Address+"/app/"+host+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"fixture-model","messages":[{"role":"user","content":"Reply 1"}],"max_tokens":32}`))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte(`"content":"1"`)) {
			t.Fatalf("%s cannot use the replacement port: status=%d body=%s err=%v", host, response.StatusCode, body, err)
		}
	}
	if err := Healthcheck(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	afterCredentials, _ := os.ReadFile(l.CredentialsFile())
	if !bytes.Equal(credentials, afterCredentials) {
		t.Fatal("port synchronization changed credentials")
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next, stopNext, doneNext := start(t, l)
	if next.Address != info.Address || next.PreviousAddress != "" {
		t.Fatalf("restart did not retain the chosen port: %q -> %q", info.Address, next.Address)
	}
	stopNext()
	if err := <-doneNext; err != nil {
		t.Fatal(err)
	}
	// The other program continues owning the original port throughout.
	if probe, err := net.Listen("tcp", address); err == nil {
		probe.Close()
		t.Fatal("startup reclaimed the unrelated listener")
	}
}

func TestPortSynchronizationFailureDoesNotReportReadyOrPersistNewAddress(t *testing.T) {
	l, address, paths, _ := portServiceFixture(t)
	occupied, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	wbBefore, _ := os.ReadFile(paths["workbuddy"])
	configBefore, _ := os.ReadFile(l.ConfigFile())
	if err := os.Chmod(paths["loomy"], 0o400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(paths["loomy"], 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = Run(ctx, l, "port-failure-test", func(Info) error { t.Error("failed synchronization reported ready"); return nil })
	if err == nil || !strings.Contains(err.Error(), "synchronize") {
		t.Fatalf("port synchronization failure was hidden: %v", err)
	}
	wbAfter, _ := os.ReadFile(paths["workbuddy"])
	configAfter, _ := os.ReadFile(l.ConfigFile())
	if !bytes.Equal(wbBefore, wbAfter) || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("failed synchronization retained a partial host or listen-address change")
	}
	if _, err := os.Stat(l.ServiceLock()); !os.IsNotExist(err) {
		t.Fatal("failed synchronization leaked the service lock", err)
	}
}
