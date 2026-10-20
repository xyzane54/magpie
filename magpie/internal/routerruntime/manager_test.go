package routerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerprocess/fakeserver"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) {
	if os.Getenv("API_ROUTER_RUNTIME_TEST_HELPER") == "1" {
		fakeserver.Main()
		return
	}
	testenv.Offline()
	testenv.Main(m)
}

type memoryIntent struct {
	mu sync.Mutex
	i  Intent
}

func (s *memoryIntent) Load(context.Context) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.i, nil
}
func (s *memoryIntent) Save(_ context.Context, i Intent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.i = i
	return nil
}

type fixtureInstaller struct {
	installed atomic.Bool
	installs  atomic.Int32
	gate      chan struct{}
}

func (i *fixtureInstaller) Verify(ctx context.Context, _ routermanifest.Model, _ routermanifest.Runtime) (routerprocess.Spec, error) {
	if err := ctx.Err(); err != nil {
		return routerprocess.Spec{}, err
	}
	if !i.installed.Load() {
		return routerprocess.Spec{}, ErrNotInstalled
	}
	return routerprocess.Spec{}, nil
}
func (i *fixtureInstaller) Install(ctx context.Context, _ routermanifest.Model, _ routermanifest.Runtime, p func(Progress)) (routerprocess.Spec, error) {
	i.installs.Add(1)
	p(Progress{Artifact: "fixture", Downloaded: 1, Total: 2})
	if i.gate != nil {
		select {
		case <-i.gate:
		case <-ctx.Done():
			return routerprocess.Spec{}, ctx.Err()
		}
	}
	i.installed.Store(true)
	return routerprocess.Spec{}, nil
}

type fixtureProcess struct {
	pid       int
	healthErr error
	waitErr   error
	done      chan struct{}
	once      sync.Once
	endpoint  string
	unhealthy atomic.Bool
	stops     atomic.Int32
	afterStop func()
}

func (p *fixtureProcess) Endpoint() string { return p.endpoint }
func (p *fixtureProcess) PID() int {
	if p.pid != 0 {
		return p.pid
	}
	return 1234
}
func (p *fixtureProcess) Done() <-chan struct{} { return p.done }
func (p *fixtureProcess) Err() error {
	if p.waitErr != nil {
		return p.waitErr
	}
	return ErrFault
}
func (p *fixtureProcess) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.unhealthy.Load() {
		return ErrFault
	}
	return p.healthErr
}
func (p *fixtureProcess) Stop(context.Context) error {
	p.once.Do(func() {
		p.stops.Add(1)
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	})
	if p.afterStop != nil {
		p.afterStop()
	}
	return nil
}

type fixtureFactory struct {
	count     atomic.Int32
	mu        sync.Mutex
	processes []*fixtureProcess
	gate      chan struct{}
	endpoint  string
	unhealthy bool
}

func (f *fixtureFactory) Start(ctx context.Context, _ routerprocess.Spec) (routerprocess.Process, error) {
	f.count.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := &fixtureProcess{done: make(chan struct{}), endpoint: f.endpoint}
	p.unhealthy.Store(f.unhealthy)
	f.mu.Lock()
	f.processes = append(f.processes, p)
	f.mu.Unlock()
	return p, nil
}
func (f *fixtureFactory) last() *fixtureProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processes[len(f.processes)-1]
}
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for !fn() {
		select {
		case <-timeout:
			t.Fatal("state did not converge")
		case <-time.After(time.Millisecond):
		}
	}
}
func managerFixture(t *testing.T, edit func(*Options, *fixtureInstaller, *fixtureFactory)) (*Manager, *fixtureInstaller, *fixtureFactory, *memoryIntent) {
	t.Helper()
	i := &fixtureInstaller{}
	i.installed.Store(true)
	f := &fixtureFactory{}
	s := &memoryIntent{i: Intent{Enabled: true, Selected: "spark-x2.5-1.7B"}}
	o := Options{Catalog: routermanifest.Fixed(), RuntimeID: "windows-amd64-cpu", Store: s, Installer: i, Factory: f}
	if edit != nil {
		edit(&o, i, f)
	}
	m, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m, i, f, s
}
func use(m *Manager, ctx context.Context, p Purpose, fn func(context.Context, string) error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- m.Use(ctx, p, fn) }()
	return done
}
func noWork(context.Context, string) error { return nil }

func TestExplicitIntentInstallationAndRestart(t *testing.T) {
	m, i, f, s := managerFixture(t, func(o *Options, i *fixtureInstaller, _ *fixtureFactory) {
		o.Store = &memoryIntent{}
		i.installed.Store(false)
	})
	if err := m.Install(); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if f.count.Load() != 0 || i.installs.Load() != 0 {
		t.Fatal("startup started resources")
	}
	if err := m.Select(context.Background(), "spark-x2.5-1.7B"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrNotInstalled) {
		t.Fatal(err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Installation == "installed" })
	if f.count.Load() != 0 {
		t.Fatal("installation auto-loaded model")
	}
	if err := m.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatal(err)
	}
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrDisabled) {
			t.Fatal(err)
		}
	}
	if f.count.Load() != 1 {
		t.Fatal("disabled model relaunched")
	}
	_ = s // The fixture's replacement repository is also read back directly.
	intent, _ := m.o.Store.Load(context.Background())
	if intent.Enabled {
		t.Fatal("disable not durable")
	}
	m2, err := New(context.Background(), m.o)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close(context.Background())
	if m2.Status().Intent.Enabled || m2.Status().Installation != "installed" {
		t.Fatal(m2.Status())
	}
}

func TestInstallCancellationNeverClaimsInstalled(t *testing.T) {
	m, i, _, _ := managerFixture(t, func(_ *Options, i *fixtureInstaller, _ *fixtureFactory) {
		i.installed.Store(false)
		i.gate = make(chan struct{})
	})
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return i.installs.Load() == 1 })
	if err := m.CancelInstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Status().Installation != "canceled" || i.installed.Load() {
		t.Fatal(m.Status())
	}
	close(i.gate)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Installation == "installed" })
}

func TestColdLoadReverificationClearsDamagedInstalledStatus(t *testing.T) {
	m, i, f, _ := managerFixture(t, nil)
	i.installed.Store(false)
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrFault) {
		t.Fatal(err)
	}
	if m.Status().Installation == "installed" || f.count.Load() != 0 {
		t.Fatal("damaged files claimed installed or launched", m.Status())
	}
}

type uncooperativeProcess struct{ fixtureProcess }

func (p *uncooperativeProcess) Stop(context.Context) error { return ErrFault }

type uncooperativeFactory struct {
	p      *uncooperativeProcess
	starts atomic.Int32
}

func (f *uncooperativeFactory) Start(context.Context, routerprocess.Spec) (routerprocess.Process, error) {
	f.starts.Add(1)
	return f.p, nil
}

func TestFailedLoadRetainsUnreapedChildOwnership(t *testing.T) {
	expired := make(chan time.Time)
	p := &uncooperativeProcess{fixtureProcess: fixtureProcess{done: make(chan struct{})}}
	p.unhealthy.Store(true)
	f := &uncooperativeFactory{p: p}
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.Factory = f
		o.After = func(d time.Duration) <-chan time.Time {
			if d == 60*time.Second {
				return expired
			}
			return time.After(d)
		}
	})
	done := use(m, context.Background(), Execution, noWork)
	eventually(t, func() bool { return f.starts.Load() == 1 })
	expired <- time.Now()
	if err := <-done; !errors.Is(err, ErrFault) {
		t.Fatal(err)
	}
	if m.Status().PID == 0 {
		t.Fatal("lost unreaped child ownership")
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrFault) {
		t.Fatal(err)
	}
	if f.starts.Load() != 1 {
		t.Fatal("started a second child while first still alive")
	}
	_ = p.fixtureProcess.Stop(context.Background())
	eventually(t, func() bool { return m.Status().PID == 0 })
}

func TestEnableRejectsSelectionChangedDuringDeviceCheck(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Select(context.Background(), "spark-x2.5-4B"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableSelected(context.Background(), "spark-x2.5-1.7B"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if m.Status().Intent.Enabled {
		t.Fatal("enabled a model different from checked selection")
	}
}

func TestConcurrentColdStartSingleProcessAndCallerCancellation(t *testing.T) {
	m, _, f, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.gate = make(chan struct{}) })
	ctx, cancel := context.WithCancel(context.Background())
	first := use(m, ctx, Judgment, noWork)
	eventually(t, func() bool { return f.count.Load() == 1 })
	done := make([]<-chan error, 10)
	for n := range done {
		done[n] = use(m, context.Background(), Execution, noWork)
	}
	eventually(t, func() bool { return m.Status().Waiting == 11 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(f.gate)
	for _, d := range done {
		if err := <-d; err != nil {
			t.Fatal(err)
		}
	}
	if f.count.Load() != 1 {
		t.Fatal("concurrent cold starts spawned multiple children")
	}
}

func TestSharedQueueBoundaryCancellationAndFIFO(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	hold := make(chan struct{})
	entered := make(chan struct{})
	first := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-hold:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make([]<-chan error, QueueLimit)
	var mu sync.Mutex
	var order []int
	for n := range waiting {
		requestCtx := context.Background()
		if n == 0 {
			requestCtx = ctx
		}
		waiting[n] = use(m, requestCtx, []Purpose{Execution, Judgment, Redaction}[n%3], func(context.Context, string) error { mu.Lock(); order = append(order, n); mu.Unlock(); return nil })
		eventually(t, func() bool { return m.Status().Waiting == n+1 })
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	cancel()
	if err := <-waiting[0]; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Waiting == 15 })
	close(hold)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	for _, d := range waiting[1:] {
		if err := <-d; err != nil {
			t.Fatal(err)
		}
	}
	for n, v := range order {
		if v != n+1 {
			t.Fatalf("FIFO order %v", order)
		}
	}
}

func TestExecutionQueueBudgetIsNotGenerationBudget(t *testing.T) {
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.ExecutionQueueBudget = 30 * time.Millisecond
		o.ExecutionBudget = time.Minute
	})
	hold := make(chan struct{})
	entered := make(chan struct{})
	first := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error {
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) < 50*time.Second {
			return errors.New("queue budget reused for generation")
		}
		close(entered)
		select {
		case <-hold:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-entered
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(hold)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	for _, p := range []Purpose{Judgment, Redaction} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.Use(ctx, p, noWork); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestControlledLoadingTimeoutReapsAndIdleProtectsOccupancy(t *testing.T) {
	loadExpired := make(chan time.Time)
	m, _, f, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
		f.unhealthy = true
		o.After = func(d time.Duration) <-chan time.Time {
			if d == 60*time.Second {
				return loadExpired
			}
			return time.After(d)
		}
	})
	done := use(m, context.Background(), Execution, noWork)
	eventually(t, func() bool { return f.count.Load() == 1 && m.Status().Runtime == Loading })
	loadExpired <- time.Now()
	if err := <-done; !errors.Is(err, ErrFault) {
		t.Fatal(err)
	}
	if f.last().stops.Load() != 1 || m.Status().Runtime != Fault {
		t.Fatal("loading timeout leaked process", m.Status())
	}

	var nanos atomic.Int64
	nanos.Store(time.Now().UnixNano())
	m2, _, f2, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.Now = func() time.Time { return time.Unix(0, nanos.Load()) }
	})
	hold := make(chan struct{})
	entered := make(chan struct{})
	active := use(m2, context.Background(), Execution, func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-hold:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-entered
	waiting := use(m2, context.Background(), Redaction, noWork)
	eventually(t, func() bool { return m2.Status().Waiting == 1 })
	nanos.Add(int64(time.Hour))
	if err := m2.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f2.last().stops.Load() != 0 {
		t.Fatal("unloaded occupied runtime")
	}
	close(hold)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	nanos.Add(int64(15*time.Minute - time.Nanosecond))
	_ = m2.Sweep(context.Background())
	if f2.last().stops.Load() != 0 {
		t.Fatal("unloaded before full idle interval")
	}
	nanos.Add(1)
	_ = m2.Sweep(context.Background())
	if f2.last().stops.Load() != 1 || m2.Status().Runtime != Unloaded || !m2.Status().Intent.Enabled {
		t.Fatal(m2.Status())
	}
	if err := m2.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatal(err)
	}
	if f2.count.Load() != 2 {
		t.Fatal("enabled idle runtime did not reload")
	}
}

func TestDisableDrainsActiveAndRejectsQueuedAndFutureRequests(t *testing.T) {
	m, _, f, s := managerFixture(t, nil)
	hold := make(chan struct{})
	entered := make(chan struct{})
	active := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-hold:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-entered
	queued := use(m, context.Background(), Judgment, noWork)
	eventually(t, func() bool { return m.Status().Waiting == 1 })
	disabled := make(chan error, 1)
	go func() { disabled <- m.Disable(context.Background()) }()
	eventually(t, func() bool { return !m.Status().Intent.Enabled })
	if err := <-queued; !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if f.last().stops.Load() != 0 {
		t.Fatal("killed in-flight generation before drain")
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	close(hold)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	i, _ := s.Load(context.Background())
	if i.Enabled || f.last().stops.Load() != 1 {
		t.Fatal("disable not persisted/reaped")
	}
}

func TestRequestedStopDoesNotReportProcessFault(t *testing.T) {
	for _, operation := range []string{"disable", "close", "idle"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Unix(100, 0)
			m, _, f, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
				o.Now = func() time.Time { return now }
			})
			if err := m.Use(context.Background(), Execution, noWork); err != nil {
				t.Fatal(err)
			}
			var during Status
			f.last().afterStop = func() {
				// Done is closed, but Stop cannot return until the actual watcher
				// has handled it. This forces the ordering observed with 4B.
				m.workers.Wait()
				during = m.Status()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var err error
			switch operation {
			case "disable":
				err = m.Disable(ctx)
			case "close":
				err = m.Close(ctx)
			case "idle":
				now = now.Add(time.Hour)
				err = m.Sweep(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if during.Runtime != Unloading || during.Error != "" || during.PID != 0 {
				t.Fatalf("requested stop reported a process fault before Stop returned: %+v", during)
			}
			status := m.Status()
			if status.Runtime != Unloaded || status.Error != "" || status.PID != 0 || status.Active != 0 || status.Waiting != 0 {
				t.Fatalf("requested stop retained a process fault: %+v", status)
			}
		})
	}
}

type delayedWatchProcess struct {
	fixtureProcess
	firstDone    atomic.Bool
	watchEntered chan struct{}
	watchRelease chan struct{}
}

func (p *delayedWatchProcess) Done() <-chan struct{} {
	if p.firstDone.CompareAndSwap(false, true) {
		close(p.watchEntered)
		<-p.watchRelease
	}
	return p.fixtureProcess.Done()
}

type delayedWatchFactory struct{ p *delayedWatchProcess }

func (f delayedWatchFactory) Start(context.Context, routerprocess.Spec) (routerprocess.Process, error) {
	return f.p, nil
}

func TestExitedProcessBeforeStopRetainsFaultWithDelayedWatcher(t *testing.T) {
	for _, operation := range []string{"disable", "close", "idle"} {
		for _, priorError := range []string{"", "unload_failed", "drain_timed_out"} {
			t.Run(operation+"/"+priorError, func(t *testing.T) {
				p := &delayedWatchProcess{fixtureProcess: fixtureProcess{done: make(chan struct{})},
					watchEntered: make(chan struct{}), watchRelease: make(chan struct{})}
				var once sync.Once
				releaseWatch := func() { once.Do(func() { close(p.watchRelease) }) }
				defer releaseWatch()
				now := time.Unix(100, 0)
				m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
					o.Factory = delayedWatchFactory{p}
					o.Now = func() time.Time { return now }
				})
				if err := m.Use(context.Background(), Execution, noWork); err != nil {
					t.Fatal(err)
				}
				<-p.watchEntered
				m.mu.Lock()
				m.lastError = priorError
				m.mu.Unlock()
				// The child exits before Stop is requested, but its watcher cannot
				// observe Done until the lifecycle operation has cleared m.proc.
				p.once.Do(func() { close(p.done) })
				stopEntered := make(chan struct{})
				p.afterStop = func() { close(stopEntered) }
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				stopped := make(chan error, 1)
				now = now.Add(time.Hour)
				go func() {
					switch operation {
					case "disable":
						stopped <- m.Disable(ctx)
					case "close":
						stopped <- m.Close(ctx)
					case "idle":
						stopped <- m.Sweep(ctx)
					}
				}()
				select {
				case <-stopEntered:
				case <-ctx.Done():
					t.Fatal("lifecycle operation never reached Stop")
				}
				eventually(t, func() bool { return m.Status().PID == 0 })
				wantError := priorError
				if wantError == "" {
					wantError = "process_exited"
				}
				if status := m.Status(); status.Runtime != Unloaded || status.Error != wantError {
					t.Errorf("prior process exit lost before watcher release: %+v; want %q", status, wantError)
				}
				releaseWatch()
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
				m.workers.Wait()
				if status := m.Status(); status.Runtime != Unloaded || status.Error != wantError {
					t.Fatalf("late watcher changed prior process exit: %+v; want %q", status, wantError)
				}
			})
		}
	}
}

func TestUnexpectedExitDuringDisableDrainStillReportsFault(t *testing.T) {
	m, _, f, _ := managerFixture(t, nil)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseCall := func() { once.Do(func() { close(release) }) }
	defer releaseCall()
	active := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	})
	<-entered
	disabled := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { disabled <- m.Disable(ctx) }()
	eventually(t, func() bool { return !m.Status().Intent.Enabled })
	// No Stop has been requested while the active call drains. A real exit
	// must still cancel that call and publish Fault, even though stopping=true.
	p := f.last()
	p.once.Do(func() { close(p.done) })
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("unexpected exit did not cancel the active call")
	}
	status := m.Status()
	if status.Runtime != Fault || status.Error != "process_exited" || status.PID != 0 {
		t.Errorf("unexpected exit was hidden during drain: %+v", status)
	}
	releaseCall()
	if err := <-active; !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if p.stops.Load() != 0 {
		t.Fatal("the unexpected exit was attributed to a requested Stop")
	}
}

func TestCrashCancelsActiveAndCloseReapsWithoutDisablingDurableIntent(t *testing.T) {
	m, _, f, s := managerFixture(t, nil)
	entered := make(chan struct{})
	active := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	<-entered
	_ = f.last().Stop(context.Background())
	if err := <-active; !errors.Is(err, ErrFault) {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Runtime == Fault })
	if err := m.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.last().stops.Load() != 1 {
		t.Fatal("Router close leaked owned model")
	}
	i, _ := s.Load(context.Background())
	if !i.Enabled {
		t.Fatal("service shutdown erased user intent")
	}
	if err := m.Use(context.Background(), Execution, noWork); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestCallerCancellationAndPurposeDeadlinesRemainDistinctFromCrash(t *testing.T) {
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.ExecutionBudget = 20 * time.Millisecond
		o.JudgmentBudget = 20 * time.Millisecond
		o.RedactionBudget = 20 * time.Millisecond
	})
	for _, purpose := range []Purpose{Execution, Judgment, Redaction} {
		if err := m.Use(context.Background(), purpose, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(purpose, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := use(m, ctx, Execution, func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDefaultQueueWaitDoesNotConsumeInferenceBudget(t *testing.T) {
	const budget = 80 * time.Millisecond
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.JudgmentBudget = budget
		o.RedactionBudget = budget
	})
	for _, purpose := range []Purpose{Judgment, Redaction} {
		entered := make(chan struct{})
		release := make(chan struct{})
		holder := use(m, context.Background(), Execution, func(context.Context, string) error {
			close(entered)
			<-release
			return nil
		})
		<-entered
		queued := use(m, context.Background(), purpose, func(ctx context.Context, _ string) error {
			select {
			case <-time.After(budget * 3 / 5):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		eventually(t, func() bool { return m.Status().Waiting == 1 })
		time.Sleep(budget * 3 / 5)
		close(release)
		if err := <-holder; err != nil {
			t.Fatal(purpose, err)
		}
		if err := <-queued; err != nil {
			t.Fatalf("%v inference failed after queue wait consumed stage time: %v", purpose, err)
		}
	}
}

func TestForcedDisableIsNotReportedAsClientCancellationOrCrash(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	entered := make(chan struct{})
	done := use(m, context.Background(), Execution, func(ctx context.Context, _ string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Disable(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

type pausedContext struct {
	context.Context
	entered, resume chan struct{}
	once            sync.Once
}

func (c *pausedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	<-c.resume
	return nil
}

func TestOldColdWaiterCannotRestartAfterDisableAndReenable(t *testing.T) {
	m, _, f, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.gate = make(chan struct{}) })
	ctx := &pausedContext{Context: context.Background(), entered: make(chan struct{}), resume: make(chan struct{})}
	done := use(m, ctx, Execution, noWork)
	t.Cleanup(func() {
		select {
		case <-ctx.resume:
		default:
			close(ctx.resume)
		}
		select {
		case <-f.gate:
		default:
			close(f.gate)
		}
	})
	<-ctx.entered
	eventually(t, func() bool { return f.count.Load() == 1 })
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(f.gate)
	close(ctx.resume)
	if err := <-done; !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if f.count.Load() != 1 {
		t.Fatal("old request restarted disabled runtime")
	}
	if err := m.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatal(err)
	}
	if f.count.Load() != 2 {
		t.Fatal("explicitly reenabled runtime did not load")
	}
}

func TestHealthIsIndependentOfVersionedCapabilities(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	if err := m.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Purpose{Judgment, Execution, Redaction} {
		if m.Status().Capabilities[p].State != "unknown" {
			t.Fatal("health passed capability")
		}
	}
	if err := m.CheckCapability(context.Background(), Check{Purpose: Judgment}); !errors.Is(err, ErrCapability) {
		t.Fatal(err)
	}
	if err := m.CheckCapability(context.Background(), Check{Purpose: Execution, Suite: "execution-fixture-v1", Run: func(context.Context, string) (string, error) { return "fixture-sample-sha256", nil }}); err != nil {
		t.Fatal(err)
	}
	if err := m.Require(Execution); err != nil {
		t.Fatal(err)
	}
	if err := m.Require(Redaction); !errors.Is(err, ErrCapability) {
		t.Fatal(err)
	}
	previous := m.Status().Capabilities[Execution].Fingerprint
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Select(context.Background(), "spark-x2.5-4B"); err != nil {
		t.Fatal(err)
	}
	if m.Status().Capabilities[Execution].State != "unknown" || m.Status().Capabilities[Execution].Fingerprint == previous {
		t.Fatal("reused capability from old model")
	}
}

func TestGenerationPreservesToolsAndExecutionSemantics(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) { f.endpoint = server.URL })
	body := []byte(`{"messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"read"}}],"chat_template_kwargs":{"enable_thinking":true,"other":"kept"},"stream":true}`)
	for _, p := range []Purpose{Execution, Judgment, Redaction} {
		if err := m.Generate(context.Background(), p, body, func(r *http.Response) error {
			data, err := io.ReadAll(r.Body)
			if len(data) == 0 {
				return errors.New("empty SSE")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n, b := range bodies {
		if b["cache_prompt"] != true || len(b["tools"].([]any)) != 1 {
			t.Fatal(b)
		}
		kwargs := b["chat_template_kwargs"].(map[string]any)
		if kwargs["other"] != "kept" || kwargs["enable_thinking"] != (n == 0) {
			t.Fatal(kwargs)
		}
	}
}

type scriptProcess struct {
	fixtureProcess
	pid int
	err error
}

func (p *scriptProcess) PID() int   { return p.pid }
func (p *scriptProcess) Err() error { return p.err }
func (p *scriptProcess) Stop(context.Context) error {
	p.stops.Add(1)
	p.once.Do(func() { close(p.done) })
	return nil
}

type scriptFactory struct {
	mu    sync.Mutex
	procs []*scriptProcess
	calls int
}

func (f *scriptFactory) Start(context.Context, routerprocess.Spec) (routerprocess.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	f.calls++
	if idx >= len(f.procs) {
		return nil, errors.New("unexpected Start call")
	}
	return f.procs[idx], nil
}

func TestLoadRetriesOnceOnEphemeralPortImmediateExitAndPreservesDependencyExitError(t *testing.T) {
	exited := &scriptProcess{fixtureProcess: fixtureProcess{endpoint: "http://127.0.0.1:4101", done: make(chan struct{})}, pid: 4101, err: errors.New("bind failed")}
	exited.unhealthy.Store(true)
	exited.once.Do(func() { close(exited.done) })
	ready := &scriptProcess{fixtureProcess: fixtureProcess{endpoint: "http://127.0.0.1:4102", done: make(chan struct{})}, pid: 4102}
	sf := &scriptFactory{procs: []*scriptProcess{exited, ready}}
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.Factory = sf
	})
	if err := m.Use(context.Background(), Execution, noWork); err != nil {
		t.Fatalf("expected ephemeral port collision retry to succeed, got %v", err)
	}
	if sf.calls != 2 || m.Status().PID != 4102 || exited.stops.Load() != 1 {
		t.Fatalf("unexpected retry state: calls=%d pid=%d stops=%d", sf.calls, m.Status().PID, exited.stops.Load())
	}

	depExit := &scriptProcess{
		fixtureProcess: fixtureProcess{endpoint: "http://127.0.0.1:4103", done: make(chan struct{})},
		pid:            4103,
		err:            &routerprocess.DependencyError{Code: routerprocess.DependencyMissingCode},
	}
	depExit.unhealthy.Store(true)
	depExit.once.Do(func() { close(depExit.done) })
	depFactory := &scriptFactory{procs: []*scriptProcess{depExit}}
	m2, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.Factory = depFactory
	})
	err := m2.Use(context.Background(), Execution, noWork)
	if !errors.Is(err, ErrFault) || dependencyCode(err) != routerprocess.DependencyMissingCode {
		t.Fatalf("expected ErrFault joined with dependency error, got %v", err)
	}
	if depFactory.calls != 1 || m2.Status().Installation != "not_installed" || m2.Status().Error != routerprocess.DependencyMissingCode {
		t.Fatalf("expected no retry and dependency classification on dependency exit, calls=%d status=%+v", depFactory.calls, m2.Status())
	}
}

func TestInstallResetsProgressOnStartAndCompletion(t *testing.T) {
	m, i, _, _ := managerFixture(t, func(_ *Options, i *fixtureInstaller, _ *fixtureFactory) {
		i.installed.Store(false)
		i.gate = make(chan struct{})
	})
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Progress.Total == 2 })
	if err := m.CancelInstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Status().Installation != "canceled" || m.Status().Progress.Total == 0 {
		t.Fatalf("expected canceled install to preserve last progress for diagnostics, got %+v", m.Status())
	}

	close(i.gate)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Installation == "installed" })
	if s := m.Status(); s.Progress != (Progress{}) {
		t.Fatalf("expected completed install to clear progress, got %+v", s.Progress)
	}
}

type stagedFailureInstaller struct {
	runtimeGate chan struct{}
}

func (i *stagedFailureInstaller) Verify(context.Context, routermanifest.Model, routermanifest.Runtime) (routerprocess.Spec, error) {
	return routerprocess.Spec{}, ErrNotInstalled
}

func (i *stagedFailureInstaller) Install(ctx context.Context, _ routermanifest.Model, _ routermanifest.Runtime, p func(Progress)) (routerprocess.Spec, error) {
	p(Progress{Artifact: "dep", Stage: "dependency", Step: 1, TotalSteps: 4, Host: "aka.ms", Downloaded: 100, Total: 100})
	p(Progress{Artifact: "runtime", Stage: "runtime", Step: 2, TotalSteps: 4, Host: "github.com", Downloaded: 0, Total: 0})
	select {
	case <-i.runtimeGate:
		return routerprocess.Spec{}, context.DeadlineExceeded
	case <-ctx.Done():
		return routerprocess.Spec{}, ctx.Err()
	}
}

func TestInstallStageMetadataAndErrorClassificationPreservesReadyDependencies(t *testing.T) {
	staged := &stagedFailureInstaller{runtimeGate: make(chan struct{})}
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.Installer = staged
	})
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Progress.Stage == "runtime" })
	during := m.Status()
	if during.Dependencies.State != "ready" || during.Progress.Step != 2 || during.Progress.TotalSteps != 4 || during.Progress.Host != "github.com" {
		t.Fatalf("expected dependency state to become ready on stage 2, got %+v", during)
	}
	close(staged.runtimeGate)
	eventually(t, func() bool { return m.Status().Installation == "failed" })
	failed := m.Status()
	if failed.Dependencies.State != "ready" {
		t.Fatalf("expected dependency state to remain ready after runtime stage failure, got %+v", failed.Dependencies)
	}
	if failed.Error != "local_model_install_runtime_timeout" || failed.Progress.Stage != "runtime" || failed.Progress.Reason != "timeout" || failed.Progress.Host != "github.com" {
		t.Fatalf("expected runtime timeout error code and progress reason, got error=%q progress=%+v", failed.Error, failed.Progress)
	}
}

