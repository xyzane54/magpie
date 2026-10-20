// real-model-check is an internal, offline acceptance command. It never registers
// W4/W5 capability suites or changes the fixed production artifact catalog.
package routeracceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
)

type options struct {
	Model, ModelFile, TemplateFile, RuntimeArchive, DataDir, Output string
	LoadTimeout, GenerationTimeout, InstallTimeout                  time.Duration
	PrepareOnly, InstallOnly, BusinessPreacceptance                 bool
	PrivacyDiagnostic                                               string
}

type assertion struct {
	Name       string               `json:"name"`
	State      string               `json:"state"`
	Started    time.Time            `json:"started,omitempty"`
	DurationMS float64              `json:"durationMs"`
	Error      string               `json:"error,omitempty"`
	Detail     any                  `json:"detail,omitempty"`
	Status     routerruntime.Status `json:"status"`
}

type evidence struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	Started          time.Time              `json:"started"`
	Finished         time.Time              `json:"finished"`
	Options          options                `json:"options"`
	Model            routermanifest.Model   `json:"model"`
	Runtime          routermanifest.Runtime `json:"runtime"`
	Fingerprint      string                 `json:"fingerprint"`
	Layout           dirs.Layout            `json:"layout"`
	Platform         string                 `json:"platform"`
	GoVersion        string                 `json:"goVersion"`
	Build            *debug.BuildInfo       `json:"build,omitempty"`
	Assertions       []assertion            `json:"assertions"`
	Processes        []processEvidence      `json:"processes"`
	Memory           []memorySample         `json:"memory"`
	Stages           map[string]bool        `json:"stages"`
	FinalStatus      routerruntime.Status   `json:"finalStatus"`
	Error            string                 `json:"error,omitempty"`
	Scope            string                 `json:"scope"`
	Product          bool                   `json:"product"`
	Executable       string                 `json:"executable,omitempty"`
	ExecutableSHA256 string                 `json:"executableSha256,omitempty"`
	Management       []managementEvidence   `json:"management,omitempty"`
	Business         *businessPreacceptance `json:"businessPreacceptance,omitempty"`
	Privacy          *privacyRepairEvidence `json:"privacyDiagnostic,omitempty"`
	reserved         bool
}

var assertionNames = []string{
	"startup_no_load", "disabled_start", "source_verification", "select_enable",
	"capability_gate_before_load", "install", "installed_verification", "runtime_version",
	"load", "health", "normal_generation", "sse_generation", "synthetic_tool_history",
	"cancellation", "post_cancel_generation", "capability_gate_after_diagnostics", "disable_reaps", "disabled_no_restart",
	"reload_for_close", "close_reaps", "closed_no_restart", "capabilities_remain_unknown", "cleanup",
	"business_preacceptance",
}

func Main() {
	o := options{}
	flag.StringVar(&o.Model, "model", "spark-x2.5-1.7B", "fixed catalog model ID")
	flag.StringVar(&o.ModelFile, "model-file", "", "absolute path to complete, pinned GGUF cache file")
	flag.StringVar(&o.TemplateFile, "template-file", "", "absolute path to pinned chat_template.jinja cache file")
	flag.StringVar(&o.RuntimeArchive, "runtime-archive", "", "absolute path to complete b10828 archive")
	flag.StringVar(&o.DataDir, "data-dir", "", "absolute dedicated acceptance data directory")
	flag.StringVar(&o.Output, "output", "", "absolute evidence JSON path; raw responses live beside it")
	flag.DurationVar(&o.LoadTimeout, "load-timeout", 60*time.Second, "separate cold-load budget")
	flag.DurationVar(&o.GenerationTimeout, "generation-timeout", 120*time.Second, "execution generation budget")
	flag.DurationVar(&o.InstallTimeout, "install-timeout", 10*time.Minute, "bounded local import/install budget")
	flag.BoolVar(&o.PrepareOnly, "prepare-only", false, "verify isolated disabled startup/close only; no imports or model start")
	flag.BoolVar(&o.InstallOnly, "install-only", false, "perform verified local import/install and close; no model start")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report, err := run(ctx, o)
	if err != nil {
		report.Error = err.Error()
	}
	if o.Output != "" && filepath.IsAbs(o.Output) {
		if writeErr := writeJSON(o.Output, report); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			err = errors.Join(err, writeErr)
		}
	} else {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func run(ctx context.Context, o options) (evidence, error) { return runMode(ctx, o, false) }

func Run(ctx context.Context, o Options) (Report, error) { return runMode(ctx, o, true) }

type Options = options
type Report = evidence

func runMode(ctx context.Context, o options, product bool) (report evidence, result error) {
	report = evidence{SchemaVersion: 1, Started: time.Now().UTC(), Options: o, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion: runtime.Version(), Stages: map[string]bool{"completeFiles": false, "installed": false, "loaded": false, "inference": false},
		Scope: "Synthetic native diagnostic transport/lifecycle samples only. Judgment, execution and redaction capability suites remain unknown; this is not specialist quality acceptance."}
	report.Build, _ = debug.ReadBuildInfo()
	for _, name := range assertionNames {
		report.Assertions = append(report.Assertions, assertion{Name: name, State: "not_run"})
	}
	if product {
		report.Product = true
		for _, name := range dependencyAssertions {
			report.Assertions = append(report.Assertions, assertion{Name: name, State: "not_run"})
		}
		var err error
		report.Executable, report.ExecutableSHA256, err = executableHash()
		if err != nil {
			return report, err
		}
	}
	defer func() { report.Finished = time.Now().UTC() }()
	for name, path := range map[string]string{"data-dir": o.DataDir, "output": o.Output} {
		if !filepath.IsAbs(path) {
			return report, fmt.Errorf("%s must be an explicit absolute path", name)
		}
	}
	if o.LoadTimeout <= 0 || o.GenerationTimeout <= 0 || o.InstallTimeout <= 0 || (o.PrepareOnly && o.InstallOnly) {
		return report, errors.New("invalid mode or nonpositive time budget")
	}
	if err := validatePrivacyDiagnostic(o); err != nil {
		return report, err
	}
	if product {
		if err := reserveEvidence(o.Output); err != nil {
			return report, err
		}
		report.reserved = true
	}
	if err := os.MkdirAll(filepath.Dir(o.Output), 0o700); err != nil {
		return report, err
	}
	catalog := routermanifest.Fixed()
	model, ok := catalog.Model(o.Model)
	if !ok {
		return report, errors.New("model must name a fixed catalog entry")
	}
	runtimeID := routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH)
	rt, ok := catalog.Runtime(runtimeID)
	if !ok {
		return report, errors.New("no fixed native runtime for this platform")
	}
	report.Model, report.Runtime, report.Fingerprint = model, rt, model.Fingerprint(rt)
	layout := dirs.Layout{Install: o.DataDir, Config: filepath.Join(o.DataDir, "config"), Data: o.DataDir,
		Models: filepath.Join(o.DataDir, "models"), Runtime: filepath.Join(o.DataDir, "runtime"), Cache: filepath.Join(o.DataDir, "cache"),
		State: filepath.Join(o.DataDir, "state"), Logs: filepath.Join(o.DataDir, "logs")}
	report.Layout = layout
	if err := layout.Ensure(); err != nil {
		return report, err
	}
	unlock, held := filelock.Lock(filepath.Join(layout.State, "real-model-check.lock"), 0)
	if !held {
		return report, errors.New("another acceptance command owns this data directory")
	}
	defer unlock()
	store := routerconfig.New(layout)
	if _, err := store.InitializeContext(ctx); err != nil {
		return report, err
	}
	sources := []source{{rt.Artifact, o.RuntimeArchive}, {model.Artifact, o.ModelFile}, {model.Template, o.TemplateFile}}
	client, shutdown := offlineClient(sources)
	defer shutdown()
	installer := &routerruntime.FileInstaller{Layout: layout, Downloader: &routerdownload.Downloader{Client: client}}
	factory := &trackingFactory{}
	var m *routerruntime.Manager
	var session *productSession
	var startupFiles any
	var err error
	if product {
		startupFiles, err = storageInventory(layout)
		if err == nil {
			session, err = startProduct(ctx, layout, factory)
		}
		if err == nil {
			m = session.manager
		}
	} else {
		m, err = routerruntime.New(ctx, routerruntime.Options{Catalog: catalog, RuntimeID: runtimeID,
			Store: routerruntime.ConfigIntentStore{Store: store}, Installer: installer, Factory: factory,
			LoadTimeout: o.LoadTimeout, ExecutionBudget: o.GenerationTimeout})
	}
	if err != nil {
		return report, err
	}
	manage := func(call context.Context, action string) error {
		if session != nil {
			return session.action(call, action, model.ID)
		}
		switch action {
		case "select":
			return m.Select(call, model.ID)
		case "enable":
			return m.EnableSelected(call, model.ID)
		case "install":
			return m.Install()
		case "disable":
			return m.Disable(call)
		case "load":
			return m.Use(call, routerruntime.Execution, func(context.Context, string) error { return nil })
		case "health":
			return m.Health(call)
		}
		return errors.New("unknown diagnostic management operation")
	}
	closeRuntime := m.Close
	if session != nil {
		closeRuntime = session.stop
	}
	step := func(name string, fn func() (any, error)) error {
		started := time.Now()
		detail, stepErr := fn()
		a := assertion{Name: name, State: "pass", Started: started.UTC(), DurationMS: milliseconds(time.Since(started)), Detail: detail, Status: m.Status()}
		if stepErr != nil {
			a.State, a.Error = "fail", stepErr.Error()
			result = errors.Join(result, fmt.Errorf("%s: %w", name, stepErr))
		}
		for i := range report.Assertions {
			if report.Assertions[i].Name == name {
				report.Assertions[i] = a
			}
		}
		fmt.Fprintf(os.Stderr, "%s %s %.1fms\n", name, a.State, a.DurationMS)
		return stepErr
	}
	defer func() {
		_ = step("cleanup", func() (any, error) {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			closeErr := closeRuntime(cleanup)
			stopErr := factory.stopAll(cleanup)
			return nil, errors.Join(closeErr, stopErr, stopped(m), factory.observationError())
		})
		report.Processes, report.FinalStatus = factory.snapshot(), m.Status()
		if session != nil {
			report.Management = session.snapshot()
		}
	}()
	_ = step("startup_no_load", func() (any, error) {
		if session != nil {
			if err := session.action(ctx, "status", ""); err != nil {
				return nil, err
			}
		}
		return m.Status(), check(factory.count() == 0 && m.Status().PID == 0, "startup spawned a model")
	})
	if product {
		_ = step("dependencies_default_no_install", func() (any, error) {
			after, e := storageInventory(layout)
			return map[string]any{"before": startupFiles, "after": after}, errors.Join(e, check(equalJSON(startupFiles, after) && factory.count() == 0, "product startup installed or started native resources"))
		})
		_ = step("dependency_pins", func() (any, error) { return rt.Dependencies, validateDependencyPins(rt) })
		_ = step("dependencies_no_manual_environment", func() (any, error) { return nativeLoaderState() })
	}
	if step("disabled_start", func() (any, error) { return nil, manage(ctx, "disable") }) != nil {
		return report, result
	}
	if o.PrepareOnly {
		_ = step("close_reaps", func() (any, error) { return nil, errors.Join(closeRuntime(ctx), stopped(m)) })
		_ = step("closed_no_restart", func() (any, error) { return nil, rejectDisabled(ctx, m, factory) })
		_ = step("capabilities_remain_unknown", func() (any, error) { return m.Status().Capabilities, unknownCapabilities(m) })
		return report, result
	}
	if step("source_verification", func() (any, error) {
		for _, source := range sources {
			if !filepath.IsAbs(source.Path) {
				return sources, errors.New("every source path must be absolute")
			}
			if err := routerdownload.Verify(ctx, source.Artifact, source.Path); err != nil {
				return sources, fmt.Errorf("%s: %w", source.Artifact.ID, err)
			}
		}
		report.Stages["completeFiles"] = true
		return sources, nil
	}) != nil {
		return report, result
	}
	if step("select_enable", func() (any, error) {
		if err := manage(ctx, "select"); err != nil {
			return nil, err
		}
		return nil, manage(ctx, "enable")
	}) != nil {
		return report, result
	}
	_ = step("capability_gate_before_load", func() (any, error) { return m.Status().Capabilities, assertUnknownGate(ctx, m, factory) })
	if step("install", func() (any, error) {
		installCtx, cancel := context.WithTimeout(ctx, o.InstallTimeout)
		defer cancel()
		if product {
			if err := warmSources(installCtx, sources, layout, client); err != nil {
				return nil, err
			}
		}
		if err := manage(installCtx, "install"); err != nil {
			return nil, err
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			s := m.Status()
			if s.Installation == "installed" {
				if session != nil {
					if err := session.action(ctx, "status", ""); err != nil {
						return s, err
					}
				}
				report.Stages["installed"] = true
				return s, nil
			}
			if s.Installation != "downloading" {
				return s, fmt.Errorf("formal installation ended in %s: %s", s.Installation, s.Error)
			}
			select {
			case <-installCtx.Done():
				return s, installCtx.Err()
			case <-ticker.C:
			}
		}
	}) != nil {
		return report, result
	}
	var spec routerprocess.Spec
	if step("installed_verification", func() (any, error) { var e error; spec, e = installer.Verify(ctx, model, rt); return spec, e }) != nil {
		return report, result
	}
	if product {
		_ = step("dependencies_verified", func() (any, error) { return dependencyStatus(m), validateInstalledDependencies(ctx, m, spec, rt) })
		_ = step("dependencies_repeat_install", func() (any, error) {
			before := spec
			if err := manage(ctx, "install"); err != nil {
				return nil, err
			}
			if err := waitInstalled(ctx, m, o.InstallTimeout); err != nil {
				return nil, err
			}
			after, e := installer.Verify(ctx, model, rt)
			return after, errors.Join(e, check(equalJSON(before, after) && factory.count() == 0, "repeat install changed pinned execution spec or spawned a model"))
		})
	}
	_ = step("runtime_version", func() (any, error) {
		versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		output, err := routerprocess.RuntimeVersion(versionCtx, spec)
		return map[string]any{"command": []string{spec.Executable, "--version"}, "output": string(output)}, errors.Join(err, check(strings.Contains(string(output), "10828"), "native version output does not contain pinned build 10828"))
	})
	if o.InstallOnly {
		_ = step("close_reaps", func() (any, error) { return nil, errors.Join(closeRuntime(ctx), stopped(m)) })
		_ = step("closed_no_restart", func() (any, error) { return nil, rejectDisabled(ctx, m, factory) })
		_ = step("capabilities_remain_unknown", func() (any, error) { return m.Status().Capabilities, unknownCapabilities(m) })
		return report, result
	}
	load := func() (any, error) {
		current := sampleMemory(0)
		report.Memory = append(report.Memory, current)
		if current.Error != "" || current.AvailableRAMBytes == 0 {
			return current, fmt.Errorf("fresh available RAM observation failed: %s", current.Error)
		}
		err := manage(ctx, "load")
		if err == nil {
			report.Stages["loaded"] = true
		}
		report.Memory = append(report.Memory, sampleMemory(m.Status().PID))
		return m.Status(), err
	}
	if product {
		_ = step("dependencies_validated_before_load", func() (any, error) {
			return spec.RuntimeDependencies, routerprocess.ValidateDependencies(ctx, spec.Executable, spec.RuntimeDependencies)
		})
	}
	if step("load", load) != nil {
		return report, result
	}
	if product {
		_ = step("dependencies_loaded_modules", func() (any, error) { return loadedDependencyModules(ctx, m.Status().PID, spec) })
	}
	_ = step("health", func() (any, error) { return nil, manage(ctx, "health") })
	if o.PrivacyDiagnostic != "" {
		privacy, err := runPrivacyRepairDiagnostic(ctx, m, model, rt, o)
		report.Privacy = &privacy
		result = errors.Join(result, err)
		report.Stages["inference"] = len(privacy.Calls) > 0
	} else {
		ordinary := func(name, prompt, marker string) (any, error) {
			return generateJSON(ctx, m, filepath.Join(filepath.Dir(o.Output), name+".json"), chatRequest(model.ID, prompt, 96), marker)
		}
		if step("normal_generation", func() (any, error) {
			return ordinary("normal", "Reply with exactly LOCAL_OK and no other text.", "LOCAL_OK")
		}) == nil {
			report.Stages["inference"] = true
		}
		_ = step("sse_generation", func() (any, error) {
			request := chatRequest(model.ID, "Write the integers from 1 through 24, separated by spaces.", 96)
			request["stream"] = true
			return generateSSE(ctx, m, filepath.Join(filepath.Dir(o.Output), "stream.sse"), request, nil)
		})
		_ = step("synthetic_tool_history", func() (any, error) {
			request := chatRequest(model.ID, "", 96)
			request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_fixture", "description": "Read a synthetic fixture", "parameters": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"}}}}}
			request["tool_choice"] = "none"
			request["messages"] = []any{
				map[string]any{"role": "user", "content": "Read synthetic fixture alpha."},
				map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_synthetic_1", "type": "function", "function": map[string]any{"name": "get_fixture", "arguments": `{"key":"alpha"}`}}}},
				map[string]any{"role": "tool", "tool_call_id": "call_synthetic_1", "content": `{"value":"SYNTHETIC_42"}`},
				map[string]any{"role": "user", "content": "Return exactly the value from the tool result, without explanation."},
			}
			return generateJSON(ctx, m, filepath.Join(filepath.Dir(o.Output), "tool-history.json"), request, "SYNTHETIC_42")
		})
		_ = step("cancellation", func() (any, error) {
			call, cancel := context.WithCancel(ctx)
			defer cancel()
			request := chatRequest(model.ID, "Count integers from 1 to 2000, separated by spaces. Keep counting until 2000.", 4096)
			request["stream"] = true
			detail, err := generateSSE(call, m, filepath.Join(filepath.Dir(o.Output), "cancel.sse"), request, cancel)
			s := m.Status()
			return detail, errors.Join(check(errors.Is(err, context.Canceled), fmt.Sprintf("active cancellation returned %v", err)), check(detail.CancelIssued, "no active streamed content reached cancellation point"), check(s.Active == 0 && s.Waiting == 0, "cancellation did not release execution slot"))
		})
		_ = step("post_cancel_generation", func() (any, error) {
			return ordinary("after-cancel", "Reply with exactly AFTER_CANCEL_OK.", "AFTER_CANCEL_OK")
		})
		if o.BusinessPreacceptance {
			_ = step("business_preacceptance", func() (any, error) {
				business, err := runBusinessPreacceptance(ctx, m, model, rt)
				report.Business = &business
				return business, err
			})
		}
	}
	_ = step("capability_gate_after_diagnostics", func() (any, error) { return m.Status().Capabilities, assertUnknownGate(ctx, m, factory) })
	_ = step("disable_reaps", func() (any, error) {
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := manage(stop, "disable")
		reapErr := factory.requireReaped(stop)
		return factory.snapshot(), errors.Join(err, reapErr, stopped(m))
	})
	_ = step("disabled_no_restart", func() (any, error) { return nil, rejectDisabled(ctx, m, factory) })
	if step("reload_for_close", func() (any, error) {
		if err := manage(ctx, "enable"); err != nil {
			return nil, err
		}
		return load()
	}) == nil {
		_ = step("close_reaps", func() (any, error) {
			closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			closeErr := closeRuntime(closeCtx)
			reapErr := factory.requireReaped(closeCtx)
			return nil, errors.Join(closeErr, reapErr, stopped(m))
		})
		_ = step("closed_no_restart", func() (any, error) { return nil, rejectDisabled(ctx, m, factory) })
	}
	_ = step("capabilities_remain_unknown", func() (any, error) { return m.Status().Capabilities, unknownCapabilities(m) })
	return report, result
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func check(ok bool, reason string) error {
	if !ok {
		return errors.New(reason)
	}
	return nil
}

func unknownCapabilities(m *routerruntime.Manager) error {
	for p, c := range m.Status().Capabilities {
		if c.State != "unknown" || c.Ready {
			return fmt.Errorf("diagnostic run changed %s acceptance: %+v", p, c)
		}
	}
	return nil
}

func assertUnknownGate(ctx context.Context, m *routerruntime.Manager, factory *trackingFactory) error {
	before := factory.count()
	for _, purpose := range []routerruntime.Purpose{routerruntime.Judgment, routerruntime.Execution, routerruntime.Redaction} {
		called := false
		err := m.Invoke(ctx, purpose, []byte(`{"messages":[{"role":"user","content":"synthetic gate probe"}]}`), func(*http.Response) error { called = true; return nil })
		if !errors.Is(err, routerruntime.ErrCapability) || called || factory.count() != before {
			return fmt.Errorf("unknown %s capability did not reject without a new process: %v", purpose, err)
		}
	}
	return unknownCapabilities(m)
}

func stopped(m *routerruntime.Manager) error {
	s := m.Status()
	return errors.Join(
		check(s.PID == 0 && s.Runtime == routerruntime.Unloaded && s.Active == 0 && s.Waiting == 0, "stop did not publish PID=0/unloaded/active=0/waiting=0"),
		check(s.Error == "", fmt.Sprintf("stopped runtime retained error: %s", s.Error)),
	)
}

func rejectDisabled(ctx context.Context, m *routerruntime.Manager, factory *trackingFactory) error {
	before := factory.count()
	called := false
	err := m.Generate(ctx, routerruntime.Execution, []byte(`{"messages":[{"role":"user","content":"synthetic closed probe"}]}`), func(*http.Response) error { called = true; return nil })
	return check(errors.Is(err, routerruntime.ErrDisabled) && !called && before == factory.count() && m.Status().PID == 0, fmt.Sprintf("disabled/closed request changed lifetime: %v", err))
}

func chatRequest(model, prompt string, maxTokens int) map[string]any {
	return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": maxTokens,
		"temperature": 0, "stream": false, "chat_template_kwargs": map[string]any{"enable_thinking": false}}
}

func generateJSON(ctx context.Context, m *routerruntime.Manager, path string, request map[string]any, marker string) (any, error) {
	body, _ := json.Marshal(request)
	started := time.Now()
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	detail := map[string]any{"request": request, "responsePath": path}
	err := m.Generate(ctx, routerruntime.Execution, body, func(r *http.Response) error {
		detail["headersMs"] = milliseconds(time.Since(started))
		data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		if err != nil || len(data) > 1<<20 {
			return errors.Join(err, check(len(data) <= 1<<20, "response exceeds one MiB bound"))
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return err
		}
		if len(response.Choices) == 0 {
			return errors.New("generation returned no choices")
		}
		content := response.Choices[0].Message.Content
		detail["content"], detail["finishReason"], detail["usage"] = content, response.Choices[0].FinishReason, response.Usage
		return check(strings.TrimSpace(content) == marker, "generated content does not equal the expected synthetic marker")
	})
	detail["elapsedMs"] = milliseconds(time.Since(started))
	return detail, err
}

type streamEvidence struct {
	Request           map[string]any `json:"request"`
	ResponsePath      string         `json:"responsePath"`
	HeadersMS         float64        `json:"headersMs"`
	FirstEventMS      float64        `json:"firstEventMs"`
	FirstContentMS    float64        `json:"firstContentMs"`
	FinishMS          float64        `json:"finishMs"`
	DoneMS            float64        `json:"doneMs"`
	ElapsedMS         float64        `json:"elapsedMs"`
	Events            int            `json:"events"`
	FirstContent      int            `json:"firstContentOrdinal"`
	FinishEvent       int            `json:"finishOrdinal"`
	DoneEvent         int            `json:"doneOrdinal"`
	FinishReason      string         `json:"finishReason"`
	Content           string         `json:"content"`
	CancelIssued      bool           `json:"cancelIssued"`
	ObservedBeforeEnd bool           `json:"firstContentObservedBeforeTerminalEvent"`
}

func generateSSE(ctx context.Context, m *routerruntime.Manager, path string, request map[string]any, cancel context.CancelFunc) (detail streamEvidence, err error) {
	detail.Request, detail.ResponsePath = request, path
	body, _ := json.Marshal(request)
	started := time.Now()
	err = m.Generate(ctx, routerruntime.Execution, body, func(r *http.Response) error {
		detail.HeadersMS = milliseconds(time.Since(started))
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream") {
			return errors.New("stream did not use text/event-stream")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		scanner := bufio.NewScanner(io.TeeReader(io.LimitReader(r.Body, 1<<20), f))
		scanner.Buffer(make([]byte, 4096), 128<<10)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			detail.Events++
			if detail.Events == 1 {
				detail.FirstEventMS = milliseconds(time.Since(started))
			}
			if data == "[DONE]" {
				detail.DoneEvent, detail.DoneMS = detail.Events, milliseconds(time.Since(started))
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return err
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					if detail.FirstContent == 0 {
						detail.FirstContent, detail.FirstContentMS = detail.Events, milliseconds(time.Since(started))
						detail.ObservedBeforeEnd = detail.FinishEvent == 0 && detail.DoneEvent == 0 && choice.FinishReason == nil
					}
					detail.Content += choice.Delta.Content
					if cancel != nil && !detail.CancelIssued {
						detail.CancelIssued = true
						cancel()
					}
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					detail.FinishEvent, detail.FinishMS, detail.FinishReason = detail.Events, milliseconds(time.Since(started)), *choice.FinishReason
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if cancel != nil {
			return ctx.Err()
		}
		return check(detail.FirstContent > 0 && detail.ObservedBeforeEnd && detail.FinishEvent > detail.FirstContent && detail.DoneEvent > detail.FinishEvent, "stream lacks separately observed content, finish reason or DONE event")
	})
	detail.ElapsedMS = milliseconds(time.Since(started))
	return detail, err
}

type source struct {
	Artifact routermanifest.Artifact `json:"artifact"`
	Path     string                  `json:"path"`
}

// offlineClient preserves original URL/size/hash and resume metadata. Only exact
// pinned URLs map to this process's loopback file server; every other URL fails.
func offlineClient(sources []source) (*http.Client, func()) {
	paths, remap := map[string]source{}, map[string]string{}
	for i, source := range sources {
		key := "/artifact/" + strconv.Itoa(i)
		paths[key], remap[source.Artifact.URL] = source, key
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source, ok := paths[r.URL.Path]
		if !ok || r.Method != http.MethodGet || r.URL.RawQuery != "" {
			http.Error(w, "not a pinned local import", http.StatusForbidden)
			return
		}
		f, err := os.Open(source.Path)
		if err != nil {
			http.Error(w, "cache file unavailable", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != source.Artifact.Size {
			http.Error(w, "cache file size/type mismatch", http.StatusConflict)
			return
		}
		w.Header().Set("ETag", `"`+source.Artifact.SHA256+`"`)
		http.ServeContent(w, r, source.Artifact.FileName, info.ModTime(), f)
	}))
	inner := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != strings.TrimPrefix(server.URL, "http://") {
			return nil, errors.New("offline import refuses non-owned loopback destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	base, _ := url.Parse(server.URL)
	client := &http.Client{Transport: mappedTransport{base: base, paths: remap, inner: inner}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, func() { inner.CloseIdleConnections(); server.Close() }
}

type mappedTransport struct {
	base  *url.URL
	paths map[string]string
	inner http.RoundTripper
}

func (t mappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	path, ok := t.paths[r.URL.String()]
	if !ok || r.Method != http.MethodGet {
		return nil, errors.New("offline import refuses unpinned URL/method")
	}
	copy := r.Clone(r.Context())
	u := *t.base
	u.Path = path
	copy.URL, copy.Host = &u, u.Host
	return t.inner.RoundTrip(copy)
}

type processEvidence struct {
	PID               int                `json:"pid"`
	Endpoint          string             `json:"endpoint"`
	Spec              routerprocess.Spec `json:"verifiedSpec"`
	ContractArguments []string           `json:"contractArguments"`
	Started           time.Time          `json:"started"`
	Reaped            bool               `json:"reaped"`
	PeakWorkingSet    uint64             `json:"sampledPeakWorkingSetBytes"`
	PeakPrivate       uint64             `json:"sampledPeakPrivateBytes"`
	Samples           []memorySample     `json:"memorySamples"`
}

type trackedProcess struct {
	p       routerprocess.Process
	e       processEvidence
	sampled chan struct{}
}

type trackingFactory struct {
	mu         sync.Mutex
	owned      []*trackedProcess
	observeErr error
}

func (f *trackingFactory) Start(ctx context.Context, spec routerprocess.Spec) (routerprocess.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, previous := range f.owned {
		select {
		case <-previous.p.Done():
		default:
			return nil, errors.New("refusing a second live model process")
		}
	}
	p, err := (routerprocess.NativeFactory{}).Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	f.recordLocked(p, spec)
	return p, nil
}

func (f *trackingFactory) observe(p routerprocess.Process, spec routerprocess.Spec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, previous := range f.owned {
		select {
		case <-previous.p.Done():
		default:
			f.observeErr = errors.New("product started overlapping native model processes")
		}
	}
	f.recordLocked(p, spec)
}

func (f *trackingFactory) observationError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.observeErr
}

func (f *trackingFactory) recordLocked(p routerprocess.Process, spec routerprocess.Spec) {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(p.Endpoint(), "http://"))
	args := []string{"--host", "127.0.0.1", "--port", port, "--model", spec.ModelPath, "--ctx-size", strconv.Itoa(spec.ContextSize), "--parallel", "1", "--cache-reuse", "256", "--jinja"}
	if spec.Backend == "cpu" || spec.Backend == "" {
		args = append(args, "--n-gpu-layers", "0")
	}
	tracked := &trackedProcess{p: p, sampled: make(chan struct{}), e: processEvidence{PID: p.PID(), Endpoint: p.Endpoint(), Spec: spec, ContractArguments: append(args, spec.ExtraArgs...), Started: time.Now().UTC()}}
	f.owned = append(f.owned, tracked)
	go f.sample(tracked)
}

func (f *trackingFactory) sample(p *trackedProcess) {
	defer close(p.sampled)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		sample := sampleMemory(p.p.PID())
		f.mu.Lock()
		if sample.WorkingSetBytes > p.e.PeakWorkingSet {
			p.e.PeakWorkingSet = sample.WorkingSetBytes
		}
		if sample.PrivateBytes > p.e.PeakPrivate {
			p.e.PeakPrivate = sample.PrivateBytes
		}
		p.e.Samples = append(p.e.Samples, sample)
		f.mu.Unlock()
		select {
		case <-p.p.Done():
			f.mu.Lock()
			p.e.Reaped = true
			f.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}

func (f *trackingFactory) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.owned) }
func (f *trackingFactory) snapshot() []processEvidence {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]processEvidence, len(f.owned))
	for i, p := range f.owned {
		result[i] = p.e
		result[i].Samples = append([]memorySample(nil), p.e.Samples...)
	}
	return result
}
func (f *trackingFactory) requireReaped(ctx context.Context) error {
	f.mu.Lock()
	owned := append([]*trackedProcess(nil), f.owned...)
	f.mu.Unlock()
	for _, p := range owned {
		select {
		case <-p.p.Done():
		case <-ctx.Done():
			return fmt.Errorf("PID %d was not reaped: %w", p.p.PID(), ctx.Err())
		}
		select {
		case <-p.sampled:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (f *trackingFactory) stopAll(ctx context.Context) error {
	f.mu.Lock()
	owned := append([]*trackedProcess(nil), f.owned...)
	f.mu.Unlock()
	var err error
	for _, p := range owned {
		err = errors.Join(err, p.p.Stop(ctx))
	}
	return errors.Join(err, f.requireReaped(ctx))
}

type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := (64 << 10) - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return n, nil
}
func (b *boundedBuffer) String() string { return string(b.data) }

type memorySample struct {
	At                  time.Time `json:"at"`
	PID                 int       `json:"pid,omitempty"`
	AvailableRAMBytes   uint64    `json:"availableRamBytes"`
	TotalRAMBytes       uint64    `json:"totalRamBytes"`
	WorkingSetBytes     uint64    `json:"workingSetBytes"`
	PeakWorkingSetBytes uint64    `json:"peakWorkingSetBytes"`
	PrivateBytes        uint64    `json:"privateBytes"`
	Error               string    `json:"error,omitempty"`
}
