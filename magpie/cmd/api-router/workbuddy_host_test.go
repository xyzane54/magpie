package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/routerhost"
)

const workBuddyCLIManagement = "workbuddy-cli-management-fixture-never-print"

func workBuddyCLIArgs(action, root, state, origin string) []string {
	args := hostArgs(action, "workbuddy", root, state)
	if action == "preview" || action == "apply" {
		args = connectionArgs(args, origin)
	}
	return args
}

func workBuddyCLIFixture(t *testing.T, failure string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/apps" && r.Header.Get("Authorization") != "" {
			t.Error("management credential escaped /api/apps")
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("checker sent a model credential")
		}
		switch r.URL.Path {
		case "/healthz":
			io.WriteString(w, `{"service":"api-router","status":"ok"}`)
		case "/api/apps":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+workBuddyCLIManagement {
				t.Error("wrong management check")
			}
			model := "test-model"
			if failure == "app-model" {
				model = "other-model"
			}
			fmt.Fprintf(w, `{"schemaVersion":1,"apps":[{"schemaVersion":1,"revision":1,"id":"workbuddy","path":"/app/workbuddy/v1","configured":true,"credentialMode":"stored","cloud":{"providerId":"p_fixture","model":%q}}]}`, model)
		case "/app/workbuddy/v1/chat/completions":
			var body struct{ Model string }
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "test-model" {
				t.Error("wrong WorkBuddy business request")
			}
			if failure == "business-status" {
				w.WriteHeader(http.StatusNotImplemented)
				io.WriteString(w, `{"error":{"message":"fixture-private-cloud-error"}}`)
			} else if failure == "business-empty" {
				io.WriteString(w, `{"object":"chat.completion","model":"test-model","choices":[]}`)
			} else {
				io.WriteString(w, `{"id":"chatcmpl-cli","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			}
		default:
			t.Errorf("wrong host/protocol path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func workBuddyCLIOriginals(t *testing.T, root string) map[string][]byte {
	t.Helper()
	originals := map[string][]byte{
		"models.json":   []byte(`{"models":[{"id":"test-model","url":"https://original.invalid/v1","apiKey":"fixture-user-key-private","unknown":{"keep":true}},{"id":"other-model","url":"https://other.invalid/v1","apiKey":"fixture-other-key-private"}],"extension":"keep"}`),
		"settings.json": []byte("{\n // keep user comment\n \"model\":\"original-model\",\n \"userSetting\":true\n}\n"),
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range originals {
		if err := os.WriteFile(filepath.Join(root, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return originals
}

func workBuddyCLIRecord(t *testing.T, state string) []byte {
	t.Helper()
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "workbuddy-") {
		t.Fatalf("expected one WorkBuddy recovery record: %v %v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(state, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{workBuddyCLIManagement, base64.StdEncoding.EncodeToString([]byte(workBuddyCLIManagement))} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("management token persisted in recovery record")
		}
	}
	return raw
}

func TestWorkBuddyHostCLIProductionApplyRepeatAndStoppedRestore(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			base := t.TempDir()
			root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
			var originals map[string][]byte
			if existing {
				originals = workBuddyCLIOriginals(t, root)
			}
			t.Setenv("WORKBUDDY_CLI_MANAGEMENT_FIXTURE", workBuddyCLIManagement)
			// This preserves the old injection seam while proving WorkBuddy uses
			// its own production checker, rather than the two built-in policies.
			oldChecker := hostChecker
			hostChecker = func(string) routerhost.Checker { panic("WorkBuddy used the built-in host checker") }
			t.Cleanup(func() { hostChecker = oldChecker })
			var calls atomic.Int32
			server := workBuddyCLIFixture(t, "", &calls)
			var output bytes.Buffer
			preview := workBuddyCLIArgs("preview", root, state, server.URL)
			if err := run(preview, &output); err != nil {
				t.Fatal(err)
			}
			var summary struct {
				Digest string
				Plan   struct {
					RouterURL, RequestURL, ModelSelection, HostVersion, CredentialSource string
				}
			}
			if err := json.Unmarshal(output.Bytes(), &summary); err != nil || summary.Digest == "" || summary.Plan.RouterURL != server.URL+"/app/workbuddy/v1" || summary.Plan.RequestURL != server.URL+"/app/workbuddy/v1/chat/completions" || summary.Plan.ModelSelection != "custom-local:test-model" || summary.Plan.HostVersion != "WorkBuddy 5.3.8" || !strings.Contains(summary.Plan.CredentialSource, "public non-secret placeholder") {
				t.Fatalf("preview lost the installed configuration contract: %s (%v)", output.String(), err)
			}
			if calls.Load() != 0 {
				t.Fatal("preview performed network checks")
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("preview created recovery state")
			}
			if !existing {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("preview created host configuration")
				}
			} else {
				for name, original := range originals {
					raw, err := os.ReadFile(filepath.Join(root, name))
					if err != nil || !bytes.Equal(raw, original) {
						t.Fatal("preview changed an original file")
					}
				}
			}
			apply := workBuddyCLIArgs("apply", root, state, server.URL)
			apply = append(apply, "--management-token-env", "WORKBUDDY_CLI_MANAGEMENT_FIXTURE")
			if err := run(append(apply, "--plan-digest", summary.Digest), &output); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 {
				t.Fatalf("production checks=%d, expected health/apps/chat", calls.Load())
			}
			models, err := os.ReadFile(filepath.Join(root, "models.json"))
			if err != nil || gjson.GetBytes(models, "models.0.id").String() != "test-model" || gjson.GetBytes(models, "models.0.url").String() != server.URL+"/app/workbuddy/v1" || gjson.GetBytes(models, "models.0.apiKey").String() != routerhost.PlaceholderAPIKey || !gjson.GetBytes(models, "models.0.supportsToolCall").Bool() || gjson.GetBytes(models, "models.0.useCustomProtocol").Bool() {
				t.Fatal("WorkBuddy model configuration does not match the installed contract")
			}
			settings, err := os.ReadFile(filepath.Join(root, "settings.json"))
			if err != nil || gjson.GetBytes(settings, "model").String() != "custom-local:test-model" {
				t.Fatal("WorkBuddy default model selection was not configured")
			}
			firstRecord := workBuddyCLIRecord(t, state)
			repeatDigest := previewDigest(t, preview)
			if err := run(append(apply, "--plan-digest", repeatDigest), &output); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstRecord, workBuddyCLIRecord(t, state)) {
				t.Fatal("repeat apply replaced the first original backup")
			}
			for _, secret := range []string{workBuddyCLIManagement, "fixture-user-key-private", "fixture-other-key-private"} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("CLI output disclosed a credential")
				}
			}
			// A later unrelated edit must survive offline recovery in both cases.
			settings, err = edit.PatchJSON(settings, []edit.KV{{Path: "theme", Value: "later-user-change"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			server.Close()
			t.Setenv("WORKBUDDY_CLI_MANAGEMENT_FIXTURE", "")
			oldWorkBuddyChecker := workBuddyHostChecker
			workBuddyHostChecker = func(string) routerhost.Checker { panic("offline recovery constructed a WorkBuddy checker") }
			t.Cleanup(func() { workBuddyHostChecker = oldWorkBuddyChecker })
			recovery := workBuddyCLIArgs("restore-preview", root, state, "")
			restoreDigest := previewDigest(t, recovery)
			if err := run(append(workBuddyCLIArgs("restore", root, state, ""), "--plan-digest", restoreDigest), &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "bypasses API-router") {
				t.Fatal("recovery omitted its bypass notice")
			}
			settings, err = os.ReadFile(filepath.Join(root, "settings.json"))
			if err != nil || gjson.GetBytes(settings, "theme").String() != "later-user-change" {
				t.Fatal("offline recovery lost a later unrelated setting")
			}
			if existing {
				models, err = os.ReadFile(filepath.Join(root, "models.json"))
				if err != nil || !bytes.Equal(models, originals["models.json"]) || gjson.GetBytes(settings, "model").String() != "original-model" || !bytes.Contains(settings, []byte("keep user comment")) {
					t.Fatal("offline recovery did not restore original fields and comments")
				}
			} else {
				if _, err := os.Stat(filepath.Join(root, "models.json")); !os.IsNotExist(err) || gjson.GetBytes(settings, "model").Exists() {
					t.Fatal("offline recovery retained tool-created connection fields")
				}
			}
		})
	}
}

func TestWorkBuddyHostCLIFailedChecksDoNotWrite(t *testing.T) {
	for _, failure := range []string{"app-model", "business-status", "business-empty"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", failure, existing), func(t *testing.T) {
				base := t.TempDir()
				root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
				var originals map[string][]byte
				if existing {
					originals = workBuddyCLIOriginals(t, root)
				}
				t.Setenv("WORKBUDDY_CLI_MANAGEMENT_FIXTURE", workBuddyCLIManagement)
				var calls atomic.Int32
				server := workBuddyCLIFixture(t, failure, &calls)
				digest := previewDigest(t, workBuddyCLIArgs("preview", root, state, server.URL))
				var output bytes.Buffer
				err := run(append(workBuddyCLIArgs("apply", root, state, server.URL), "--management-token-env", "WORKBUDDY_CLI_MANAGEMENT_FIXTURE", "--plan-digest", digest), &output)
				if err == nil || strings.Contains(err.Error()+output.String(), workBuddyCLIManagement) || strings.Contains(err.Error()+output.String(), "fixture-private-cloud-error") {
					t.Fatalf("failed check accepted or disclosed a secret: %v", err)
				}
				want := int32(3)
				if failure == "app-model" {
					want = 2
				}
				if calls.Load() != want {
					t.Fatalf("checks=%d, want=%d", calls.Load(), want)
				}
				if _, err := os.Stat(state); !os.IsNotExist(err) {
					t.Fatal("failed checks created backup state")
				}
				if !existing {
					if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("failed checks created host configuration")
					}
				} else {
					entries, err := os.ReadDir(root)
					if err != nil || len(entries) != 2 {
						t.Fatal("failed checks left host operation files")
					}
					for name, original := range originals {
						raw, err := os.ReadFile(filepath.Join(root, name))
						if err != nil || !bytes.Equal(raw, original) {
							t.Fatal("failed checks changed original host files")
						}
					}
				}
			})
		}
	}
}

func TestWorkBuddyHostCLIHelpAndDangerousFlagsNeverWrite(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
	for _, action := range []string{"preview", "apply", "restore-preview", "restore"} {
		var output bytes.Buffer
		if err := run(append(workBuddyCLIArgs(action, root, state, "http://127.0.0.1:8317"), "--help"), &output); err != nil || !strings.Contains(output.String(), "WorkBuddy 5.3.8") {
			t.Fatalf("WorkBuddy help: %s %v", output.String(), err)
		}
	}
	valid := workBuddyCLIArgs("preview", root, state, "http://127.0.0.1:8317")
	for _, extra := range [][]string{
		{"--force"}, {"--skip-checks"}, {"--unknown", "fixture-private-cli-value"},
		{"--token", "fixture-private-cli-value"}, {"--management-token", "fixture-private-cli-value"},
		{"--token-env", "WORKBUDDY_CLI_MANAGEMENT_FIXTURE"}, {"--model", "--bad-model"},
		{"--model", "custom-local:test-model"}, {"--config-root", "relative"}, {"--state-dir", "relative"},
	} {
		var output bytes.Buffer
		err := run(append(append([]string(nil), valid...), extra...), &output)
		if err == nil || strings.Contains(err.Error()+output.String(), "fixture-private-cli-value") {
			t.Fatalf("dangerous option accepted or exposed: %v", err)
		}
	}
	for _, path := range []string{root, state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("help or invalid options wrote host/recovery files")
		}
	}
}

func TestWorkBuddyHostCLIStalePreviewStopsBeforeChecks(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "host"), filepath.Join(base, "records")
	var calls atomic.Int32
	server := workBuddyCLIFixture(t, "", &calls)
	digest := previewDigest(t, workBuddyCLIArgs("preview", root, state, server.URL))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"model":"later-user-choice"}`)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_CLI_MANAGEMENT_FIXTURE", workBuddyCLIManagement)
	var output bytes.Buffer
	err := run(append(workBuddyCLIArgs("apply", root, state, server.URL), "--management-token-env", "WORKBUDDY_CLI_MANAGEMENT_FIXTURE", "--plan-digest", digest), &output)
	if err == nil || !strings.Contains(err.Error(), "plan changed") || calls.Load() != 0 {
		t.Fatalf("stale plan reached checks or writes: calls=%d err=%v", calls.Load(), err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("stale plan created backup state")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("stale plan created a host file")
	}
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("stale plan overwrote the later user choice")
	}
}
