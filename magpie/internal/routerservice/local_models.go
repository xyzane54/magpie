package routerservice

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdevice"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
)

// LocalModels is one Router-owned manager, shared across all applications.
// It is independent of host adapters, provider credentials and redaction intent.
type LocalModels struct {
	Manager       *routerruntime.Manager
	Detector      *routerdevice.Detector
	Layout        dirs.Layout
	Catalog       routermanifest.Catalog
	Checks        []routerruntime.Check // Registered by W4/W5, never supplied over HTTP.
	Observer      routerruntime.LocalCallObserver
	dynamicChecks bool
	checkFactory  func(routerruntime.LocalCallIdentity, string) []routerruntime.Check
}

func newLocalModels(ctx context.Context, l dirs.Layout, store *routerconfig.Store, observed ...func(routerprocess.Process, routerprocess.Spec)) (*LocalModels, error) {
	return newLocalModelsWithTrial(ctx, l, store, false, observed...)
}

func newLocalModelsWithTrial(ctx context.Context, l dirs.Layout, store *routerconfig.Store, experimental bool, observed ...func(routerprocess.Process, routerprocess.Spec)) (*LocalModels, error) {
	catalog := routermanifest.Fixed()
	var onStart func(routerprocess.Process, routerprocess.Spec)
	if len(observed) > 0 {
		onStart = observed[0]
	}
	manager, err := routerruntime.New(ctx, routerruntime.Options{Catalog: catalog, RuntimeID: routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH), Store: routerruntime.ConfigIntentStore{Store: store}, Installer: &routerruntime.FileInstaller{Layout: l, Downloader: &routerdownload.Downloader{}}, Factory: routerprocess.NativeFactory{OnStart: onStart}, ExperimentalRedaction: experimental})
	if err != nil {
		return nil, err
	}
	return &LocalModels{Manager: manager, Detector: routerdevice.Native(), Layout: l, Catalog: catalog, dynamicChecks: true, checkFactory: localCapabilityChecks}, nil
}
func (l *LocalModels) detect(ctx context.Context, path string) routerdevice.Report {
	var requirements routerdevice.Requirements
	for _, m := range l.Catalog.Models {
		if m.Tier == "1.7B" {
			requirements.Tier1_7BBytes = uint64(m.InstallationBytes)
		}
		if m.Tier == "4B" {
			requirements.Tier4BBytes = uint64(m.InstallationBytes)
		}
	}
	return l.Detector.Detect(ctx, path, requirements)
}

func localModelPath(r *http.Request) bool {
	switch r.URL.EscapedPath() {
	case "/api/local-model", "/api/local-model/settings", "/api/local-model/detect", "/api/local-model/select", "/api/local-model/enable", "/api/local-model/install", "/api/local-model/cancel", "/api/local-model/load", "/api/local-model/disable", "/api/local-model/health", "/api/local-model/check", "/api/local-model/trial/enable", "/api/local-model/trial/disable":
		return true
	}
	return false
}
func localModelRead(r *http.Request) bool {
	return r.URL.Path == "/api/local-model" || r.URL.Path == "/api/local-model/settings" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

func localModelAPI(w http.ResponseWriter, r *http.Request, l *LocalModels) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code string, status int) {
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
		}
	}
	if l == nil || l.Manager == nil {
		fail("local_runtime_unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.URL.RawQuery != "" {
		fail("invalid_query", 400)
		return
	}
	if localModelRead(r) {
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "status": l.Manager.Status(), "catalog": l.Catalog, "models": aggregateLocalModelCandidates(r.Context(), nil, l)})
		}
		return
	}
	if r.URL.Path == "/api/local-model/settings" {
		settings, err := decodeLocalRuntimeSettings(w, r)
		if err != nil {
			fail("invalid_runtime_settings", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err = l.Manager.UpdateSettings(ctx, settings); err == nil {
			status := l.Manager.Status()
			if status.Intent.Enabled && status.Installation == "installed" {
				err = l.Manager.Load(ctx)
			}
		}
		if err != nil {
			code, status := "local_runtime_settings_apply_failed", http.StatusConflict
			if errors.Is(err, context.DeadlineExceeded) {
				code, status = "local_runtime_timed_out", http.StatusGatewayTimeout
			}
			if errors.Is(err, context.Canceled) {
				code, status = "local_runtime_canceled", 499
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "error": map[string]string{"code": code}, "status": l.Manager.Status(), "catalog": l.Catalog, "models": aggregateLocalModelCandidates(r.Context(), nil, l)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "status": l.Manager.Status(), "catalog": l.Catalog, "models": aggregateLocalModelCandidates(r.Context(), nil, l)})
		return
	}
	var input struct {
		Model     string   `json:"model"`
		Judgment  *bool    `json:"judgment"`
		Execution *bool    `json:"execution"`
		Purposes  []string `json:"purposes"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			fail("invalid_request", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			fail("invalid_request", 400)
			return
		}
	}
	ctx := r.Context()
	var err error
	status := l.Manager.Status()
	switch r.URL.Path {
	case "/api/local-model/detect":
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "device": l.detect(ctx, l.Layout.Models)})
		return
	case "/api/local-model/select":
		err = l.Manager.Select(ctx, input.Model)
	case "/api/local-model/enable":
		model, ok := l.Catalog.Model(status.Intent.Selected)
		if !ok {
			fail("model_selection_required", 409)
			return
		}
		report := l.detect(ctx, l.Layout.Models)
		if report.Detection.State != routerdevice.CheckPassed || !report.Recommendation.Allows(routerdevice.Tier(model.Tier)) {
			fail("device_check_not_passed", 409)
			return
		}
		err = l.Manager.EnableSelected(ctx, status.Intent.Selected)
	case "/api/local-model/install":
		// Model and runtime can be on separate volumes. Recheck both explicit
		// destinations immediately before installation, using the T3.1 provider.
		model, _ := l.Catalog.Model(status.Intent.Selected)
		for _, path := range []string{l.Layout.Models, l.Layout.Runtime} {
			report := l.detect(ctx, path)
			if report.Detection.State != routerdevice.CheckPassed || (model.Tier != "" && !report.Recommendation.Allows(routerdevice.Tier(model.Tier))) {
				fail("device_check_not_passed", 409)
				return
			}
		}
		err = l.Manager.Install()
	case "/api/local-model/cancel":
		err = l.Manager.CancelInstall(ctx)
	case "/api/local-model/load":
		err = l.Manager.Load(ctx)
	case "/api/local-model/disable":
		drain, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = l.Manager.Disable(drain)
	case "/api/local-model/health":
		err = l.Manager.Health(ctx)
	case "/api/local-model/trial/enable":
		purposes, parseErr := localTrialPurposes(input.Judgment, input.Execution, input.Purposes)
		if parseErr != nil {
			fail("invalid_request", http.StatusBadRequest)
			return
		}
		err = l.Manager.EnableTrial(ctx, purposes...)
	case "/api/local-model/trial/disable":
		purposes, parseErr := localTrialPurposes(input.Judgment, input.Execution, input.Purposes)
		if parseErr != nil {
			fail("invalid_request", http.StatusBadRequest)
			return
		}
		err = l.Manager.DisableTrial(ctx, purposes...)
	case "/api/local-model/check":
		// Bind the shared server-owned contracts and their prompt to one
		// selection/settings snapshot. HTTP cannot invent capability evidence.
		identity, settings := l.Manager.SnapshotBinding(ctx)
		promptVersion := localPromptVersion(settings)
		checks := append([]routerruntime.Check(nil), l.Checks...)
		factory := l.checkFactory
		if factory == nil && l.dynamicChecks {
			factory = localCapabilityChecks
			checks = nil
		}
		if factory != nil {
			checks = append(factory(identity, promptVersion), checks...)
		}
		if len(checks) == 0 {
			fail("capability_acceptance_suites_not_provided", http.StatusConflict)
			return
		}
		for _, check := range checks {
			run := check.Run
			check.Run = func(ctx context.Context, endpoint string) (string, error) {
				// This executes inside the acquired slot. A concurrent model or
				// settings change must not attach stale evidence to its replacement.
				currentIdentity, currentSettings := l.Manager.SnapshotBinding(ctx)
				if currentIdentity != identity || currentSettings != settings || run == nil {
					return "", routerruntime.ErrCapability
				}
				return run(ctx, endpoint)
			}
			checkCtx := routerruntime.WithRuntimeSettings(ctx, settings)
			checkCtx = routerruntime.WithLocalCallContext(checkCtx, routerruntime.LocalCallContext{Check: true, Mode: routerruntime.LocalCallDiagnostic, PromptVersion: promptVersion, Material: check.Suite})
			material := localJudgmentMaterial
			if check.Purpose == routerruntime.Execution {
				material = localExecutionMaterial
			}
			checkCtx = withLocalCheckCallObserver(checkCtx, localCheckCallObserver{target: l.Observer, manager: l.Manager, purpose: check.Purpose, suite: check.Suite, prompt: promptVersion, material: material})
			checkErr := l.Manager.CheckCapability(checkCtx, check)
			err = errors.Join(err, checkErr)
		}
	}
	if err != nil {
		code, status := "local_runtime_operation_failed", http.StatusConflict
		for _, known := range []error{routerruntime.ErrDisabled, routerruntime.ErrNotInstalled, routerruntime.ErrQueueFull, routerruntime.ErrBusy, routerruntime.ErrFault} {
			if errors.Is(err, known) {
				code = known.Error()
				break
			}
		}
		var dependency *routerprocess.DependencyError
		if errors.As(err, &dependency) {
			code = dependency.Code
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code, status = "local_runtime_timed_out", 504
		}
		if errors.Is(err, context.Canceled) {
			code, status = "local_runtime_canceled", 499
		}
		fail(code, status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "status": l.Manager.Status(), "models": aggregateLocalModelCandidates(r.Context(), nil, l)})
}

// Settings updates are complete, integer-only objects. Reject duplicate keys,
// nulls, missing fields and trailing JSON before changing saved or active state.
func decodeLocalRuntimeSettings(w http.ResponseWriter, r *http.Request) (routerconfig.RuntimeSettings, error) {
	var settings routerconfig.RuntimeSettings
	if r.Body == nil {
		return settings, errors.New("runtime settings body required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return settings, errors.New("invalid runtime settings")
	}
	fields := map[string]*int{
		"contextTokens": &settings.ContextTokens, "judgmentQueueSeconds": &settings.JudgmentQueueSeconds,
		"judgmentInferenceSeconds": &settings.JudgmentInferenceSeconds, "executionQueueSeconds": &settings.ExecutionQueueSeconds,
		"executionSeconds": &settings.ExecutionSeconds, "loadSeconds": &settings.LoadSeconds,
		"requestSeconds": &settings.RequestSeconds, "cloudReserveSeconds": &settings.CloudReserveSeconds,
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		field := fields[name]
		if err != nil || !ok || field == nil || seen[name] {
			return settings, errors.New("invalid runtime settings field")
		}
		var value *int
		if decoder.Decode(&value) != nil || value == nil || *value <= 0 {
			return settings, errors.New("runtime setting must be a positive integer")
		}
		*field, seen[name] = *value, true
	}
	if _, err := decoder.Token(); err != nil || len(seen) != len(fields) {
		return settings, errors.New("incomplete runtime settings")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return settings, errors.New("trailing runtime settings")
	}
	return settings, settings.Validate()
}

func localTrialPurposes(judgment, execution *bool, names []string) ([]routerruntime.Purpose, error) {
	if judgment == nil && execution == nil && len(names) == 0 {
		return []routerruntime.Purpose{routerruntime.Judgment, routerruntime.Execution}, nil
	}
	seen := map[routerruntime.Purpose]bool{}
	add := func(p routerruntime.Purpose, enabled bool) {
		if enabled {
			seen[p] = true
		}
	}
	if judgment != nil {
		add(routerruntime.Judgment, *judgment)
	}
	if execution != nil {
		add(routerruntime.Execution, *execution)
	}
	for _, name := range names {
		switch name {
		case string(routerruntime.Judgment):
			seen[routerruntime.Judgment] = true
		case string(routerruntime.Execution):
			seen[routerruntime.Execution] = true
		default:
			return nil, errors.New("invalid trial purpose")
		}
	}
	out := make([]routerruntime.Purpose, 0, len(seen))
	for _, p := range []routerruntime.Purpose{routerruntime.Judgment, routerruntime.Execution} {
		if seen[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("trial purpose required")
	}
	return out, nil
}

// A single ticker checks only idle release; it never enables, downloads or
// starts a model. The service owns and joins it before releasing the manager.
func localIdleLoop(ctx context.Context, l *LocalModels, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stop, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = l.Manager.Sweep(stop)
			cancel()
		}
	}
}

//go:embed assets/local-model.js
var localModelScript []byte
