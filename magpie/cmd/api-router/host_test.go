package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerhost"
	"github.com/yetone/magpie/internal/routerservice"
)

type hostCheckFunc func(context.Context, routerhost.Options) error

type failedHostOutput struct{}

func (failedHostOutput) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func (f hostCheckFunc) Check(ctx context.Context, options routerhost.Options) error {
	return f(ctx, options)
}

func setHostChecker(t *testing.T, checker routerhost.Checker) {
	t.Helper()
	previous := hostChecker
	hostChecker = func(string) routerhost.Checker { return checker }
	t.Cleanup(func() { hostChecker = previous })
}

func hostArgs(action, host, root, state string) []string {
	return []string{"host", action, "--host", host, "--config-root", root, "--state-dir", state}
}

func connectionArgs(args []string, origin string) []string {
	return append(args, "--router-url", origin, "--model", "test-model")
}

func previewDigest(t *testing.T, args []string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatalf("preview: %v", err)
	}
	var summary struct {
		Digest string `json:"digest"`
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Digest == "" {
		t.Fatalf("preview output: %s (%v)", out.String(), err)
	}
	if strings.Contains(out.String(), "test-app-secret-never-print") {
		t.Fatalf("preview leaked entry token: %s", out.String())
	}
	if args[1] == "restore-preview" && !strings.Contains(summary.Notice, "bypasses API-router") {
		t.Fatalf("restore preview did not explain bypass: %s", out.String())
	}
	return summary.Digest
}

func TestHostHelpNeverWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"host"}, {"host", "help"}, {"host", "--help"},
		{"host", "preview", "--help"}, {"host", "apply", "--help"},
		{"host", "restore-preview", "--help"}, {"host", "restore", "--help"},
		{"host", "apply", "--config-root", root, "--model", "--help"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err != nil || !strings.Contains(out.String(), "Usage: api-router host") {
			t.Errorf("%v: %q %v", args, out.String(), err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("help created host directory: %v", err)
	}
}

func TestHostRejectsInvalidOptionsWithoutWriting(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "host"), filepath.Join(base, "state")
	t.Setenv("API_ROUTER_TEST_APP_TOKEN", "test-app-secret-never-print")
	t.Setenv("API_ROUTER_MANAGEMENT_TOKEN", "test-management-secret")
	valid := connectionArgs(hostArgs("preview", "codex", root, state), "http://127.0.0.1:13145")
	cases := [][]string{
		{"host", "unknown"}, {"host", "preview", "--host", "--helpful"},
		append(append([]string{}, valid...), "--unknown", "test-app-secret-never-print"),
		append(append([]string{}, valid...), "extra"),
		append(append([]string{}, valid...), "--token", "test-app-secret-never-print"),
		append(append([]string{}, valid...), "--token-env", "API_ROUTER_MANAGEMENT_TOKEN"),
		append(append([]string{}, valid...), "--token-env", "MISSING_APP_TOKEN"),
		append(append([]string{}, valid...), "--host", "unknown"),
		append(append([]string{}, valid...), "--model", "--unknown"),
		append(append([]string{}, valid...), "--config-root", "relative"),
		append(append([]string{}, valid...), "--state-dir", "relative"),
		connectionArgs(hostArgs("apply", "codex", root, state), "http://127.0.0.1:13145"),
		hostArgs("restore", "codex", root, state),
		append(hostArgs("restore-preview", "codex", root, state), "--token-env", "API_ROUTER_TEST_APP_TOKEN"),
	}
	for _, args := range cases {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil {
			t.Errorf("accepted %v", args)
			continue
		}
		if strings.Contains(err.Error()+out.String(), "test-app-secret-never-print") || strings.Contains(err.Error()+out.String(), "test-management-secret") {
			t.Errorf("error leaked a secret: %s %v", out.String(), err)
		}
	}
	for _, path := range []string{root, state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("invalid options created %s: %v", path, err)
		}
	}
}

func TestHostCLIPreviewApplyAndOfflineRestore(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			base := t.TempDir()
			root, state := filepath.Join(base, "host"), filepath.Join(base, "state")
			t.Setenv("API_ROUTER_TEST_APP_TOKEN", "test-app-secret-never-print")
			checks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("injected checker received authorization")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			setHostChecker(t, hostCheckFunc(func(ctx context.Context, options routerhost.Options) error {
				checks++
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, options.RouterURL+"/test-connection", nil)
				if err != nil {
					return err
				}
				response, err := server.Client().Do(req)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					return fmt.Errorf("unexpected local fixture status")
				}
				return nil
			}))
			preview := connectionArgs(hostArgs("preview", host, root, state), server.URL)
			digest := previewDigest(t, preview)
			if checks != 0 {
				t.Fatal("preview performed connection checks")
			}
			for _, path := range []string{root, state} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("preview wrote %s: %v", path, err)
				}
			}
			apply := connectionArgs(hostArgs("apply", host, root, state), server.URL)
			var out bytes.Buffer
			if err := run(append(apply, "--plan-digest", digest), &out); err != nil {
				t.Fatal(err)
			}
			if checks != 1 || strings.Contains(out.String(), "test-app-secret-never-print") {
				t.Fatalf("checks=%d output=%s", checks, out.String())
			}
			// Recovery must be independent of a running Router and of token lookup.
			server.Close()
			t.Setenv("API_ROUTER_TEST_APP_TOKEN", "")
			hostChecker = func(string) routerhost.Checker { panic("restore constructed a connection checker") }
			recovery := hostArgs("restore-preview", host, root, state)
			restoreDigest := previewDigest(t, recovery)
			if err := run(append(hostArgs("restore", host, root, state), "--plan-digest", restoreDigest), failedHostOutput{}); err == nil || !strings.Contains(err.Error(), "cannot print recovery notice") {
				t.Fatalf("restore wrote without printing warning: %v", err)
			}
			if got := previewDigest(t, recovery); got != restoreDigest {
				t.Fatal("failed warning output changed host config or recovery record")
			}
			out.Reset()
			if err := run(append(hostArgs("restore", host, root, state), "--plan-digest", restoreDigest), &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "bypasses API-router") {
				t.Fatalf("restore omitted recovery notice: %s", out.String())
			}
		})
	}
}

func TestHostCLIRefusesStalePreviewAndFailedCheck(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "host"), filepath.Join(base, "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_ROUTER_TEST_APP_TOKEN", "test-app-secret-never-print")
	preview := connectionArgs(hostArgs("preview", "codex", root, state), "http://127.0.0.1:13145")
	digest := previewDigest(t, preview)
	configPath := filepath.Join(root, "config.toml")
	config := []byte("# added after preview\nmodel = \"user-model\"\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	checks := 0
	setHostChecker(t, hostCheckFunc(func(context.Context, routerhost.Options) error {
		checks++
		return errors.New("application configuration invalid; configure a Profile before retrying")
	}))
	apply := connectionArgs(hostArgs("apply", "codex", root, state), "http://127.0.0.1:13145")
	err := run(append(apply, "--plan-digest", digest), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "plan changed") || checks != 0 {
		t.Fatalf("stale preview: checks=%d err=%v", checks, err)
	}
	digest = previewDigest(t, preview)
	err = run(append(apply, "--plan-digest", digest), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Profile") || checks != 1 {
		t.Fatalf("failed check: checks=%d err=%v", checks, err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(got, config) {
		t.Fatalf("failed check changed host config: %q %v", got, err)
	}
}

// This exercises the actual CLI factory without replacing hostChecker. Only the
// local HTTP server stands in for the still-unintegrated A/B service contracts.
func TestHostCLIProductionApplyAndOfflineRestore(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			base := t.TempDir()
			root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
			const entry = "production-test-entry-never-print"
			const management = "production-test-management-never-print"
			t.Setenv("C_APP_ENTRY_FIXTURE", entry)
			t.Setenv("C_MANAGEMENT_FIXTURE", management)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/healthz":
					if r.Header.Get("Authorization") != "" {
						t.Error("health received secret")
					}
					fmt.Fprint(w, `{"service":"api-router","status":"ok"}`)
				case "/api/apps":
					if r.Header.Get("Authorization") != "Bearer "+management {
						t.Error("management credential not isolated")
					}
					fmt.Fprintf(w, `{"schemaVersion":1,"apps":[{"schemaVersion":1,"revision":1,"id":%q,"path":%q,"configured":true,"credentialMode":"stored","cloud":{"providerId":"p_fixture","model":"test-model"}}]}`, host, "/app/"+host+"/v1")
				default:
					suffix := "/responses"
					response := `{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}]}`
					if host == "claude-code" {
						suffix = "/messages"
						response = `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}]}`
					}
					if r.Method != "POST" || r.URL.Path != "/app/"+host+"/v1"+suffix || r.Header.Get("Authorization") != "" {
						t.Error("wrong business request or management token sent to model")
					}
					fmt.Fprint(w, response)
				}
			}))
			defer server.Close()
			connection := func(action string) []string {
				return append(hostArgs(action, host, root, state), "--router-url", server.URL, "--model", "test-model")
			}
			digest := previewDigest(t, connection("preview"))
			var out bytes.Buffer
			if err := run(append(connection("apply"), "--management-token-env", "C_MANAGEMENT_FIXTURE", "--plan-digest", digest), &out); err != nil {
				t.Fatal(err)
			}
			file := "config.toml"
			if host == "claude-code" {
				file = "settings.json"
			}
			raw, err := os.ReadFile(filepath.Join(root, file))
			if err != nil || !bytes.Contains(raw, []byte(routerhost.PlaceholderAPIKey)) || bytes.Contains(raw, []byte(entry)) || bytes.Contains(raw, []byte(management)) {
				t.Fatal("wrong host credential content")
			}
			records, err := os.ReadDir(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				data, err := os.ReadFile(filepath.Join(state, record.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte(management)) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString([]byte(management)))) {
					t.Fatal("management secret persisted")
				}
			}
			if strings.Contains(out.String(), entry) || strings.Contains(out.String(), management) {
				t.Fatal("CLI leaked credential")
			}
			server.Close()
			t.Setenv("C_APP_ENTRY_FIXTURE", "")
			t.Setenv("C_MANAGEMENT_FIXTURE", "")
			restoreDigest := previewDigest(t, hostArgs("restore-preview", host, root, state))
			if err = run(append(hostArgs("restore", host, root, state), "--plan-digest", restoreDigest), &out); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
				t.Fatal("offline restore failed")
			}
		})
	}
}

func TestHostCLIRejectsObsoleteEntryTokenWithoutReadingIt(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
	for _, name := range []string{"UNSET_LEGACY_TOKEN", "RENAMED_MANAGEMENT_TOKEN", ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RENAMED_MANAGEMENT_TOKEN", "management-secret-never-print")
			args := append(connectionArgs(hostArgs("preview", "codex", root, state), "http://127.0.0.1:13145"), "--token-env", name)
			err := run(args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "--token-env is obsolete") || strings.Contains(err.Error(), "management-secret-never-print") {
				t.Fatalf("obsolete token option did not fail safely: %v", err)
			}
		})
	}
	for _, path := range []string{root, state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("obsolete token option wrote files")
		}
	}
}

// Exercise the real service lifetime and HTTP dispatch, including access control
// and the saved application/provider projection. Only the cloud is a fixture.
func TestHostCLIProductModelFailureLeavesNoFiles(t *testing.T) {
	base := t.TempDir()
	layout := dirs.Layout{Config: filepath.Join(base, "config"), Data: filepath.Join(base, "data"), Models: filepath.Join(base, "models"), Runtime: filepath.Join(base, "runtime"), Cache: filepath.Join(base, "cache"), State: filepath.Join(base, "state"), Logs: filepath.Join(base, "logs")}
	store := routerconfig.New(layout)
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(d routerconfig.Document) error {
		d["listen"], _ = json.Marshal(map[string]string{"address": address})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// After the cloud HTTP integration lands this remains a genuine failed
		// model check, rather than depending on the old unwired 501 branch.
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprint(w, `{"error":{"message":"private-cloud-error-never-print"}}`)
	}))
	t.Cleanup(upstream.Close)
	key := "provider-secret-never-print"
	provider, err := store.SaveCloudProvider(routerconfig.ProviderEdit{Name: "isolated failure", Protocol: "chat", BaseURL: upstream.URL + "/v1", Model: "test-model", APIKey: &key, ProxyMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"codex", "claude-code"} {
		if _, err := store.SaveApp(routerconfig.AppEdit{ID: host, Name: host, Cloud: routerconfig.AppCloud{ProviderID: provider.ID, Model: "test-model"}, CredentialMode: "stored"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- routerservice.Run(ctx, layout, "host-acceptance-test", func(routerservice.Info) error { close(ready); return nil })
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("isolated product service did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		// Keep cleanup's join available even when startup fails.
		done <- err
		t.Fatalf("product service startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("isolated product service did not start")
	}
	token, err := os.ReadFile(routerservice.TokenFile(layout))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_PRODUCT_MANAGEMENT_TOKEN", strings.TrimSpace(string(token)))
	for _, host := range []string{"codex", "claude-code"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", host, existing), func(t *testing.T) {
				root, state := filepath.Join(t.TempDir(), "host"), filepath.Join(t.TempDir(), "records")
				file, original := "config.toml", []byte("# original\nmodel = \"original\"\n")
				if host == "claude-code" {
					file, original = "settings.json", []byte("{\n // original\n \"unrelated\": true\n}\n")
				}
				if existing {
					if err := os.Mkdir(root, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, file), original, 0o640); err != nil {
						t.Fatal(err)
					}
				}
				args := connectionArgs(hostArgs("preview", host, root, state), "http://"+address)
				digest := previewDigest(t, args)
				args[1] = "apply"
				var output bytes.Buffer
				err := run(append(args, "--management-token-env", "HOST_PRODUCT_MANAGEMENT_TOKEN", "--plan-digest", digest), &output)
				if err == nil || !strings.Contains(err.Error(), "model business entry") || !strings.Contains(err.Error(), "HTTP ") {
					t.Fatalf("failed product model entry allowed apply: %v", err)
				}
				t.Log(err)
				for _, private := range []string{strings.TrimSpace(string(token)), key, "private-cloud-error-never-print"} {
					if strings.Contains(err.Error()+output.String(), private) {
						t.Fatal("product check leaked private data")
					}
				}
				if _, err := os.Stat(state); !os.IsNotExist(err) {
					t.Fatalf("failed product check created a recovery directory: %v", err)
				}
				if !existing {
					if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatalf("failed product check created a host directory: %v", err)
					}
				} else {
					actual, err := os.ReadFile(filepath.Join(root, file))
					if err != nil || !bytes.Equal(actual, original) {
						t.Fatal("failed product check changed original config")
					}
					entries, err := os.ReadDir(root)
					if err != nil || len(entries) != 1 {
						t.Fatal("failed product check left host operation files")
					}
				}
			})
		}
	}
}
