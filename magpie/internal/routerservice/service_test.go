package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }

func fixture(t *testing.T) (dirs.Layout, string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	l, err := dirs.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routerconfig.New(l).Initialize(); err != nil {
		t.Fatal(err)
	}
	// Reserve a free loopback port, without assuming a machine-wide fixed port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := routerconfig.New(l).Update(func(d routerconfig.Document) error {
		d["listen"] = json.RawMessage(fmt.Sprintf(`{"address":%q}`, address))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return l, address
}

func TestAddressRejectsSharedAndInvalidListeners(t *testing.T) {
	for _, address := range []string{"", ":13145", "0.0.0.0:13145", "[::]:13145", "192.168.1.2:13145", "localhost:13145", "example.com:13145", "[fe80::1%eth0]:13145", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http"} {
		b, _ := json.Marshal(map[string]string{"address": address})
		if _, err := Address(routerconfig.Document{"listen": b}); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:13145", "[::1]:13145"} {
		b, _ := json.Marshal(map[string]string{"address": address})
		got, err := Address(routerconfig.Document{"listen": b})
		if err != nil || got != address {
			t.Errorf("%s: %s %v", address, got, err)
		}
	}
}

func TestHTTPAllowListAndLocalBoundary(t *testing.T) {
	const address = "127.0.0.1:13145"
	const token = "test-management-token"
	h := handler(Info{Service: "api-router", Version: "i0-test", Address: address}, token)
	request := func(method, path, host, remote, origin, site, auth string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://"+address+path, nil)
		r.Host, r.RemoteAddr = host, remote
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/app.js", "/provider.js", "/tutorial.js", "/provider-presets.json", "/router.css", "/app.css", "/context.css", "/plugins/routing/routing.css", "/i18n.js", "/icons/favicon.svg", "/icons/codex-color.svg", "/healthz", "/api/service"} {
		w := request("GET", path, address, "127.0.0.1:2345", "http://"+address, "same-origin", "Bearer "+token)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("token exposed by HTTP")
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing browser protection")
		}
		w = request("HEAD", path, address, "127.0.0.1:2345", "", "", "Bearer "+token)
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Fatalf("HEAD %s: %d %s", path, w.Code, w.Body.String())
		}
		w = request("POST", path, address, "127.0.0.1:2345", "", "", "Bearer "+token)
		if w.Code != 405 {
			t.Fatalf("POST %s: %d", path, w.Code)
		}
	}
	for _, auth := range []string{"", "Bearer cloud-key", "Bearer wrong", token} {
		if w := request("GET", "/api/service", address, "127.0.0.1:2345", "", "", auth); w.Code != 401 {
			t.Fatalf("bad auth: %d", w.Code)
		}
	}
	for _, c := range []struct{ host, remote, origin, site string }{
		{"attacker.example", "127.0.0.1:2345", "", ""},
		{address, "192.168.1.2:2345", "", ""},
		{address, "127.0.0.1:2345", "https://attacker.example", ""},
		{address, "127.0.0.1:2345", "null", ""},
		{address, "127.0.0.1:2345", "http://" + address, "cross-site"},
		{address, "127.0.0.1:2345", "http://" + address, "same-site"},
	} {
		if w := request("GET", "/api/service", c.host, c.remote, c.origin, c.site, "Bearer "+token); w.Code != 403 {
			t.Fatalf("boundary %+v: %d", c, w.Code)
		}
	}
	for _, path := range []string{"/boot.js", "/kernel.js", "/start.js", "/context.js", "/plugins/usage/page.js", "/plugins/library/client.js", "/routerassets.go", "/api/providers", "/api/settings", "/api/sessions", "/api/groups", "/api/accounts", "/api/quota", "/api/plugins", "/api/library", "/api/sync", "/api/update", "/api/otel", "/api/import", "/api/agents", "/api/profiles", "/settings.json", "/plugins.yml", "/management-token", "/assets/app.js", "/sessions", "/v1/models", "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens", "/v1/images/generations", "/v1/audio/transcriptions", "/v1/embeddings", "/v1/rerank", "/%61pi/service", "/api//service", "/api/../api/service"} {
		for _, method := range []string{"GET", "POST"} {
			if w := request(method, path, address, "127.0.0.1:2345", "", "", "Bearer "+token); w.Code != 404 {
				t.Errorf("old/unregistered %s %s: %d", method, path, w.Code)
			}
		}
	}
}

func start(t *testing.T, l dirs.Layout) (Info, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan Info, 1)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- Run(ctx, l, "i0-test", func(info Info) error { ready <- info; return nil })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("service failed to exit")
		}
	})
	select {
	case info := <-ready:
		return info, cancel, done
	case err := <-done:
		t.Fatalf("service failed to start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("service did not report ready")
	}
	return Info{}, cancel, done
}

func TestServiceLifecycleAndDuplicateInstance(t *testing.T) {
	l, address := fixture(t)
	before, _ := os.ReadFile(l.ConfigFile())
	info, cancel, done := start(t, l)
	if info.Address != address || !info.ModelAPI || info.Runtime {
		t.Fatalf("false readiness: %+v", info)
	}
	secret, err := os.ReadFile(TokenFile(l))
	if err != nil || len(strings.TrimSpace(string(secret))) != 64 {
		t.Fatalf("token: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(TokenFile(l))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token permissions: %v %v", fi, err)
		}
	}
	if err := Healthcheck(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest("GET", "http://"+address+"/api/service", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Info
	err = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || got.InstanceID != info.InstanceID || got.Version != "i0-test" {
		t.Fatalf("status: %+v %v", got, err)
	}
	if err := Run(context.Background(), l, "second", nil); err == nil || !strings.Contains(err.Error(), "service lock") {
		t.Fatalf("duplicate service accepted: %v", err)
	}
	after, _ := os.ReadFile(l.ConfigFile())
	if !bytes.Equal(before, after) {
		t.Fatal("startup rewrote existing config")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not stop")
	}
	if _, err := os.Stat(l.ServiceLock()); !os.IsNotExist(err) {
		t.Fatalf("lock leaked: %v", err)
	}
	if err := Healthcheck(context.Background(), l); err == nil {
		t.Fatal("stopped service reported healthy")
	}
}

func TestStartupFailuresPreserveConfigAndReleaseLock(t *testing.T) {
	l, address := fixture(t)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("ready output failed")
	if err := Run(context.Background(), l, "test", func(info Info) error {
		if info.Address == address || info.PreviousAddress != address {
			t.Error("occupied port did not select another bound address")
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	ln.Close()
	if _, err := os.Stat(l.ServiceLock()); !os.IsNotExist(err) {
		t.Fatalf("failed bind leaked lock: %v", err)
	}
	if err := Run(context.Background(), l, "test", func(Info) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.ServiceLock()); !os.IsNotExist(err) {
		t.Fatalf("failed report leaked lock: %v", err)
	}
	if err := os.WriteFile(l.ConfigFile(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), l, "test", nil); err == nil {
		t.Fatal("broken config accepted")
	}
	b, _ := os.ReadFile(l.ConfigFile())
	if string(b) != "{broken" {
		t.Fatal("broken config overwritten")
	}
}

func TestHealthcheckRejectsOtherServersAndRedirects(t *testing.T) {
	l, _ := fixture(t)
	for _, body := range []string{`{"status":"ok"}`, `{"service":"api-router","status":"bad"}`, `{"service":"api-router","status":"ok"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		if err := routerconfig.New(l).Update(func(d routerconfig.Document) error {
			b, _ := json.Marshal(map[string]string{"address": strings.TrimPrefix(server.URL, "http://")})
			d["listen"] = b
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		err := Healthcheck(context.Background(), l)
		server.Close()
		if (err == nil) != (body == `{"service":"api-router","status":"ok"}`) {
			t.Fatalf("body %s: %v", body, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/", http.StatusFound)
	}))
	defer server.Close()
	if err := routerconfig.New(l).Update(func(d routerconfig.Document) error {
		b, _ := json.Marshal(map[string]string{"address": strings.TrimPrefix(server.URL, "http://")})
		d["listen"] = b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Healthcheck(context.Background(), l); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestParseLocalReplyToolCallBoundaryValidation(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"   ","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"lookup","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		if _, err := parseLocalReply([]byte(raw)); err == nil {
			t.Fatalf("expected parseLocalReply to reject %s", raw)
		}
	}

	reply, err := parseLocalReply([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"  ","function":{"name":" lookup ","arguments":"{\"q\":\"ok\"}"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "call_1" || reply.ToolCalls[0].Name != "lookup" {
		t.Fatalf("unexpected parsed tool call: %+v", reply.ToolCalls)
	}
	for _, protocol := range []string{"anthropic", "responses"} {
		payload, err := localReplyPayload(protocol, reply)
		if err != nil {
			t.Fatalf("%s payload failed: %v", protocol, err)
		}
		encoded, err := json.Marshal(payload)
		if err != nil || !strings.Contains(string(encoded), `"call_1"`) {
			t.Fatalf("%s payload missing fallback tool call id: %s (%v)", protocol, string(encoded), err)
		}
	}
}
