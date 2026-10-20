package routerruntime

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const experimentalRedactionBudget = 30 * time.Second
const experimentalRedactionSuitePrefix = "privacy-trial-v1:"

// ExperimentalRedactionInvoker permits an explicitly selected 1.7B or 4B trial while
// preserving the Manager's actual capability samples. Its binding is immutable;
// it is created only by a Manager whose trusted startup options permit trials.
type ExperimentalRedactionInvoker struct {
	manager     *Manager
	fingerprint string
	suite       string
}

func validExperimentalRedactionSuite(suite string) bool {
	if !strings.HasPrefix(suite, experimentalRedactionSuitePrefix) || len(suite) != len(experimentalRedactionSuitePrefix)+64 {
		return false
	}
	for _, c := range suite[len(experimentalRedactionSuitePrefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewExperimentalRedactionInvoker does not accept a capability or start work.
// Only the complete versioned trial suite and current selected fingerprint may
// be bound. Public management requests cannot construct or configure this handle.
func (m *Manager) NewExperimentalRedactionInvoker(fingerprint, suite string) (*ExperimentalRedactionInvoker, error) {
	if m == nil || fingerprint == "" || !validExperimentalRedactionSuite(suite) {
		return nil, ErrCapability
	}
	invoker := &ExperimentalRedactionInvoker{manager: m, fingerprint: fingerprint, suite: suite}
	if err := invoker.Require(Redaction); err != nil {
		return nil, err
	}
	return invoker, nil
}

func (e *ExperimentalRedactionInvoker) Require(p Purpose) error {
	if e == nil || e.manager == nil || p != Redaction {
		return ErrCapability
	}
	m := e.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.o.ExperimentalRedaction {
		return ErrCapability
	}
	if !m.intent.Enabled || m.closed || m.stopping {
		return ErrDisabled
	}
	if (m.intent.Selected != "spark-x2.5-1.7B" && m.intent.Selected != "spark-x2.5-4B") || e.fingerprint == "" || e.fingerprint != m.fingerprintLocked() || !validExperimentalRedactionSuite(e.suite) {
		return ErrCapability
	}
	return nil
}

// VerifyBinding is checked by the explicit experimental extractor on every
// request. An ordinary or changed prompt/config suite cannot use this handle.
func (e *ExperimentalRedactionInvoker) VerifyBinding(fingerprint, suite string) error {
	if e == nil || fingerprint != e.fingerprint || suite != e.suite {
		return ErrCapability
	}
	return e.Require(Redaction)
}

func (e *ExperimentalRedactionInvoker) Status() Status {
	if e == nil || e.manager == nil {
		return Status{}
	}
	return e.manager.Status()
}

// Invoke shares the Manager's existing queue, process and transport. The bound
// admission is checked again after obtaining the execution slot. A single parent
// deadline also bounds cold loading; an extractor's earlier multi-batch deadline
// remains in effect. Returning from consume still cancels and releases the slot.
func (e *ExperimentalRedactionInvoker) Invoke(ctx context.Context, p Purpose, body []byte, consume func(*http.Response) error) error {
	if err := e.Require(p); err != nil {
		return err
	}
	budget := e.manager.o.RedactionBudget
	if budget <= 0 || budget > experimentalRedactionBudget {
		budget = experimentalRedactionBudget
	}
	call, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return e.manager.generate(call, p, body, consume, func() error { return e.Require(p) })
}
