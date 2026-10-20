package routerruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerprocess"
)

type settingsRecordingFactory struct {
	base  *fixtureFactory
	specs chan routerprocess.Spec
}

func (f settingsRecordingFactory) Start(ctx context.Context, spec routerprocess.Spec) (routerprocess.Process, error) {
	f.specs <- spec
	return f.base.Start(ctx, spec)
}

func TestContextSettingsReachProcessSpecWithoutInstalling(t *testing.T) {
	specs := make(chan routerprocess.Spec, 2)
	m, installer, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
		o.Factory = settingsRecordingFactory{base: f, specs: specs}
	})
	settings := routerconfig.LongContextRuntimeSettings()
	for _, window := range []int{32768, 65536} {
		settings.ContextTokens = window
		if err := m.UpdateSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		if err := m.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		if spec := <-specs; spec.ContextSize != window {
			t.Fatalf("startup retained fixed context: %d, want %d", spec.ContextSize, window)
		}
	}
	if installer.installs.Load() != 0 || factory.count.Load() != 2 {
		t.Fatal("settings re-downloaded weights or duplicated process")
	}
}

func TestAvailableMemoryIsMeasuredBeforeAndAfterLoad(t *testing.T) {
	reads := 0
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.AvailableMemory = func(context.Context) (uint64, error) { reads++; return uint64(100 - reads), nil }
	})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	if status.MemoryBeforeLoad.Value == nil || *status.MemoryBeforeLoad.Value != 99 || status.MemoryAfterLoad.Value == nil || *status.MemoryAfterLoad.Value != 98 {
		t.Fatalf("load memory observations missing: before=%+v after=%+v", status.MemoryBeforeLoad, status.MemoryAfterLoad)
	}
}

func TestContextReloadKeepsExperimentalRedactionStartupGateAndBudget(t *testing.T) {
	for _, experimental := range []bool{false, true} {
		m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) { o.ExperimentalRedaction = experimental })
		oldFingerprint := m.Identity().ModelFingerprint
		oldHandle, oldErr := m.NewExperimentalRedactionInvoker(oldFingerprint, experimentalFixtureSuite())
		if experimental && oldErr != nil {
			t.Fatal(oldErr)
		}
		if err := m.UpdateSettings(context.Background(), routerconfig.LongContextRuntimeSettings()); err != nil {
			t.Fatal(err)
		}
		currentFingerprint := m.Identity().ModelFingerprint
		_, err := m.NewExperimentalRedactionInvoker(currentFingerprint, experimentalFixtureSuite())
		if experimental {
			if err != nil || !errors.Is(oldHandle.VerifyBinding(oldFingerprint, experimentalFixtureSuite()), ErrCapability) {
				t.Fatal("context changed experimental binding gate", err)
			}
			if m.o.RedactionBudget != 30*time.Second {
				t.Fatal("context settings expanded privacy time budget")
			}
		} else if !errors.Is(err, ErrCapability) {
			t.Fatal("settings change authorized experimental redaction without startup opt-in")
		}
		if m.Status().Capabilities[Redaction].State != "unknown" {
			t.Fatal("context binding accepted privacy quality")
		}
	}
}

func TestUpdateSettingsDrainsProcessPreservesIntentAndInvalidatesEvidence(t *testing.T) {
	m, _, f, store := managerFixture(t, nil)
	if err := m.CheckCapability(context.Background(), Check{Purpose: Judgment, Suite: "fixture-v1", Trial: true, Run: func(context.Context, string) (string, error) { return "verified", nil }}); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableTrial(context.Background(), Judgment); err != nil {
		t.Fatal(err)
	}
	oldIdentity := m.Identity()
	entered, release := make(chan struct{}), make(chan struct{})
	active := use(m, context.Background(), Execution, func(context.Context, string) error { close(entered); <-release; return nil })
	<-entered
	oldProcess := f.last()
	done := make(chan error, 1)
	settings := routerconfig.LongContextRuntimeSettings()
	go func() { done <- m.UpdateSettings(context.Background(), settings) }()
	eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.stopping })
	if err := m.Use(context.Background(), Execution, func(context.Context, string) error { return nil }); !errors.Is(err, ErrDisabled) {
		t.Fatalf("reload admitted new task: %v", err)
	}
	if oldProcess.stops.Load() != 0 {
		t.Fatal("active generation killed before drain")
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatalf("drained generation changed result: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	if status.Runtime != Unloaded || status.PID != 0 || oldProcess.stops.Load() != 1 {
		t.Fatalf("old process retained: %+v", status)
	}
	if !status.Intent.Enabled || !status.Intent.Trial.Judgment || status.Intent.Settings != settings {
		t.Fatalf("intent lost on reload: %+v", status.Intent)
	}
	if store.i != status.Intent {
		t.Fatal("settings not durably persisted")
	}
	if status.Capabilities[Judgment].TrialEvidence || status.Capabilities[Judgment].TrialReady {
		t.Fatal("new context inherited old acceptance")
	}
	if oldIdentity.ModelFingerprint == m.Identity().ModelFingerprint {
		t.Fatal("context missing from runtime/model fingerprint")
	}
	if err := m.Use(WithRuntimeSettings(context.Background(), routerconfig.RuntimeSettings{}), Execution, func(context.Context, string) error { return nil }); !errors.Is(err, ErrSettingsChanged) {
		t.Fatalf("old snapshot admitted after reload: %v", err)
	}
}

func TestExplicitJudgmentUsesSeparateInferenceBudgetAndCallerCancellation(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	settings := routerconfig.LongContextRuntimeSettings()
	if err := m.UpdateSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := m.Use(context.Background(), Judgment, func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining < 25*time.Second || remaining > 31*time.Second {
			t.Fatalf("inference did not get its independent 30 second budget: %s", remaining)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.Use(ctx, Judgment, func(context.Context, string) error { t.Fatal("canceled parent reached inference"); return nil })
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrJudgmentQueueTimeout) {
		t.Fatalf("parent cancellation misclassified: %v", err)
	}
}

func TestTimeoutClassificationForQueuedAndActiveInvocations(t *testing.T) {
	for _, test := range []struct {
		purpose Purpose
		queue   bool
		want    error
	}{{Judgment, true, ErrJudgmentQueueTimeout}, {Judgment, false, ErrJudgmentInferenceTimeout}, {Execution, true, ErrExecutionQueueTimeout}, {Execution, false, ErrExecutionTimeout}} {
		t.Run(test.want.Error(), func(t *testing.T) {
			m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
				o.JudgmentBudget = 20 * time.Millisecond
				o.ExecutionQueueBudget = 20 * time.Millisecond
				o.ExecutionBudget = 20 * time.Millisecond
			})
			var err error
			if test.queue {
				occupied, release := make(chan struct{}), make(chan struct{})
				active := use(m, context.Background(), Redaction, func(context.Context, string) error { close(occupied); <-release; return nil })
				<-occupied
				err = m.Use(context.Background(), test.purpose, func(context.Context, string) error { t.Error("expired queue reached inference"); return nil })
				close(release)
				if activeErr := <-active; activeErr != nil {
					t.Fatal(activeErr)
				}
			} else {
				err = m.Use(context.Background(), test.purpose, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
			}
			if !errors.Is(err, test.want) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wrong stage classification: %v", err)
			}
		})
	}
}
