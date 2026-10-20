package routerservice

import (
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routercontrol"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerruntime"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceControlActionsKeepLocalManagementBoundary(t *testing.T) {
	var stopped atomic.Bool
	l, address := fixture(t)
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address, ProcessStamp: "one"}, "secret", routerconfig.New(l), ModelOptions{serviceControl: &serviceController{layout: l, stop: func() { stopped.Store(true) }}})
	for _, path := range []string{"/api/service/stop", "/api/service/restart", "/api/service/autostart"} {
		for _, tc := range []struct {
			name, method, host, remote, origin, auth string
			code                                     int
		}{
			{"no token", "POST", address, "127.0.0.1:2", "", "", 401},
			{"foreign origin", "POST", address, "127.0.0.1:2", "http://evil.test", "Bearer secret", 403},
			{"null origin", "POST", address, "127.0.0.1:2", "null", "Bearer secret", 403},
			{"wrong host", "POST", "evil.test", "127.0.0.1:2", "", "Bearer secret", 403},
			{"remote peer", "POST", address, "192.0.2.1:2", "", "Bearer secret", 403},
			{"GET cannot act", "GET", address, "127.0.0.1:2", "", "Bearer secret", 405},
		} {
			t.Run(path+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(tc.method, "http://"+address+path, strings.NewReader(`{"enabled":true}`))
				r.Host = tc.host
				r.RemoteAddr = tc.remote
				r.Header.Set("Authorization", tc.auth)
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.code {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	if stopped.Load() {
		t.Fatal("unauthorized request stopped service")
	}
	r := httptest.NewRequest("POST", "http://"+address+"/api/service/stop", nil)
	r.RemoteAddr = "127.0.0.1:2"
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("X-API-Router-Instance", "old")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 || stopped.Load() {
		t.Fatal("old instance controlled", w.Code)
	}
}

func TestServiceControlHTTPStopReleasesInstanceAndPreservesTrial(t *testing.T) {
	l, address := fixture(t)
	before, err := os.ReadFile(l.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan Info, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunWithModelOptions(ctx, l, "control-test", func(i Info) error { ready <- i; return nil }, ModelOptions{ExperimentalRedaction: true})
	}()
	var info Info
	select {
	case info = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("no ready")
	}
	token, err := os.ReadFile(TokenFile(l))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "http://"+address+"/api/service/stop", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("stop HTTP %d", resp.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not release service")
	}
	if info.PID != os.Getpid() || info.ProcessStamp == "" || !info.Running {
		t.Fatal("missing actual process identity", info)
	}
	if _, err := os.Stat(l.ServiceLock()); !os.IsNotExist(err) {
		t.Fatal("stop leaked lock", err)
	}
	after, _ := os.ReadFile(l.ConfigFile())
	if string(before) != string(after) {
		t.Fatal("stop changed product/model intent")
	}
	var options struct {
		ExperimentalRedaction bool `json:"experimentalRedaction"`
	}
	b, err := os.ReadFile(filepath.Join(l.State, "service-options.json"))
	if err != nil || json.Unmarshal(b, &options) != nil || !options.ExperimentalRedaction {
		t.Fatal("trial choice lost on HTTP stop", err)
	}
	for _, dir := range []string{l.Models, l.Runtime} {
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 0 {
			t.Fatal("control acquired model artifacts", dir, files, err)
		}
	}
}

func TestServiceControlStopReclaimsActiveAndQueuedManager(t *testing.T) {
	l, address := fixture(t)
	store := routerconfig.New(l)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer local.Close()
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH), Store: routerruntime.ConfigIntentStore{Store: store}, Installer: integrationInstaller{}, Factory: integrationFactory{endpoint: local.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.Select(context.Background(), "spark-x2.5-1.7B"); err != nil {
		t.Fatal(err)
	}
	if err = manager.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := make(chan Info, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunWithModelOptions(ctx, l, "queued-test", func(i Info) error { started <- i; return nil }, ModelOptions{LocalRuntime: &LocalModels{Manager: manager, Layout: l, Catalog: catalog}})
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- manager.Generate(ctx, routerruntime.Execution, []byte(`{"messages":[],"stream":true}`), func(resp *http.Response) error { _, err := io.Copy(io.Discard, resp.Body); return err })
		}()
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for manager.Status().Active != 1 || manager.Status().Waiting != 1 {
		select {
		case <-ctx.Done():
			t.Fatal("active/queued state not reached", manager.Status())
		case <-tick.C:
		}
	}
	token, err := os.ReadFile(TokenFile(l))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+address+"/api/service/stop", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("active/queued call falsely succeeded")
			}
		case <-ctx.Done():
			t.Fatal("manager call leaked")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("service stop leaked manager")
	}
	status := manager.Status()
	if status.Active != 0 || status.Waiting != 0 || status.PID != 0 {
		t.Fatal("manager was not reclaimed", status)
	}
	if _, err := os.Stat(routercontrol.RecordFile(l)); !os.IsNotExist(err) {
		t.Fatal("instance metadata leaked", err)
	}
}
