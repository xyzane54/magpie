package routerruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/routerdevice"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
)

const QueueLimit = 16

type waiter struct{ occupied bool }

type Manager struct {
	memoryBeforeLoad routerdevice.Fact[uint64]
	memoryAfterLoad  routerdevice.Fact[uint64]
	mu               sync.Mutex
	op               sync.Mutex // serializes explicit intent changes, never inference or callbacks
	o                Options
	intent           Intent
	installation     string
	state            RunState
	healthy          bool
	lastError        string
	progress         Progress
	dependencies     DependencyStatus
	capabilities     map[Purpose]Capability
	changed          chan struct{}
	proc             routerprocess.Process
	stopRequested    routerprocess.Process // identity of the child whose Stop was requested
	loading          chan struct{}
	loadCancel       context.CancelFunc
	installDone      chan struct{}
	installCancel    context.CancelFunc
	waiters          []*waiter
	active           bool
	activeCancel     context.CancelFunc
	lastUsed         time.Time
	closed           bool
	stopping         bool
	updating         bool
	epoch            uint64
	ctx              context.Context
	cancel           context.CancelFunc
	workers          sync.WaitGroup
	observer         LocalCallObserver
}

func New(ctx context.Context, o Options) (*Manager, error) {
	if o.Store == nil || o.Installer == nil || o.Factory == nil {
		return nil, errors.New("runtime dependencies required")
	}
	if o.LoadTimeout <= 0 {
		o.LoadTimeout = 60 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 15 * time.Minute
	}
	if o.JudgmentBudget <= 0 {
		o.JudgmentBudget = 5 * time.Second
	}
	if o.ExecutionQueueBudget <= 0 {
		o.ExecutionQueueBudget = 8 * time.Second
	}
	if o.ExecutionBudget <= 0 {
		o.ExecutionBudget = 120 * time.Second
	}
	if o.RedactionBudget <= 0 {
		o.RedactionBudget = 15 * time.Second
		if o.ExperimentalRedaction {
			o.RedactionBudget = experimentalRedactionBudget
		}
	}
	if o.ExperimentalRedaction && o.RedactionBudget > experimentalRedactionBudget {
		o.RedactionBudget = experimentalRedactionBudget
	}
	if o.AvailableMemory == nil {
		o.AvailableMemory = routerdevice.AvailableMemory
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	i, err := o.Store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if i.Selected != "" {
		if _, ok := o.Catalog.Model(i.Selected); !ok {
			return nil, errors.New("unknown persisted local model")
		}
	}
	if i.Enabled && i.Selected == "" {
		return nil, errors.New("enabled local model has no selection")
	}
	if err := i.Settings.Validate(); err != nil {
		return nil, err
	}
	if model, ok := o.Catalog.Model(i.Selected); ok && !i.Settings.IsZero() && model.NativeContextTokens > 0 && i.Settings.ContextTokens > model.NativeContextTokens {
		return nil, errors.New("context exceeds model native limit")
	}
	life, cancel := context.WithCancel(context.Background())
	m := &Manager{o: o, intent: i, installation: "not_installed", state: Unloaded, changed: make(chan struct{}), ctx: life, cancel: cancel, observer: o.LocalObserver}
	m.resetCapabilitiesLocked()
	m.dependencies = DependencyStatus{State: "not_required"}
	if rt, ok := o.Catalog.Runtime(o.RuntimeID); ok {
		m.updateDependenciesLocked(rt, ErrNotInstalled)
	}
	// Reading installed files is allowed at startup; loading/downloading is not.
	if i.Selected != "" {
		model, rt, err := m.resources()
		if err == nil {
			_, err = o.Installer.Verify(ctx, model, rt)
		}
		if err == nil {
			m.installation = "installed"
		}
		m.updateDependenciesLocked(rt, err)
	}
	return m, nil
}

// SetLocalObserver installs a trusted process-local completion hook. It is
// intentionally unavailable through management HTTP and does not alter model
// admission or output.
func (m *Manager) SetLocalObserver(observer LocalCallObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.observer = observer
	m.mu.Unlock()
}

func (m *Manager) resources() (routermanifest.Model, routermanifest.Runtime, error) {
	model, ok := m.o.Catalog.Model(m.intent.Selected)
	if !ok {
		return model, routermanifest.Runtime{}, ErrNotInstalled
	}
	if !m.intent.Settings.IsZero() {
		model.ContextTokens = m.intent.Settings.ContextTokens
	}
	rt, ok := m.o.Catalog.Runtime(m.o.RuntimeID)
	if !ok {
		return model, rt, errors.New("runtime artifact unavailable on this platform")
	}
	return model, rt, nil
}
func (m *Manager) fingerprintLocked() string {
	model, rt, err := m.resources()
	if err != nil {
		return ""
	}
	return model.Fingerprint(rt)
}
func (m *Manager) resetCapabilitiesLocked() {
	fp := m.fingerprintLocked()
	m.capabilities = make(map[Purpose]Capability)
	for _, p := range []Purpose{Judgment, Execution, Redaction} {
		m.capabilities[p] = Capability{State: "unknown", Fingerprint: fp, Reason: "acceptance_suite_not_provided"}
	}
}
func (m *Manager) notifyLocked() { close(m.changed); m.changed = make(chan struct{}) }
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	fp := m.fingerprintLocked()
	s := Status{MemoryBeforeLoad: m.memoryBeforeLoad, MemoryAfterLoad: m.memoryAfterLoad, Settings: m.intent.Settings, EffectiveSettings: m.effectiveSettingsLocked(), Intent: m.intent, Installation: m.installation, Runtime: m.state, Healthy: m.healthy, Waiting: len(m.waiters), QueueLimit: QueueLimit, ExecutionSlots: 1, Progress: m.progress, Error: m.lastError, LastUsed: m.lastUsed, Capabilities: make(map[Purpose]Capability), Dependencies: m.dependencies}
	if m.active {
		s.Active = 1
	}
	if m.proc != nil {
		s.PID = m.proc.PID()
	}
	for p, c := range m.capabilities {
		c.Ready = m.intent.Enabled && !m.stopping && !m.closed && m.installation == "installed" && m.state != Fault && c.State == "available" && c.Fingerprint == fp
		c.TrialEnabled = trialEnabled(m.intent.Trial, p)
		c.TrialReady = c.TrialEnabled && c.TrialEvidence && m.intent.Enabled && !m.stopping && !m.closed && m.installation == "installed" && m.state != Fault && c.Fingerprint == fp
		s.Capabilities[p] = c
	}
	return s
}

// Identity returns the current server-owned model/runtime binding without
// loading or starting a process. An empty identity means no valid selection is
// available; callers must leave those fields unknown in observations.
func (m *Manager) Identity() LocalCallIdentity {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, runtime, err := m.resources()
	if err != nil {
		return LocalCallIdentity{}
	}
	return LocalCallIdentity{ModelID: model.ID, ModelFingerprint: model.Fingerprint(runtime), RuntimeID: runtime.ID, RuntimeFingerprint: RuntimeFingerprint(runtime)}
}

// Select changes one fixed catalog tier only while no model is enabled.
func (m *Manager) Select(ctx context.Context, id string) error {
	m.op.Lock()
	defer m.op.Unlock()
	if _, ok := m.o.Catalog.Model(id); !ok {
		return errors.New("unknown local model")
	}
	m.mu.Lock()
	if m.closed || m.stopping || m.intent.Enabled || m.installDone != nil {
		m.mu.Unlock()
		return ErrBusy
	}
	i := Intent{Selected: id, Settings: m.intent.Settings}
	m.mu.Unlock()
	if err := m.o.Store.Save(ctx, i); err != nil {
		return err
	}
	m.mu.Lock()
	m.intent = i
	m.installation = "not_installed"
	m.lastError = ""
	m.progress = Progress{}
	m.resetCapabilitiesLocked()
	m.notifyLocked()
	m.mu.Unlock()
	return m.RefreshInstallation(ctx)
}

// EnableTrial persists explicit experimental routing/execution authorization
// only after the corresponding server-owned trial check has passed for the
// current model/runtime/material binding.
func (m *Manager) EnableTrial(ctx context.Context, purposes ...Purpose) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	if m.closed || m.stopping || !m.intent.Enabled || m.installation != "installed" {
		m.mu.Unlock()
		return ErrDisabled
	}
	if len(purposes) == 0 {
		purposes = []Purpose{Judgment, Execution}
	}
	fp := m.fingerprintLocked()
	next := m.intent.Trial
	for _, p := range purposes {
		if p != Judgment && p != Execution {
			m.mu.Unlock()
			return ErrCapability
		}
		c, ok := m.capabilities[p]
		if !ok || !c.Trial || !c.TrialEvidence || c.Fingerprint == "" || c.Fingerprint != fp {
			m.mu.Unlock()
			return ErrCapability
		}
		setTrialEnabled(&next, p, true)
	}
	i := m.intent
	i.Trial = next
	m.mu.Unlock()
	if err := m.o.Store.Save(ctx, i); err != nil {
		return err
	}
	m.mu.Lock()
	m.intent = i
	m.notifyLocked()
	m.mu.Unlock()
	return nil
}

// DisableTrial persists explicit experimental routing/execution shutdown. It
// never changes the independent redaction trial or runtime enable intent.
func (m *Manager) DisableTrial(ctx context.Context, purposes ...Purpose) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	next := m.intent.Trial
	if len(purposes) == 0 {
		purposes = []Purpose{Judgment, Execution}
	}
	for _, p := range purposes {
		if p != Judgment && p != Execution {
			m.mu.Unlock()
			return ErrCapability
		}
		setTrialEnabled(&next, p, false)
	}
	i := m.intent
	i.Trial = next
	m.mu.Unlock()
	if err := m.o.Store.Save(ctx, i); err != nil {
		return err
	}
	m.mu.Lock()
	m.intent = i
	m.notifyLocked()
	m.mu.Unlock()
	return nil
}

func trialEnabled(in TrialIntent, p Purpose) bool {
	switch p {
	case Judgment:
		return in.Judgment
	case Execution:
		return in.Execution
	default:
		return false
	}
}

func setTrialEnabled(in *TrialIntent, p Purpose, enabled bool) {
	if in == nil {
		return
	}
	switch p {
	case Judgment:
		in.Judgment = enabled
	case Execution:
		in.Execution = enabled
	}
}
func (m *Manager) Enable(ctx context.Context) error {
	return m.EnableSelected(ctx, "")
}

// EnableSelected binds a potentially slow device check to the same selection.
// An intervening selection edit fails instead of enabling unchecked resources.
func (m *Manager) EnableSelected(ctx context.Context, expected string) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	if m.closed || m.stopping {
		m.mu.Unlock()
		return ErrDisabled
	}
	i := m.intent
	if expected != "" && i.Selected != expected {
		m.mu.Unlock()
		return ErrBusy
	}
	if i.Selected == "" {
		m.mu.Unlock()
		return ErrNotInstalled
	}
	i.Enabled = true
	m.mu.Unlock()
	if err := m.o.Store.Save(ctx, i); err != nil {
		return err
	}
	m.mu.Lock()
	m.intent = i
	m.notifyLocked()
	m.mu.Unlock()
	return nil
}

func (m *Manager) RefreshInstallation(ctx context.Context) error {
	m.mu.Lock()
	if m.installDone != nil || m.loading != nil || m.proc != nil {
		m.mu.Unlock()
		return ErrBusy
	}
	model, rt, err := m.resources()
	id := m.intent.Selected
	m.mu.Unlock()
	if err == nil {
		_, err = m.o.Installer.Verify(ctx, model, rt)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.intent.Selected == id && m.installDone == nil && m.loading == nil && m.proc == nil {
		m.updateDependenciesLocked(rt, err)
		if err == nil {
			m.installation = "installed"
		} else {
			m.installation = "not_installed"
		}
		m.notifyLocked()
	}
	// A missing installation is a state, not a failed selection operation.
	return ctx.Err()
}

// Install returns after starting an owned job. Request disconnect does not
// abandon that job; CancelInstall, Disable and Close synchronously join it.
func (m *Manager) Install() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.intent.Enabled || m.closed || m.stopping {
		return ErrDisabled
	}
	if m.installDone != nil {
		return nil
	}
	if m.proc != nil || m.loading != nil {
		return ErrBusy
	}
	model, rt, err := m.resources()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	m.installDone, m.installCancel = done, cancel
	m.installation, m.lastError, m.progress = "downloading", "", Progress{}
	if rt.Dependencies != nil {
		m.dependencies = DependencyStatus{State: "preparing", ID: rt.Dependencies.ID, Version: rt.Dependencies.Version}
	}
	m.notifyLocked()
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer cancel()
		_, err := m.o.Installer.Install(ctx, model, rt, func(p Progress) {
			m.mu.Lock()
			m.progress = p
			if rt.Dependencies != nil && m.dependencies.State == "preparing" && p.Stage != "" && p.Stage != "dependency" {
				m.dependencies = DependencyStatus{State: "ready", ID: rt.Dependencies.ID, Version: rt.Dependencies.Version}
			}
			m.notifyLocked()
			m.mu.Unlock()
		})
		if err == nil {
			_, err = m.o.Installer.Verify(ctx, model, rt)
		}
		m.mu.Lock()
		if rt.Dependencies != nil && m.dependencies.State == "ready" && err != nil && dependencyCode(err) == "" {
			// Keep verified runtime dependency status when a later download stage fails or is canceled.
		} else {
			m.updateDependenciesLocked(rt, err)
		}
		if err == nil {
			m.installation, m.progress = "installed", Progress{}
		} else if errors.Is(err, context.Canceled) {
			m.installation = "canceled"
		} else {
			m.progress.Reason = classifyInstallError(err)
			m.installation, m.lastError = "failed", operationCode(err, installErrorCode(m.progress.Stage, m.progress.Reason))
		}
		m.installDone, m.installCancel = nil, nil
		close(done)
		m.notifyLocked()
		m.mu.Unlock()
	}()
	return nil
}
func (m *Manager) CancelInstall(ctx context.Context) error {
	m.mu.Lock()
	done, cancel := m.installDone, m.installCancel
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ensure merges cold starts independently of any individual waiter's deadline.
func (m *Manager) ensure(ctx context.Context, epoch uint64) error {
	for {
		m.mu.Lock()
		if !m.intent.Enabled || m.closed || m.stopping || epoch != m.epoch {
			m.mu.Unlock()
			return ErrDisabled
		}
		if m.proc != nil {
			healthy := m.healthy
			m.mu.Unlock()
			if healthy {
				return nil
			}
			return ErrFault
		}
		if m.installation != "installed" {
			dependencyError := m.dependencies.Error
			m.mu.Unlock()
			if dependencyError != "" {
				return &routerprocess.DependencyError{Code: dependencyError, Err: ErrNotInstalled}
			}
			return ErrNotInstalled
		}
		done := m.loading
		if done == nil {
			model, rt, err := m.resources()
			if err != nil {
				m.mu.Unlock()
				return err
			}
			loadCtx, cancel := context.WithCancel(m.ctx)
			done = make(chan struct{})
			m.loading, m.loadCancel = done, cancel
			m.state, m.lastError = Loading, ""
			m.notifyLocked()
			m.workers.Add(1)
			go m.load(loadCtx, cancel, done, model, rt, m.loadTimeoutLocked())
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
		m.mu.Lock()
		failed := m.state == Fault
		m.mu.Unlock()
		if failed {
			m.mu.Lock()
			dependencyError := m.dependencies.Error
			m.mu.Unlock()
			if dependencyError != "" {
				return &routerprocess.DependencyError{Code: dependencyError, Err: ErrFault}
			}
			return ErrFault
		}
	}
}
func (m *Manager) load(ctx context.Context, cancel context.CancelFunc, done chan struct{}, model routermanifest.Model, rt routermanifest.Runtime, loadTimeout time.Duration) {
	defer m.workers.Done()
	defer cancel()
	// A cancelable timer also supports deterministic loading-timeout tests.
	finished := make(chan struct{})
	go func() {
		select {
		case <-m.o.After(loadTimeout):
			cancel()
		case <-finished:
		case <-ctx.Done():
		}
	}()
	before, beforeErr := m.o.AvailableMemory(ctx)
	m.mu.Lock()
	m.memoryBeforeLoad = routerdevice.Unknown[uint64]("available_memory_probe_failed")
	m.memoryAfterLoad = routerdevice.Unknown[uint64]("load_not_completed")
	if beforeErr == nil {
		m.memoryBeforeLoad = routerdevice.Known(before)
	}
	m.mu.Unlock()
	spec, err := m.o.Installer.Verify(ctx, model, rt)
	if err == nil {
		spec.ContextSize = model.ContextTokens
	}
	verificationFailed := err != nil && ctx.Err() == nil
	var p routerprocess.Process
	if err == nil {
		for attempt := 0; attempt < 2; attempt++ {
			p, err = m.o.Factory.Start(ctx, spec)
			if err != nil {
				break
			}
			exitedBeforeReady := false
			for {
				if err = ctx.Err(); err != nil {
					break
				}
				probe, stop := context.WithTimeout(ctx, time.Second)
				err = p.Health(probe)
				stop()
				if err == nil {
					break
				}
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-p.Done():
					err = errors.Join(ErrFault, p.Err())
					exitedBeforeReady = true
				case <-m.o.After(50 * time.Millisecond):
					continue
				}
				break
			}
			if !exitedBeforeReady || attempt > 0 || dependencyCode(err) != "" || ctx.Err() != nil {
				break
			}
			stop, finish := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.Stop(stop)
			finish()
			p = nil
		}
	}
	after, afterErr := m.o.AvailableMemory(ctx)
	close(finished)
	m.mu.Lock()
	m.memoryAfterLoad = routerdevice.Unknown[uint64]("available_memory_probe_failed")
	if afterErr == nil {
		m.memoryAfterLoad = routerdevice.Known(after)
	}
	if verificationFailed {
		m.installation = "not_installed"
	}
	if dependencyCode(err) != "" {
		m.installation = "not_installed"
	}
	m.updateDependenciesLocked(rt, err)
	adopt := err == nil && ctx.Err() == nil && m.intent.Enabled && !m.closed && !m.stopping
	if adopt {
		m.proc, m.healthy, m.state = p, true, Ready
		m.lastUsed = m.o.Now()
		m.workers.Add(1)
		go m.watch(p)
	}
	m.mu.Unlock()
	unreaped := false
	if !adopt && p != nil {
		stop, finish := context.WithTimeout(context.Background(), 5*time.Second)
		_ = p.Stop(stop)
		finish()
		select {
		case <-p.Done():
		default:
			unreaped = true
		}
	}
	m.mu.Lock()
	if unreaped {
		m.proc = p
		m.workers.Add(1)
		go m.watch(p)
	}
	if !adopt {
		m.healthy = false
		if m.intent.Enabled && !m.stopping && !m.closed {
			m.state, m.lastError = Fault, operationCode(err, "load_failed_or_timed_out")
		} else {
			m.state = Unloaded
		}
		if unreaped {
			m.state, m.lastError = Fault, "load_cleanup_pending"
		}
	}
	m.loading, m.loadCancel = nil, nil
	close(done)
	m.notifyLocked()
	m.mu.Unlock()
}
func (m *Manager) watch(p routerprocess.Process) {
	defer m.workers.Done()
	<-p.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc == p {
		m.proc, m.healthy = nil, false
		if m.stopRequested != p {
			m.state = Fault
			if m.lastError == "" {
				m.lastError = "process_exited"
			}
		}
		if m.activeCancel != nil {
			m.activeCancel()
		}
		m.notifyLocked()
	}
	if m.stopRequested == p {
		m.stopRequested = nil
	}
}

// Draining work is not a requested process exit. Bind the expected exit to the
// owned child immediately before Stop, without hiding an already observed exit.
func (m *Manager) stopProcess(ctx context.Context, p routerprocess.Process) error {
	m.mu.Lock()
	if m.proc == p {
		select {
		case <-p.Done():
			// Stop may clear ownership before the watcher gets its lock. Record
			// this prior exit now, retaining any more specific runtime failure.
			if m.stopRequested != p {
				m.healthy = false
				m.state = Fault
				if m.lastError == "" {
					m.lastError = "process_exited"
				}
				if m.activeCancel != nil {
					m.activeCancel()
				}
				m.notifyLocked()
			}
		default:
			m.stopRequested = p
		}
	}
	m.mu.Unlock()
	return p.Stop(ctx)
}

func validPurpose(p Purpose) bool { return p == Judgment || p == Execution || p == Redaction }

// Use holds the single execution slot until fn returns, including streamed body
// consumption. The callback must honor ctx. Cold loading precedes stage budgets.
func (m *Manager) Use(ctx context.Context, p Purpose, fn func(context.Context, string) error) error {
	if !validPurpose(p) || fn == nil {
		return errors.New("invalid runtime invocation")
	}
	w := &waiter{}
	m.mu.Lock()
	if !m.intent.Enabled || m.closed || m.stopping {
		m.mu.Unlock()
		return ErrDisabled
	}
	if len(m.waiters) >= QueueLimit {
		m.mu.Unlock()
		return ErrQueueFull
	}
	settings, snapshot := RuntimeSettingsFrom(ctx)
	if !snapshot {
		settings = m.intent.Settings
	}
	if settings != m.intent.Settings {
		m.mu.Unlock()
		return ErrSettingsChanged
	}
	epoch := m.epoch
	m.waiters = append(m.waiters, w)
	m.notifyLocked()
	m.mu.Unlock()
	defer m.removeWaiter(w)
	if err := m.ensure(ctx, epoch); err != nil {
		return err
	}
	budget := m.o.JudgmentBudget
	if p == Execution {
		budget = m.o.ExecutionQueueBudget
	}
	if p == Redaction {
		budget = m.o.RedactionBudget
	}
	if !settings.IsZero() {
		if p == Judgment {
			budget = time.Duration(settings.JudgmentQueueSeconds) * time.Second
		}
		if p == Execution {
			budget = time.Duration(settings.ExecutionQueueSeconds) * time.Second
		}
	}
	stage, stageCancel := context.WithTimeout(ctx, budget)
	defer stageCancel()
	for {
		m.mu.Lock()
		if !m.intent.Enabled || m.closed || m.stopping || epoch != m.epoch {
			m.mu.Unlock()
			return ErrDisabled
		}
		if m.proc == nil || !m.healthy {
			m.mu.Unlock()
			return ErrFault
		}
		if err := stage.Err(); err != nil {
			m.mu.Unlock()
			if errors.Is(err, context.DeadlineExceeded) {
				return purposeTimeout(ctx, p, true)
			}
			return err
		}
		if !m.active && len(m.waiters) > 0 && m.waiters[0] == w {
			m.waiters = m.waiters[1:]
			pctx := stage
			var finish context.CancelFunc
			if p == Execution {
				executionBudget := m.o.ExecutionBudget
				if !settings.IsZero() {
					executionBudget = time.Duration(settings.ExecutionSeconds) * time.Second
				}
				pctx, finish = context.WithTimeout(ctx, executionBudget)
			} else if p == Judgment && !settings.IsZero() {
				pctx, finish = context.WithTimeout(ctx, time.Duration(settings.JudgmentInferenceSeconds)*time.Second)
			} else {
				pctx, finish = context.WithTimeout(ctx, budget)
			}
			m.active, m.activeCancel, m.state = true, finish, Busy
			process := m.proc
			m.notifyLocked()
			m.mu.Unlock()
			err := fn(pctx, process.Endpoint())
			stageErr := pctx.Err()
			if err == nil {
				err = stageErr
			}
			finish()
			m.mu.Lock()
			// A crashed child cancels its call context, but that is not a
			// client cancellation. W4 must be able to distinguish fallback
			// eligible faults from cancellation, shutdown and stage expiry.
			if err != nil {
				switch {
				case ctx.Err() != nil:
					err = ctx.Err()
				case epoch != m.epoch || m.closed || !m.intent.Enabled:
					err = ErrDisabled
				case errors.Is(stageErr, context.DeadlineExceeded):
					err = purposeTimeout(ctx, p, false)
				default:
					select {
					case <-process.Done():
						err = ErrFault
					default:
					}
				}
			}
			m.active, m.activeCancel = false, nil
			m.lastUsed = m.o.Now()
			if m.proc == process {
				if m.stopping {
					m.state = Unloading
				} else {
					m.state = Idle
				}
			}
			m.notifyLocked()
			m.mu.Unlock()
			return err
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-stage.Done():
			if errors.Is(stage.Err(), context.DeadlineExceeded) {
				return purposeTimeout(ctx, p, true)
			}
			return stage.Err()
		case <-changed:
		}
	}
}
func (m *Manager) removeWaiter(w *waiter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.waiters {
		if c == w {
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			m.notifyLocked()
			break
		}
	}
}

// CheckCapability records only versioned sample evidence and never infers
// judgment/redaction from a healthy process or a normal execution answer.
func (m *Manager) CheckCapability(ctx context.Context, c Check) error {
	if !validPurpose(c.Purpose) {
		return errors.New("invalid capability")
	}
	m.mu.Lock()
	fingerprint := m.fingerprintLocked()
	epoch := m.epoch
	m.mu.Unlock()
	if c.Run == nil || c.Suite == "" {
		m.mu.Lock()
		m.capabilities[c.Purpose] = Capability{State: "unknown", Fingerprint: fingerprint, Reason: "acceptance_suite_not_provided"}
		m.mu.Unlock()
		return ErrCapability
	}
	var evidence string
	err := m.Use(ctx, c.Purpose, func(ctx context.Context, endpoint string) error {
		var err error
		evidence, err = c.Run(ctx, endpoint)
		return err
	})
	state, reason := "available", ""
	if err != nil || evidence == "" {
		state, reason = "unavailable", "sample_validation_failed"
		if err == nil {
			err = ErrCapability
		}
	}
	trialEvidence := c.Trial && err == nil && evidence != ""
	if c.Trial {
		state, reason = "unknown", "experimental_trial_quality_not_formally_accepted"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if fingerprint != m.fingerprintLocked() || epoch != m.epoch {
		return ErrCapability
	}
	m.capabilities[c.Purpose] = Capability{State: state, Fingerprint: fingerprint, Suite: c.Suite, Evidence: evidence, CheckedAt: m.o.Now(), Reason: reason, Trial: c.Trial, TrialEvidence: trialEvidence}
	m.notifyLocked()
	return err
}
func (m *Manager) Require(p Purpose) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.intent.Enabled || m.closed || m.stopping {
		return ErrDisabled
	}
	c, ok := m.capabilities[p]
	if !ok || c.Fingerprint != m.fingerprintLocked() {
		return ErrCapability
	}
	if c.State == "available" {
		return nil
	}
	if (p == Judgment || p == Execution) && c.Trial && c.TrialEvidence && trialEnabled(m.intent.Trial, p) {
		return nil
	}
	if c.State != "available" {
		return ErrCapability
	}
	return nil
}

// Sweep is the controlled-time idle check. A service ticker invokes it; tests
// advance Now directly. No model is loaded by the ticker.
func (m *Manager) Sweep(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	if !m.intent.Enabled || m.closed || m.stopping || m.active || len(m.waiters) > 0 || m.loading != nil || m.proc == nil || m.o.Now().Sub(m.lastUsed) < m.o.IdleTimeout {
		m.mu.Unlock()
		return nil
	}
	p := m.proc
	m.healthy, m.state = false, Unloading
	m.stopping = true
	m.notifyLocked()
	m.mu.Unlock()
	err := m.stopProcess(ctx, p)
	m.mu.Lock()
	m.stopping = false
	if err == nil {
		if m.proc == p {
			m.proc = nil
		}
		m.state = Unloaded
	} else {
		m.state, m.lastError = Fault, "unload_failed"
	}
	m.notifyLocked()
	m.mu.Unlock()
	return err
}

func (m *Manager) Disable(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	i := m.intent
	i.Enabled = false
	m.intent = i
	m.stopping = true
	m.state = Unloading
	m.epoch++
	m.notifyLocked()
	m.mu.Unlock()
	// Persist before acknowledging disable; failure still stops this session.
	saveErr := m.o.Store.Save(ctx, i)
	err := m.stop(ctx, false)
	return errors.Join(saveErr, err)
}
func (m *Manager) stop(ctx context.Context, closing bool) error {
	m.mu.Lock()
	m.stopping = true
	if closing {
		m.closed = true
		m.epoch++
	}
	if m.loadCancel != nil {
		m.loadCancel()
	}
	if m.installCancel != nil {
		m.installCancel()
	}
	m.notifyLocked()
	for m.active || m.loading != nil || m.installDone != nil {
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			m.mu.Lock()
			if m.activeCancel != nil {
				m.activeCancel()
			}
			p := m.proc
			m.mu.Unlock()
			if p != nil {
				kill, finish := context.WithTimeout(context.Background(), 5*time.Second)
				_ = m.stopProcess(kill, p)
				finish()
			}
			m.mu.Lock()
			m.stopping = m.updating
			m.healthy = false
			m.state = Fault
			m.lastError = "drain_timed_out"
			m.notifyLocked()
			m.mu.Unlock()
			return ctx.Err()
		}
		m.mu.Lock()
	}
	p := m.proc
	m.healthy, m.state = false, Unloading
	m.notifyLocked()
	m.mu.Unlock()
	var err error
	if p != nil {
		err = m.stopProcess(ctx, p)
	}
	m.mu.Lock()
	m.stopping = m.updating
	m.state = Unloaded
	if err != nil {
		m.state, m.lastError = Fault, "unload_failed"
	} else if m.proc == p {
		m.proc = nil
	}
	m.notifyLocked()
	m.mu.Unlock()
	return err
}

// Close retains persisted enable intent but prevents all new work in this
// Router lifetime. It cancels every owned worker and joins process monitors.
func (m *Manager) Close(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	err := m.stop(ctx, true)
	m.cancel()
	done := make(chan struct{})
	go func() { m.workers.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("runtime close: %w", ctx.Err())
	}
}
