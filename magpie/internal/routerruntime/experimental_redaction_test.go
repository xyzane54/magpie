package routerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func experimentalFixtureSuite() string {
	return experimentalRedactionSuitePrefix + strings.Repeat("a", 64)
}

func experimentalFixtureInvoker(t *testing.T, m *Manager) *ExperimentalRedactionInvoker {
	t.Helper()
	fingerprint := m.Status().Capabilities[Redaction].Fingerprint
	invoker, err := m.NewExperimentalRedactionInvoker(fingerprint, experimentalFixtureSuite())
	if err != nil {
		t.Fatal(err)
	}
	return invoker
}

func TestExperimentalRedactionRequiresStartupChoiceAndExactBinding(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		t.Run(modelID, func(t *testing.T) {
			ordinary, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
				o.Store = &memoryIntent{i: Intent{Enabled: true, Selected: modelID}}
			})
			fingerprint := ordinary.Status().Capabilities[Redaction].Fingerprint
			if _, err := ordinary.NewExperimentalRedactionInvoker(fingerprint, experimentalFixtureSuite()); !errors.Is(err, ErrCapability) {
				t.Fatal("ordinary startup admitted an experimental handle", err)
			}
			if ordinary.o.RedactionBudget != 15*time.Second || factory.count.Load() != 0 {
				t.Fatal("ordinary startup changed its budget or loaded a model")
			}
			m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
				o.ExperimentalRedaction = true
				o.Store = &memoryIntent{i: Intent{Enabled: true, Selected: modelID}}
			})
			if m.o.RedactionBudget != 30*time.Second {
				t.Fatal("experimental startup did not select the 30-second budget")
			}
			invoker := experimentalFixtureInvoker(t, m)
			fingerprint = m.Status().Capabilities[Redaction].Fingerprint
			before := m.Status().Capabilities
			if err := invoker.VerifyBinding(fingerprint, experimentalFixtureSuite()); err != nil {
				t.Fatal(err)
			}
			for _, p := range []Purpose{Judgment, Execution, "invalid"} {
				if err := invoker.Require(p); !errors.Is(err, ErrCapability) {
					t.Fatal("experimental handle admitted another purpose", p, err)
				}
				if err := invoker.Invoke(context.Background(), p, []byte(`{"messages":[]}`), func(*http.Response) error { return nil }); !errors.Is(err, ErrCapability) {
					t.Fatal("experimental invocation admitted another purpose", p, err)
				}
			}
			for _, suite := range []string{"", "privacy-trial-v1:", "privacy-go-v5:" + strings.Repeat("a", 64), experimentalRedactionSuitePrefix + strings.Repeat("a", 63), experimentalRedactionSuitePrefix + strings.Repeat("A", 64), experimentalFixtureSuite() + " ", experimentalFixtureSuite() + "a"} {
				if _, err := m.NewExperimentalRedactionInvoker(fingerprint, suite); !errors.Is(err, ErrCapability) {
					t.Fatal("experimental handle accepted an incomplete or different suite", err)
				}
			}
			if err := invoker.VerifyBinding("different", experimentalFixtureSuite()); !errors.Is(err, ErrCapability) {
				t.Fatal("extractor could change the fingerprint", err)
			}
			if err := invoker.VerifyBinding(fingerprint, experimentalRedactionSuitePrefix+strings.Repeat("b", 64)); !errors.Is(err, ErrCapability) {
				t.Fatal("extractor could change the suite", err)
			}
			if _, err := m.NewExperimentalRedactionInvoker("different", experimentalFixtureSuite()); !errors.Is(err, ErrCapability) {
				t.Fatal("constructor accepted another fingerprint", err)
			}
			if err := m.Require(Redaction); !errors.Is(err, ErrCapability) {
				t.Fatal("experimental binding accepted a formal capability", err)
			}
			if !reflect.DeepEqual(before, invoker.Status().Capabilities) || factory.count.Load() != 0 {
				t.Fatal("experimental binding changed capability evidence or loaded a model")
			}
		})
	}
}

func TestExperimentalRedactionRejectsDisabledClosedAndMissingSelection(t *testing.T) {
	{
		m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
			o.ExperimentalRedaction = true
			o.Store = &memoryIntent{}
		})
		_, err := m.NewExperimentalRedactionInvoker(m.Status().Capabilities[Redaction].Fingerprint, experimentalFixtureSuite())
		if err == nil || factory.count.Load() != 0 {
			t.Fatal("missing selection admitted or loaded an experimental model")
		}
	}
	m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.ExperimentalRedaction = true
		o.RedactionBudget = time.Minute
	})
	if m.o.RedactionBudget != 30*time.Second {
		t.Fatal("experimental budget exceeded its 30-second cap")
	}
	invoker := experimentalFixtureInvoker(t, m)
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := invoker.Require(Redaction); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled manager remained trial-admissible", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := invoker.Require(Redaction); !errors.Is(err, ErrDisabled) || factory.count.Load() != 0 {
		t.Fatal("closed trial manager restarted a process", err)
	}
}

func TestExperimentalRedactionSelectionChangeInvalidatesHandle(t *testing.T) {
	m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, _ *fixtureFactory) {
		o.ExperimentalRedaction = true
	})
	previous := experimentalFixtureInvoker(t, m)
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Select(context.Background(), "spark-x2.5-4B"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := previous.Require(Redaction); !errors.Is(err, ErrCapability) {
		t.Fatal("previous model's experimental handle remained active", err)
	}
	current := experimentalFixtureInvoker(t, m)
	if err := current.Require(Redaction); err != nil {
		t.Fatal("selected 4B model did not receive its own bound trial handle", err)
	}
	if err := m.Require(Redaction); !errors.Is(err, ErrCapability) || factory.count.Load() != 0 || m.Status().Capabilities[Redaction].State != "unknown" {
		t.Fatal("selection or trial binding loaded a model or accepted formal redaction", err)
	}
}

func TestExperimentalRedactionUsesOwnedTransportAndPreservesFormalUnknown(t *testing.T) {
	for _, modelID := range []string{"spark-x2.5-1.7B", "spark-x2.5-4B"} {
		t.Run(modelID, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Kwargs map[string]any `json:"chat_template_kwargs"`
					Cache  bool           `json:"cache_prompt"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Kwargs["enable_thinking"] != false || !body.Cache {
					t.Error("experimental call changed the shared transport contract")
				}
				_, _ = io.WriteString(w, `{"choices":[]}`)
			}))
			defer server.Close()
			m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
				o.ExperimentalRedaction = true
				o.Store = &memoryIntent{i: Intent{Enabled: true, Selected: modelID}}
				f.endpoint = server.URL
			})
			invoker := experimentalFixtureInvoker(t, m)
			before := m.Status().Capabilities
			parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			parentDeadline, _ := parent.Deadline()
			err := invoker.Invoke(parent, Redaction, []byte(`{"messages":[],"chat_template_kwargs":{"enable_thinking":true}}`), func(response *http.Response) error {
				deadline, ok := response.Request.Context().Deadline()
				if !ok || !deadline.Equal(parentDeadline) || m.Status().Active != 1 {
					t.Error("trial extended its parent deadline or lost the only slot")
				}
				_, err := io.ReadAll(response.Body)
				return err
			})
			if err != nil || requests.Load() != 1 || factory.count.Load() != 1 {
				t.Fatal("experimental call did not use the one owned manager", err)
			}
			if err := m.Invoke(context.Background(), Redaction, []byte(`{"messages":[]}`), func(*http.Response) error { return nil }); !errors.Is(err, ErrCapability) {
				t.Fatal("trial execution admitted a formal Invoke", err)
			}
			if !reflect.DeepEqual(before, m.Status().Capabilities) || requests.Load() != 1 {
				t.Fatal("trial changed real capability evidence or dispatched a formal call")
			}
		})
	}
}

func TestExperimentalRedactionCancelReleasesSlotAndSameProcessRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: synthetic\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer func() { cancel(); server.Close() }()
	m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
		o.ExperimentalRedaction = true
		f.endpoint = server.URL
	})
	invoker := experimentalFixtureInvoker(t, m)
	consuming := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- invoker.Invoke(ctx, Redaction, []byte(`{"messages":[]}`), func(response *http.Response) error {
			close(consuming)
			_, err := io.Copy(io.Discard, response.Body)
			return err
		})
	}()
	awaitDiagnosticState(t, consuming)
	pid := m.Status().PID
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("trial cancellation lost its identity", err)
	}
	if s := m.Status(); s.Active != 0 || s.Waiting != 0 || s.PID != pid || s.Capabilities[Redaction].State != "unknown" {
		t.Fatal("trial cancellation leaked occupancy or changed capability")
	}
	if err := invoker.Invoke(context.Background(), Redaction, []byte(`{"messages":[]}`), func(response *http.Response) error { _, err := io.Copy(io.Discard, response.Body); return err }); err != nil {
		t.Fatal(err)
	}
	if factory.count.Load() != 1 || m.Status().PID != pid {
		t.Fatal("trial recovery created a different process")
	}
}

func TestExperimentalRedactionDisableRejectsQueuedAndDrainsActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()
	m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
		o.ExperimentalRedaction, f.endpoint = true, server.URL
	})
	invoker := experimentalFixtureInvoker(t, m)
	consuming, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- invoker.Invoke(ctx, Redaction, []byte(`{"messages":[]}`), func(response *http.Response) error {
			if _, err := io.ReadAll(response.Body); err != nil {
				return err
			}
			close(consuming)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	awaitDiagnosticState(t, consuming)
	queued := make(chan error, 1)
	go func() {
		queued <- invoker.Invoke(ctx, Redaction, []byte(`{"messages":[]}`), func(*http.Response) error { return nil })
	}()
	eventually(t, func() bool { return m.Status().Waiting == 1 })
	disabled := make(chan error, 1)
	go func() { disabled <- m.Disable(ctx) }()
	if err := <-queued; !errors.Is(err, ErrDisabled) {
		t.Fatal("disable dispatched a queued trial", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Active != 0 || s.Waiting != 0 || s.PID != 0 || s.Capabilities[Redaction].State != "unknown" || requests.Load() != 1 {
		t.Fatal("disable leaked a trial slot/process, sent a queued request, or changed capability")
	}
}

func TestExperimentalAndFormalAdmissionAreRecheckedAfterQueue(t *testing.T) {
	for _, experimental := range []bool{false, true} {
		t.Run(map[bool]string{false: "formal", true: "experimental"}[experimental], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(200) }))
			defer server.Close()
			m, _, _, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
				o.ExperimentalRedaction, f.endpoint = experimental, server.URL
			})
			var invoke func(context.Context, Purpose, []byte, func(*http.Response) error) error
			if experimental {
				invoke = experimentalFixtureInvoker(t, m).Invoke
			} else {
				if err := m.CheckCapability(ctx, Check{Purpose: Redaction, Suite: "formal-fixture-v1", Run: func(context.Context, string) (string, error) { return "test-fixture", nil }}); err != nil {
					t.Fatal(err)
				}
				invoke = m.Invoke
			}
			occupied, release := make(chan struct{}), make(chan struct{})
			active := use(m, ctx, Execution, func(context.Context, string) error {
				close(occupied)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			awaitDiagnosticState(t, occupied)
			queued := make(chan error, 1)
			go func() {
				queued <- invoke(ctx, Redaction, []byte(`{"messages":[]}`), func(*http.Response) error { return nil })
			}()
			eventually(t, func() bool { return m.Status().Waiting == 1 })
			m.mu.Lock()
			if experimental {
				m.o.ExperimentalRedaction = false
			} else {
				c := m.capabilities[Redaction]
				c.State = "unknown"
				m.capabilities[Redaction] = c
			}
			m.mu.Unlock()
			close(release)
			if err := <-active; err != nil {
				t.Fatal(err)
			}
			if err := <-queued; !errors.Is(err, ErrCapability) || requests.Load() != 0 {
				t.Fatal("revoked admission reached HTTP after waiting", err)
			}
		})
	}
}
