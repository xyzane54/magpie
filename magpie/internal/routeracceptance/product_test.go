package routeracceptance

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
)

func productOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{Model: "spark-x2.5-1.7B", DataDir: filepath.Join(root, "product"), Output: filepath.Join(root, "evidence", "result.json"), ModelFile: filepath.Join(root, "missing.gguf"), TemplateFile: filepath.Join(root, "missing.jinja"), RuntimeArchive: filepath.Join(root, "missing.zip"), LoadTimeout: time.Minute, GenerationTimeout: 2 * time.Minute, InstallTimeout: time.Minute}
}

func TestProductPreparationUsesHTTPAndPreservesAll23Assertions(t *testing.T) {
	o := productOptions(t)
	o.PrepareOnly = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Product || len(report.Assertions) != len(assertionNames)+len(dependencyAssertions) {
		t.Fatalf("product suite dropped or invented baseline assertions: %+v", report.Assertions)
	}
	names := []string{}
	for _, a := range report.Assertions[:len(assertionNames)] {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, assertionNames) {
		t.Fatal("baseline 23 assertion identities changed", names)
	}
	actions := []string{}
	for _, request := range report.Management {
		actions = append(actions, request.Action)
		if request.HTTPStatus != 200 {
			t.Fatalf("preparation management failed: %+v", request)
		}
	}
	if !reflect.DeepEqual(actions, []string{"status", "disable"}) {
		t.Fatal("preparation did not use real management HTTP or performed extra actions", actions)
	}
	for _, a := range report.Assertions {
		switch a.Name {
		case "startup_no_load", "disabled_start", "dependencies_default_no_install", "dependency_pins", "dependencies_no_manual_environment", "close_reaps", "closed_no_restart", "capabilities_remain_unknown", "cleanup":
			if a.State != "pass" {
				t.Fatalf("preparation failed: %+v", a)
			}
		default:
			if a.State != "not_run" {
				t.Fatalf("preparation pretended to install or infer: %+v", a)
			}
		}
	}
	if len(report.Processes) != 0 || report.FinalStatus.Error != "" {
		t.Fatal("preparation started a native process or retained an error", report.FinalStatus)
	}
	if err := WriteReport(o.Output, report, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(o.Output, report, nil); err == nil {
		t.Fatal("report publication overwrote earlier evidence")
	}
}

func TestProductEvidenceRefusesOldRawResponses(t *testing.T) {
	o := productOptions(t)
	if err := os.MkdirAll(filepath.Dir(o.Output), 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(filepath.Dir(o.Output), "normal.json")
	if err := os.WriteFile(old, []byte("previous evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "pre-existing evidence") {
		t.Fatal("old evidence was accepted", err)
	}
	if err := WriteReport(o.Output, report, err); err == nil {
		t.Fatal("failed reservation published into old evidence")
	}
	data, _ := os.ReadFile(old)
	if !bytes.Equal(data, []byte("previous evidence")) {
		t.Fatal("old response changed", string(data))
	}
	if _, err := os.Stat(o.DataDir); !os.IsNotExist(err) {
		t.Fatal("refused evidence initialized product data", err)
	}
}

func TestProductMissingSourceStillJoinsFormalService(t *testing.T) {
	o := productOptions(t)
	report, err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "source_verification") {
		t.Fatal("missing source did not fail explicitly", err)
	}
	if len(report.Processes) != 0 || report.FinalStatus.PID != 0 || report.FinalStatus.Error != "" {
		t.Fatal("failed setup left product native state", report.FinalStatus)
	}
	for _, a := range report.Assertions {
		if a.Name == "cleanup" && a.State != "pass" {
			t.Fatal("failed setup did not close the service", a)
		}
	}
	if _, err := os.Stat(report.Layout.ServiceLock()); !os.IsNotExist(err) {
		t.Fatal("formal service lock survived diagnostic error", err)
	}
}

func TestProductDoesNotExposeDiagnosticHTTP(t *testing.T) {
	root := t.TempDir()
	layout := dirs.Layout{Install: root, Data: root, Config: filepath.Join(root, "config"), Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "logs")}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := routerconfig.New(layout).Initialize(); err != nil {
		t.Fatal(err)
	}
	session, err := startProduct(context.Background(), layout, &trackingFactory{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := session.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{"/api/local-model/generate", "/api/local-model/diagnose", "/api/local-model/invoke", "/api/local-model/diagnostics"} {
		request, _ := http.NewRequest(http.MethodPost, session.base+path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+session.token)
		response, err := session.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("diagnostic seam was exposed over HTTP: %s %d", path, response.StatusCode)
		}
	}
	request, _ := http.NewRequest(http.MethodPost, session.base+"/api/local-model/check", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+session.token)
	response, err := session.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatal("HTTP acquired unregistered capability acceptance", response.StatusCode)
	}
}
