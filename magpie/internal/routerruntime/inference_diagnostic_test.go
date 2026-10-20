package routerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerationHTTPErrorKeepsStatusAndRedactsOrdinaryOutput(t *testing.T) {
	const secret = "synthetic-private-error-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, strings.Repeat(secret, 200))
	}))
	defer server.Close()
	m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) {
		f.endpoint = server.URL
	})
	for _, diagnostic := range []bool{false, true} {
		t.Run(fmt.Sprintf("diagnostic=%t", diagnostic), func(t *testing.T) {
			ctx := context.Background()
			var trace GenerationDiagnostics
			if diagnostic {
				ctx = WithGenerationDiagnostics(ctx, &trace)
			}
			consumed := false
			err := m.Generate(ctx, Redaction, []byte(`{"messages":[]}`), func(*http.Response) error {
				consumed = true
				return nil
			})
			var statusErr *GenerationHTTPError
			if !errors.As(err, &statusErr) || statusErr.HTTPStatus != http.StatusBadRequest || consumed {
				t.Fatalf("HTTP failure lost its status or reached consume: err=%v consumed=%t", err, consumed)
			}
			for _, value := range []any{err, *statusErr, fmt.Errorf("outer: %w", err)} {
				for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
					if output := fmt.Sprintf(verb, value); strings.Contains(output, secret) {
						t.Fatalf("ordinary %s formatting exposed the synthetic response body", verb)
					}
				}
				data, marshalErr := json.Marshal(value)
				if marshalErr != nil || strings.Contains(string(data), secret) {
					t.Fatalf("ordinary JSON serialization exposed the synthetic response body: %v", marshalErr)
				}
			}
			body := statusErr.DiagnosticBody()
			if diagnostic {
				if len(body) != 4096 || !strings.HasPrefix(string(body), secret) {
					t.Fatal("trusted diagnostic did not retain the bounded actual error body")
				}
				body[0] = 'X'
				if statusErr.DiagnosticBody()[0] == 'X' {
					t.Fatal("diagnostic body aliases the error's retained bytes")
				}
				measured := trace.Snapshot()
				if measured.HTTPStatus != http.StatusBadRequest || !diagnosticPhasesFitTotal(measured) {
					t.Fatalf("HTTP failure did not publish actual phase measurements: %+v", measured)
				}
			} else if len(body) != 0 {
				t.Fatal("ordinary generation retained a raw diagnostic body")
			}
		})
	}
}

func TestGenerationDiagnosticsMeasureQueueHeadersAndConsumeWithoutChangingAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	headersEntered := make(chan struct{})
	headersRelease := make(chan struct{})
	consumeEntered := make(chan struct{})
	consumeRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body["cache_prompt"]) != "true" {
			t.Error("diagnostic changed the production request contract")
		}
		close(headersEntered)
		select {
		case <-headersRelease:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer func() {
		cancel()
		server.Close()
	}()
	m, _, _, _ := managerFixture(t, func(_ *Options, _ *fixtureInstaller, f *fixtureFactory) {
		f.endpoint = server.URL
	})
	occupied := make(chan struct{})
	occupancyRelease := make(chan struct{})
	first := use(m, ctx, Redaction, func(call context.Context, _ string) error {
		close(occupied)
		select {
		case <-occupancyRelease:
			return nil
		case <-call.Done():
			return call.Err()
		}
	})
	awaitDiagnosticState(t, occupied)
	var trace GenerationDiagnostics
	generated := make(chan error, 1)
	go func() {
		generated <- m.Generate(WithGenerationDiagnostics(ctx, &trace), Redaction, []byte(`{"messages":[]}`), func(response *http.Response) error {
			if _, err := io.ReadAll(response.Body); err != nil {
				return err
			}
			close(consumeEntered)
			select {
			case <-consumeRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	eventually(t, func() bool { return m.Status().Waiting == 1 })
	queueObserved := time.Now()
	minimumQueueMS := elapsedMS(queueObserved)
	close(occupancyRelease)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	awaitDiagnosticState(t, headersEntered)
	headersObserved := time.Now()
	minimumHeadersMS := elapsedMS(headersObserved)
	close(headersRelease)
	awaitDiagnosticState(t, consumeEntered)
	if m.Status().Active != 1 {
		t.Fatal("diagnostic response consumption released the only execution slot")
	}
	consumeObserved := time.Now()
	minimumConsumeMS := elapsedMS(consumeObserved)
	close(consumeRelease)
	if err := <-generated; err != nil {
		t.Fatal(err)
	}
	measured := trace.Snapshot()
	if measured.HTTPStatus != http.StatusOK || measured.QueueLoadMS < minimumQueueMS || measured.RequestHeadersMS < minimumHeadersMS || measured.ConsumeMS < minimumConsumeMS || !diagnosticPhasesFitTotal(measured) {
		t.Fatalf("diagnostic phase measurements did not cover held production phases: %+v", measured)
	}
	if err := m.Invoke(context.Background(), Redaction, []byte(`{"messages":[]}`), func(*http.Response) error { return nil }); !errors.Is(err, ErrCapability) {
		t.Fatalf("diagnostic generation changed production capability admission: %v", err)
	}
}

func TestGenerationDiagnosticsReportCanceledQueueWithoutDispatch(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var trace GenerationDiagnostics
	err := m.Generate(WithGenerationDiagnostics(ctx, &trace), Redaction, []byte(`{"messages":[]}`), func(*http.Response) error {
		t.Fatal("canceled diagnostic reached HTTP consumption")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled diagnostic lost cancellation: %v", err)
	}
	measured := trace.Snapshot()
	if measured.HTTPStatus != 0 || measured.RequestHeadersMS != 0 || measured.ConsumeMS != 0 || !diagnosticPhasesFitTotal(measured) {
		t.Fatalf("canceled queue fabricated an HTTP phase: %+v", measured)
	}
}

func diagnosticPhasesFitTotal(measured GenerationDiagnosticSnapshot) bool {
	// Zero is valid for a phase below the native clock's resolution. Allow a
	// nanosecond when adding floating-point milliseconds from measured phases.
	return measured.QueueLoadMS >= 0 && measured.RequestHeadersMS >= 0 && measured.ConsumeMS >= 0 && measured.TotalMS+0.000001 >= measured.QueueLoadMS+measured.RequestHeadersMS+measured.ConsumeMS
}

func awaitDiagnosticState(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("diagnostic phase did not reach its controlled state")
	}
}
