package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdevice"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
)

type managedFixtureInstaller struct {
	installed atomic.Bool
	calls     atomic.Int32
}

func (i *managedFixtureInstaller) Verify(context.Context, routermanifest.Model, routermanifest.Runtime) (routerprocess.Spec, error) {
	if !i.installed.Load() {
		return routerprocess.Spec{}, errors.New("missing fixture")
	}
	return routerprocess.Spec{}, nil
}
func (i *managedFixtureInstaller) Install(ctx context.Context, _ routermanifest.Model, _ routermanifest.Runtime, progress func(routerruntime.Progress)) (routerprocess.Spec, error) {
	i.calls.Add(1)
	progress(routerruntime.Progress{Downloaded: 1, Total: 2})
	<-ctx.Done()
	return routerprocess.Spec{}, ctx.Err()
}

type managedFixtureProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *managedFixtureProcess) Endpoint() string             { return "http://127.0.0.1:1" }
func (p *managedFixtureProcess) PID() int                     { return 123 }
func (p *managedFixtureProcess) Done() <-chan struct{}        { return p.done }
func (p *managedFixtureProcess) Err() error                   { return nil }
func (p *managedFixtureProcess) Health(context.Context) error { return nil }
func (p *managedFixtureProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type managedFixtureFactory struct{ calls atomic.Int32 }

func (f *managedFixtureFactory) Start(context.Context, routerprocess.Spec) (routerprocess.Process, error) {
	f.calls.Add(1)
	return &managedFixtureProcess{done: make(chan struct{})}, nil
}

func TestLocalRuntimeSettingsAPIValidationPersistenceAndReload(t *testing.T) {
	l, address := fixture(t)
	store := routerconfig.New(l)
	installer := &managedFixtureInstaller{}
	installer.installed.Store(true)
	factory := &managedFixtureFactory{}
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: "windows-amd64-cpu", Store: routerruntime.ConfigIntentStore{Store: store}, Installer: installer, Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Select(context.Background(), localModelID); err != nil {
		t.Fatal(err)
	}
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "settings-fixture", store, ModelOptions{LocalRuntime: &LocalModels{Manager: manager, Layout: l, Catalog: catalog}})
	request := func(method, body, auth, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+address+"/api/local-model/settings", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:2345"
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	auth := "Bearer settings-fixture"
	settings := routerconfig.LongContextRuntimeSettings()
	body, _ := json.Marshal(settings)
	for _, method := range []string{"GET", "HEAD", "POST"} {
		if w := request(method, string(body), "", ""); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", method, w.Code)
		}
	}
	for _, origin := range []string{"https://foreign.example", "null"} {
		if w := request("POST", string(body), auth, origin); w.Code != 403 {
			t.Fatalf("foreign origin: %d", w.Code)
		}
	}
	if w := request("DELETE", "", auth, ""); w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD, POST" {
		t.Fatalf("methods: %d %s", w.Code, w.Header().Get("Allow"))
	}
	for _, bad := range []string{"", "null", "{}", string(body) + "{}", strings.Replace(string(body), `"contextTokens":65536`, `"contextTokens":65536,"contextTokens":8192`, 1), strings.Replace(string(body), `"contextTokens":65536`, `"contextTokens":null`, 1), strings.Replace(string(body), `"contextTokens":65536`, `"contextTokens":65536.5`, 1), strings.Replace(string(body), `"contextTokens":65536`, `"contextTokens":"65536"`, 1), strings.Replace(string(body), `"contextTokens":65536`, `"contextTokens":1048576`, 1), strings.Replace(string(body), `"cloudReserveSeconds":60`, `"cloudReserveSeconds":600`, 1), strings.Replace(string(body), `"loadSeconds":90`, `"unknown":90`, 1)} {
		if w := request("POST", bad, auth, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_runtime_settings") {
			t.Fatalf("invalid settings accepted: %d %s input=%s", w.Code, w.Body, bad)
		}
	}
	if !manager.Settings().IsZero() || factory.calls.Load() != 0 {
		t.Fatal("invalid settings changed runtime")
	}
	if w := request("POST", string(body), auth, ""); w.Code != 200 {
		t.Fatalf("save disabled: %d %s", w.Code, w.Body)
	}
	if manager.Settings() != settings || factory.calls.Load() != 0 || installer.calls.Load() != 0 {
		t.Fatal("saving disabled settings enabled or downloaded resources")
	}
	if err := manager.EnableSelected(context.Background(), localModelID); err != nil {
		t.Fatal(err)
	}
	settings.ContextTokens = 32768
	body, _ = json.Marshal(settings)
	if w := request("POST", string(body), auth, ""); w.Code != 200 {
		t.Fatalf("save and reload: %d %s", w.Code, w.Body)
	}
	if manager.Settings() != settings || factory.calls.Load() != 1 || installer.calls.Load() != 0 {
		t.Fatal("settings did not reload exactly once without download")
	}
	if w := request("GET", "", auth, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"contextTokens":32768`) || !strings.Contains(w.Body.String(), `"effectiveSettings"`) || !strings.Contains(w.Body.String(), `"catalog"`) {
		t.Fatalf("read settings: %d %s", w.Code, w.Body)
	}
	if w := request("HEAD", "", auth, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("head settings: %d %s", w.Code, w.Body)
	}
	loaded, err := (routerruntime.ConfigIntentStore{Store: store}).Load(context.Background())
	if err != nil || loaded.Settings != settings || !loaded.Enabled {
		t.Fatalf("settings not persisted: %+v %v", loaded, err)
	}
}

func TestLocalModelManagementConsentBoundariesAndCapabilitySeparation(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		t.Run(modelID, func(t *testing.T) {
			testLocalModelManagement(t, modelID)
		})
	}
}

func testLocalModelManagement(t *testing.T, modelID string) {
	l, address := fixture(t)
	store := routerconfig.New(l)
	if err := store.Update(func(d routerconfig.Document) error { d["redaction"] = json.RawMessage(`{"enabled":true}`); return nil }); err != nil {
		t.Fatal(err)
	}
	installer := &managedFixtureInstaller{}
	factory := &managedFixtureFactory{}
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: "windows-amd64-cpu", Store: routerruntime.ConfigIntentStore{Store: store}, Installer: installer, Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	device := routerdevice.New(routerdevice.ProviderFunc(func(context.Context, string) (routerdevice.Snapshot, error) {
		return routerdevice.Snapshot{System: routerdevice.Known(routerdevice.System{OS: "windows", Version: routerdevice.Known("10.0.26200"), Architecture: routerdevice.Known("amd64")}), MemoryBytes: routerdevice.Known(uint64(32) * routerdevice.GiB), DiskFreeBytes: routerdevice.Known(uint64(1) << 40), Backends: routerdevice.Known([]routerdevice.Backend{{Kind: routerdevice.CPU, State: routerdevice.Available}})}, nil
	}))
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "management-fixture", store, ModelOptions{LocalRuntime: &LocalModels{Manager: manager, Detector: device, Layout: l, Catalog: catalog}})
	request := func(method, path, body, auth, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:2345"
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	auth := "Bearer management-fixture"
	for _, path := range []string{"/api/local-model", "/api/local-model/detect", "/api/local-model/enable", "/api/local-model/install", "/api/local-model/cancel", "/api/local-model/load", "/api/local-model/disable", "/api/local-model/check"} {
		method := "POST"
		if path == "/api/local-model" {
			method = "GET"
		}
		if w := request(method, path, "{}", "", ""); w.Code != 401 {
			t.Fatalf("unauthorized %s: %d", path, w.Code)
		}
	}
	if w := request("POST", "/api/local-model/enable", "{}", auth, "https://foreign.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/local-model/enable", "", auth, ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/%61pi/local-model", "", auth, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/local-model/select", `{"model":"spark-x2.5-1.7B","url":"https://example.invalid"}`, auth, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, op := range []struct {
		path, body string
		status     int
	}{
		{"/api/local-model", "", 200}, {"/api/local-model/detect", "{}", 200}, {"/api/local-model/install", "{}", 409}, {"/api/local-model/load", "{}", 409},
		{"/api/local-model/select", `{"model":"` + modelID + `"}`, 200},
	} {
		method := "POST"
		if op.path == "/api/local-model" {
			method = "GET"
		}
		w := request(method, op.path, op.body, auth, "")
		if w.Code != op.status {
			t.Fatalf("%s %d %s", op.path, w.Code, w.Body)
		}
	}
	if installer.calls.Load() != 0 || factory.calls.Load() != 0 || manager.Status().Intent.Enabled {
		t.Fatal("management reads/detection/selection enabled resources")
	}
	for _, path := range []string{"/api/local-model/enable", "/api/local-model/install", "/api/local-model/cancel"} {
		w := request("POST", path, "{}", auth, "")
		if w.Code != 200 {
			t.Fatalf("%s %d %s", path, w.Code, w.Body)
		}
	}
	if manager.Status().Installation != "canceled" {
		t.Fatal(manager.Status())
	}
	installer.installed.Store(true)
	if err := manager.RefreshInstallation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", "/api/local-model/load", "{}", auth, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("POST", "/api/local-model/health", "{}", auth, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if !manager.Status().Healthy || factory.calls.Load() != 1 {
		t.Fatal(manager.Status())
	}
	for _, c := range manager.Status().Capabilities {
		if c.State != "unknown" {
			t.Fatal("HTTP health passed capability")
		}
	}
	if w := request("POST", "/api/local-model/check", "{}", auth, ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/local-model/disable", "{}", auth, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("POST", "/api/local-model/load", "{}", auth, ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	d, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var redaction struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(d["redaction"], &redaction); err != nil || !redaction.Enabled {
		t.Fatal("runtime disabled application redaction", string(d["redaction"]))
	}
	if manager.Status().Intent.Enabled || factory.calls.Load() != 1 {
		t.Fatal("disabled runtime restarted")
	}
	if w := request("HEAD", "/api/local-model", "", auth, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestLocalModelTierEnablementBoundaries(t *testing.T) {
	l, address := fixture(t)
	store := routerconfig.New(l)
	installer := &managedFixtureInstaller{}
	factory := &managedFixtureFactory{}
	catalog := routermanifest.Fixed()
	manager, err := routerruntime.New(context.Background(), routerruntime.Options{Catalog: catalog, RuntimeID: "windows-amd64-cpu", Store: routerruntime.ConfigIntentStore{Store: store}, Installer: installer, Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	var snap routerdevice.Snapshot
	device := routerdevice.New(routerdevice.ProviderFunc(func(context.Context, string) (routerdevice.Snapshot, error) {
		return snap, nil
	}))
	h := handlerWithModelOptions(Info{Service: "api-router", Address: address}, "tier-fixture", store, ModelOptions{LocalRuntime: &LocalModels{Manager: manager, Detector: device, Layout: l, Catalog: catalog}})
	auth := "Bearer tier-fixture"
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+address+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:2345"
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 16 GiB RAM with sufficient available memory and 8 CPU threads recommends 1.7B (MaxTier: 4B), allowing both 1.7B and 4B.
	snap = routerdevice.Snapshot{
		System:               routerdevice.Known(routerdevice.System{OS: "windows", Version: routerdevice.Known("10.0.26200"), Architecture: routerdevice.Known("amd64"), LogicalCPUs: routerdevice.Known(8)}),
		MemoryBytes:          routerdevice.Known(uint64(16) * routerdevice.GiB),
		AvailableMemoryBytes: routerdevice.Known(uint64(6) * routerdevice.GiB),
		DiskFreeBytes:        routerdevice.Known(uint64(100) * routerdevice.GiB),
		Backends:             routerdevice.Known([]routerdevice.Backend{{Kind: routerdevice.CPU, State: routerdevice.Available}}),
	}
	if w := request("/api/local-model/select", `{"model":"spark-x2.5-4B"}`); w.Code != 200 {
		t.Fatalf("select 4B: %d %s", w.Code, w.Body)
	}
	if w := request("/api/local-model/enable", `{}`); w.Code != 200 {
		t.Fatalf("16 GiB (MaxTier=4B) rejected 4B enable: %d %s", w.Code, w.Body)
	}
	if w := request("/api/local-model/disable", `{}`); w.Code != 200 {
		t.Fatalf("disable 4B: %d %s", w.Code, w.Body)
	}

	// 16 GiB RAM with low available memory (3 GiB < 4.5 GiB) caps MaxTier at 1.7B: 4B enable is rejected, 1.7B enable succeeds.
	snap.AvailableMemoryBytes = routerdevice.Known(uint64(3) * routerdevice.GiB)
	if w := request("/api/local-model/enable", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "device_check_not_passed") {
		t.Fatalf("low available memory accepted 4B enable: %d %s", w.Code, w.Body)
	}
	if w := request("/api/local-model/select", `{"model":"spark-x2.5-1.7B"}`); w.Code != 200 {
		t.Fatalf("select 1.7B: %d %s", w.Code, w.Body)
	}
	if w := request("/api/local-model/enable", `{}`); w.Code != 200 {
		t.Fatalf("low available memory (3 GiB >= 2.5 GiB) rejected 1.7B enable: %d %s", w.Code, w.Body)
	}
	if w := request("/api/local-model/disable", `{}`); w.Code != 200 {
		t.Fatalf("disable 1.7B: %d %s", w.Code, w.Body)
	}
}
