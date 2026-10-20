package routerruntime

import (
	"context"
	"errors"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

type runtimeSettingsKey struct{}

// WithRuntimeSettings binds one request to its entry-time settings. A reload
// invalidates admission for older snapshots rather than changing their budgets.
func WithRuntimeSettings(ctx context.Context, s routerconfig.RuntimeSettings) context.Context {
	return context.WithValue(ctx, runtimeSettingsKey{}, s)
}

func RuntimeSettingsFrom(ctx context.Context) (routerconfig.RuntimeSettings, bool) {
	s, ok := ctx.Value(runtimeSettingsKey{}).(routerconfig.RuntimeSettings)
	return s, ok
}

func (m *Manager) Settings() routerconfig.RuntimeSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.intent.Settings
}

// SnapshotBinding captures settings and model identity under the same lock.
// A stale request snapshot cannot claim the identity of its replacement runtime.
func (m *Manager) SnapshotBinding(ctx context.Context) (LocalCallIdentity, routerconfig.RuntimeSettings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := RuntimeSettingsFrom(ctx)
	if !ok {
		s = m.intent.Settings
	}
	if s != m.intent.Settings {
		return LocalCallIdentity{}, s
	}
	model, runtime, err := m.resources()
	if err != nil {
		return LocalCallIdentity{}, s
	}
	return LocalCallIdentity{ModelID: model.ID, ModelFingerprint: model.Fingerprint(runtime), RuntimeID: runtime.ID, RuntimeFingerprint: RuntimeFingerprint(runtime)}, s
}

func (m *Manager) EffectiveSettings() routerconfig.RuntimeSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effectiveSettingsLocked()
}

// Load starts and health-checks the selected runtime without granting any
// capability evidence. It participates in the existing scheduler lifecycle.
func (m *Manager) Load(ctx context.Context) error {
	return m.Use(ctx, Execution, func(context.Context, string) error { return nil })
}

func (m *Manager) effectiveSettingsLocked() routerconfig.RuntimeSettings {
	s := m.intent.Settings.Effective()
	if m.intent.Settings.IsZero() {
		if model, ok := m.o.Catalog.Model(m.intent.Selected); ok {
			s.ContextTokens = model.ContextTokens
		}
		s.JudgmentQueueSeconds = int(m.o.JudgmentBudget / time.Second)
		s.JudgmentInferenceSeconds = s.JudgmentQueueSeconds
		s.ExecutionQueueSeconds = int(m.o.ExecutionQueueBudget / time.Second)
		s.ExecutionSeconds = int(m.o.ExecutionBudget / time.Second)
		s.LoadSeconds = int(m.o.LoadTimeout / time.Second)
	}
	return s
}

func (m *Manager) loadTimeoutLocked() time.Duration {
	if m.intent.Settings.IsZero() {
		return m.o.LoadTimeout
	}
	return time.Duration(m.intent.Settings.LoadSeconds) * time.Second
}

// UpdateSettings stops admission, drains current generation, reaps the owned
// process, then persists the new settings. Loading remains an explicit action.
// A failure preserves the old settings and never leaves a second child running.
func (m *Manager) UpdateSettings(ctx context.Context, s routerconfig.RuntimeSettings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	if m.closed || m.stopping || m.installDone != nil {
		m.mu.Unlock()
		return ErrBusy
	}
	if s == m.intent.Settings {
		m.mu.Unlock()
		return nil
	}
	if model, ok := m.o.Catalog.Model(m.intent.Selected); ok && !s.IsZero() && model.NativeContextTokens > 0 && s.ContextTokens > model.NativeContextTokens {
		m.mu.Unlock()
		return errors.New("context exceeds model native limit")
	}
	i := m.intent
	i.Settings = s
	m.updating, m.stopping = true, true
	m.notifyLocked()
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.updating, m.stopping = false, false; m.notifyLocked(); m.mu.Unlock() }()
	if err := m.stop(ctx, false); err != nil {
		return err
	}
	if err := m.o.Store.Save(ctx, i); err != nil {
		return err
	}
	m.mu.Lock()
	m.intent = i
	m.epoch++
	m.lastError = ""
	m.resetCapabilitiesLocked()
	m.notifyLocked()
	m.mu.Unlock()
	return nil
}

// Budget errors also match DeadlineExceeded for existing context-aware callers.
func purposeTimeout(parent context.Context, purpose Purpose, queue bool) error {
	if err := parent.Err(); err != nil {
		return err
	}
	var reason error
	switch purpose {
	case Judgment:
		if queue {
			reason = ErrJudgmentQueueTimeout
		} else {
			reason = ErrJudgmentInferenceTimeout
		}
	case Execution:
		if queue {
			reason = ErrExecutionQueueTimeout
		} else {
			reason = ErrExecutionTimeout
		}
	default:
		return context.DeadlineExceeded
	}
	return errors.Join(reason, context.DeadlineExceeded)
}
