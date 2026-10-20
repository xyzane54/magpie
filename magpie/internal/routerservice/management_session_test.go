package routerservice

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func browserManagementRequest(method, address, path string) *http.Request {
	r := httptest.NewRequest(method, "http://"+address+path, nil)
	r.Host = address
	r.RemoteAddr = "127.0.0.1:2345"
	r.Header.Set("Origin", "http://"+address)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-API-Router-UI", "1")
	return r
}

func checkManagementResponseProtections(t *testing.T, header http.Header) {
	t.Helper()
	if header.Get("Cache-Control") != "no-store" || header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing management response protections: %v", header)
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Fatalf("management endpoint grants CORS via %s", name)
		}
	}
}

func TestManagementSessionSameOriginBootstrap(t *testing.T) {
	const token = "process-management-token"
	for _, address := range []string{"127.0.0.1:13145", "[::1]:13145"} {
		t.Run(address, func(t *testing.T) {
			h := handler(Info{Service: "api-router", Address: address}, token)
			r := browserManagementRequest(http.MethodPost, address, "/api/management/session")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("own browser cannot connect automatically: %d %s", w.Code, w.Body.String())
			}
			checkManagementResponseProtections(t, w.Header())
			if w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
				t.Fatal("bootstrap response lacks same-origin resource policy")
			}
			if w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("bootstrap content type: %q", w.Header().Get("Content-Type"))
			}
			var session struct {
				SchemaVersion int    `json:"schemaVersion"`
				Token         string `json:"token"`
			}
			if err := json.NewDecoder(w.Body).Decode(&session); err != nil {
				t.Fatal(err)
			}
			if session.SchemaVersion != 1 || session.Token != token {
				t.Fatalf("bootstrap did not return this process session: %+v", session)
			}
			for _, auth := range []string{"", "Bearer " + session.Token} {
				r = browserManagementRequest(http.MethodGet, address, "/api/service")
				if auth != "" {
					r.Header.Set("Authorization", auth)
				}
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := http.StatusUnauthorized
				if auth != "" {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatalf("service authorization after bootstrap: got %d, want %d", w.Code, want)
				}
				if strings.Contains(w.Body.String(), token) {
					t.Fatal("service status leaks management token")
				}
			}
		})
	}
}

func TestManagementSessionRejectsUnauthorizedBrowserContexts(t *testing.T) {
	const address = "127.0.0.1:13145"
	const token = "process-management-token"
	h := handler(Info{Address: address}, token)
	cases := []struct {
		name   string
		change func(*http.Request)
	}{
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://attacker.example") }},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", "http://"+address) }},
		{"mixed duplicate origin", func(r *http.Request) { r.Header.Add("Origin", "http://attacker.example") }},
		{"comma separated origins", func(r *http.Request) { r.Header.Set("Origin", "http://"+address+", http://"+address) }},
		{"https origin", func(r *http.Request) { r.Header.Set("Origin", "https://"+address) }},
		{"origin trailing slash", func(r *http.Request) { r.Header.Set("Origin", "http://"+address+"/") }},
		{"missing fetch metadata", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }},
		{"navigation fetch metadata", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }},
		{"same site fetch metadata", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"foreign fetch metadata", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"missing UI header", func(r *http.Request) { r.Header.Del("X-API-Router-UI") }},
		{"different UI header", func(r *http.Request) { r.Header.Set("X-API-Router-UI", "true") }},
		{"foreign host", func(r *http.Request) { r.Host = "attacker.example:13145" }},
		{"different loopback host", func(r *http.Request) { r.Host = "127.0.0.2:13145" }},
		{"nonlocal peer", func(r *http.Request) { r.RemoteAddr = "192.0.2.10:2345" }},
		{"malformed peer", func(r *http.Request) { r.RemoteAddr = "127.0.0.1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := browserManagementRequest(http.MethodPost, address, "/api/management/session")
			// An otherwise valid Bearer cannot bypass the bootstrap's browser gate.
			r.Header.Set("Authorization", "Bearer "+token)
			tc.change(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("unsafe browser context accepted: %d %s", w.Code, w.Body.String())
			}
			checkManagementResponseProtections(t, w.Header())
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("rejected bootstrap leaks management token")
			}
		})
	}
	// An uninitialized handler must never grant an empty management session.
	w := httptest.NewRecorder()
	handler(Info{Address: address}, "").ServeHTTP(w, browserManagementRequest(http.MethodPost, address, "/api/management/session"))
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("empty process token accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementSessionMethodsAndPathAliases(t *testing.T) {
	const address = "127.0.0.1:13145"
	const token = "process-management-token"
	h := handler(Info{Address: address}, token)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, browserManagementRequest(method, address, "/api/management/session"))
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("bootstrap permits %s: %d %v", method, w.Code, w.Header())
			}
			checkManagementResponseProtections(t, w.Header())
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("disallowed method leaks management token")
			}
		})
	}
	for _, path := range []string{
		"/%61pi/management/session", "/api/management/%73ession", "/api/management%2fsession",
		"/api/management/session%2f", "/api/management/session/", "/api//management/session",
		"//api/management/session", "/api/../api/management/session",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, browserManagementRequest(http.MethodPost, address, path))
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), token) {
				t.Fatalf("management alias accepted or leaks token: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestManagementSessionRefreshesAfterServiceRestart(t *testing.T) {
	l, address := fixture(t)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	bootstrap := func() string {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, "http://"+address+"/api/management/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", "http://"+address)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-API-Router-UI", "1")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		checkManagementResponseProtections(t, resp.Header)
		var session struct {
			SchemaVersion int    `json:"schemaVersion"`
			Token         string `json:"token"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&session)
		if err != nil || resp.StatusCode != http.StatusOK || session.SchemaVersion != 1 || len(session.Token) != 64 {
			t.Fatalf("running service bootstrap: status %d, decode %v, token length %d", resp.StatusCode, err, len(session.Token))
		}
		return session.Token
	}
	_, cancel, done := start(t, l)
	oldToken := bootstrap()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first service did not stop")
	}
	client.CloseIdleConnections()
	_, _, _ = start(t, l)
	newToken := bootstrap()
	if newToken == oldToken {
		t.Fatal("new service reused the previous process management token")
	}
	for _, token := range []string{oldToken, newToken} {
		r, err := http.NewRequest(http.MethodGet, "http://"+address+"/api/service", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		want := http.StatusOK
		if token == oldToken {
			want = http.StatusUnauthorized
		}
		if resp.StatusCode != want {
			t.Fatalf("service authorization after restart: got %d, want %d", resp.StatusCode, want)
		}
	}
}
