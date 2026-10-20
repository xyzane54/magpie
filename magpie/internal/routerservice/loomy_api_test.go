package routerservice

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

type loomyAPIFixture struct {
	handler   http.Handler
	token     string
	root      string
	state     string
	path      string
	original  []byte
	config    string
	configRaw []byte
}

func newLoomyAPIFixture(t *testing.T) loomyAPIFixture {
	t.Helper()
	layout, _ := fixture(t)
	base := t.TempDir()
	f := loomyAPIFixture{token: "loomy-management-only-secret", root: filepath.Join(base, "opencode"),
		state: filepath.Join(base, "service-recovery"), config: layout.ConfigFile()}
	f.path = filepath.Join(f.root, "opencode.json")
	f.original = []byte("{\r\n  // user-owned settings remain private\r\n  \"model\":\"imodel/original\",\r\n  \"provider\":{\"imodel\":{\"options\":{\"apiKey\":\"loomy-original-auth-secret\",\"useSessionAuth\":true}}},\r\n  \"theme\":\"system\"\r\n}\r\n")
	if err := os.MkdirAll(f.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, f.original, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	f.configRaw, err = os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handlerWithModelOptions(Info{Service: "api-router", Address: "127.0.0.1:13145"},
		f.token, routerconfig.New(layout), ModelOptions{HostStateDir: f.state})
	return f
}

func (f loomyAPIFixture) body(action string) map[string]any {
	return map[string]any{"action": action, "configRoot": f.root, "model": "vendor/model.v1"}
}

func (f loomyAPIFixture) unchanged(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, source := range []struct {
		path string
		raw  []byte
	}{{f.path, f.original}, {f.config, f.configRaw}} {
		got, err := os.ReadFile(source.path)
		if err != nil || !bytes.Equal(got, source.raw) {
			t.Fatal("management request changed host or Router configuration", source.path, err)
		}
	}
	if _, err := os.Stat(f.state); !os.IsNotExist(err) {
		t.Fatal("nonwriting request created recovery state", err)
	}
	entries, err := os.ReadDir(f.root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "opencode.json" {
		t.Fatal("nonwriting request left host files or locks", entries, err)
	}
	if response != nil {
		for _, secret := range []string{"loomy-original-auth-secret", f.token, "user-owned settings remain private", "imodel/original"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("host management response disclosed private source content")
			}
		}
	}
}

func TestLoomyHostAPIPreviewWritesNothingAndReturnsSafePlan(t *testing.T) {
	f := newLoomyAPIFixture(t)
	w := providerRequest(t, f.handler, "POST", "/api/hosts/loomy", f.token, f.body("preview"))
	if w.Code != 200 {
		t.Fatalf("preview status %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Action, Digest, BaseURL, RequestURL, StateDir string
		Plan                                          json.RawMessage
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "preview" || len(result.Digest) != 64 || len(result.Plan) == 0 ||
		result.BaseURL != "http://127.0.0.1:13145/app/loomy/v1" ||
		result.RequestURL != "http://127.0.0.1:13145/app/loomy/v1/chat/completions" || result.StateDir != f.state {
		t.Fatal("preview lacks exact Loomy endpoint, digest or service-owned recovery location")
	}
	f.unchanged(t, w)
	// This fixture starts no HTTP service or usable application binding. Preview
	// must still succeed without running the network connection Checker.
	w = providerRequest(t, f.handler, "POST", "/api/hosts/loomy", f.token, f.body("preview"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), result.Digest) {
		t.Fatal("repeat preview changed its plan or attempted connection checks")
	}
	f.unchanged(t, w)
}

func TestLoomyHostAPIRejectsUnsafeRequestsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		method string
		auth   bool
		origin string
		edit   func(loomyAPIFixture, map[string]any)
	}{
		{name: "get", status: 405, method: "GET", auth: true},
		{name: "unauthenticated", status: 401, method: "POST"},
		{name: "foreign-origin", status: 403, method: "POST", auth: true, origin: "https://foreign.invalid"},
		{name: "wrong-digest", status: 409, method: "POST", auth: true, edit: func(_ loomyAPIFixture, body map[string]any) {
			body["action"], body["digest"] = "apply", strings.Repeat("0", 64)
		}},
		{name: "http-state-dir", status: 400, method: "POST", auth: true, edit: func(f loomyAPIFixture, body map[string]any) {
			body["stateDir"] = filepath.Join(filepath.Dir(f.root), "caller-selected-state")
		}},
		{name: "management-token-in-root", status: 403, method: "POST", auth: true, edit: func(f loomyAPIFixture, body map[string]any) {
			body["configRoot"] = filepath.Join(f.root, f.token)
		}},
		{name: "management-token-in-model", status: 403, method: "POST", auth: true, edit: func(f loomyAPIFixture, body map[string]any) {
			body["model"] = f.token
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoomyAPIFixture(t)
			body := f.body("preview")
			if tc.edit != nil {
				tc.edit(f, body)
			}
			auth := ""
			if tc.auth {
				auth = f.token
			}
			var w *httptest.ResponseRecorder
			if tc.origin == "" {
				w = providerRequest(t, f.handler, tc.method, "/api/hosts/loomy", auth, body)
			} else {
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(tc.method, "http://127.0.0.1:13145/api/hosts/loomy", bytes.NewReader(raw))
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+auth)
				r.Header.Set("Origin", tc.origin)
				w = httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
			}
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
			if tc.method == "GET" && w.Header().Get("Allow") != "POST" {
				t.Fatal("GET did not advertise the explicit POST operation")
			}
			f.unchanged(t, w)
		})
	}
}
