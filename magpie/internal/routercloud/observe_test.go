package routercloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAttemptObservationIncludesFailureRetryAndCompletion(t *testing.T) {
	var events []Event
	calls := 0
	e := Executor{Observe: func(event Event) { events = append(events, event) }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			// This fixture must perform measurable work: Windows clock ticks can
			// otherwise make an entirely in-memory retry legitimately take 0 ns.
			// Keep the positive-duration assertion below meaningful on Windows.
			time.Sleep(20 * time.Millisecond)
			r := response(503, `{"error":"private-server-message"}`)
			r.Header.Set("Retry-After", "0")
			return r, nil
		}
		return response(200, whole("chat", true)), nil
	})}
	out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), configured("http://private-host.test/v1", "chat")})
	if err != nil || out.Attempts != 2 || len(out.AttemptDetails) != 2 || out.Operation != "generate" || out.Duration <= 0 {
		t.Fatalf("incomplete observation: %+v err=%v", out, err)
	}
	first, second := out.AttemptDetails[0], out.AttemptDetails[1]
	if first.Number != 1 || first.StartedAt.IsZero() || first.Duration < 0 || first.HTTPStatus != 503 || first.ErrorCode != "cloud_upstream_error" || first.OutputCommitted || !first.Retried || first.RetryDelay != 0 || first.RetryWait < 0 {
		t.Fatalf("first attempt: %+v", first)
	}
	if second.Number != 2 || second.HTTPStatus != 200 || second.ErrorCode != "" || !second.OutputCommitted || second.Retried {
		t.Fatalf("second attempt: %+v", second)
	}
	want := []string{"request_started", "attempt_started", "attempt_finished", "retry_wait_started", "retry_wait_finished", "attempt_started", "attempt_finished", "request_finished"}
	if len(events) != len(want) {
		t.Fatalf("events=%+v", events)
	}
	for i, event := range events {
		if event.Kind != want[i] || event.Operation != "generate" || event.At.IsZero() {
			t.Fatalf("event%d=%+v", i, event)
		}
	}
	if events[2].HTTPStatus != 503 || events[2].ErrorCode != "cloud_upstream_error" || events[7].Duration != out.Duration {
		t.Fatalf("events=%+v", events)
	}
	b, _ := json.Marshal([]any{out, events})
	for _, secret := range []string{"private-host", "private-server-message", "fake-cloud-key", "hello"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("observation leaked %s", secret)
		}
	}
}

func TestAttemptObservationCancellationAndPreflightFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []Event
	e := Executor{Observe: func(event Event) {
		events = append(events, event)
		if event.Kind == "retry_wait_started" {
			cancel()
		}
	}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(503, ""), nil })}
	out, err := e.Execute(ctx, httptest.NewRecorder(), Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
	if safeErrorCode(err) != "request_canceled" || out.Attempts != 1 || len(out.AttemptDetails) != 1 || out.AttemptDetails[0].Retried || out.AttemptDetails[0].RetryDelay != time.Second {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if events[len(events)-2].Kind != "retry_wait_finished" || events[len(events)-2].ErrorCode != "request_canceled" || events[len(events)-1].ErrorCode != "request_canceled" {
		t.Fatalf("events=%+v", events)
	}
	events = nil
	_, err = e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", []byte(`{"messages":[]}`), configured("http://upstream.test/v1", "chat")})
	if err == nil || len(events) != 2 || events[1].ErrorCode != "unsupported_input" {
		t.Fatalf("preflight not observed: %+v err=%v", events, err)
	}
}

func TestAttemptObservationRecordsCommittedResponseFailure(t *testing.T) {
	e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		r := response(200, "")
		r.Header.Set("Content-Type", "text/event-stream")
		r.Body = io.NopCloser(&failingReader{prefix: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"})
		return r, nil
	})}
	out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", true), configured("http://upstream.test/v1", "chat")})
	if err == nil || len(out.AttemptDetails) != 1 || !out.OutputCommitted || !out.AttemptDetails[0].OutputCommitted || out.AttemptDetails[0].ErrorCode != "cloud_response_error" || out.AttemptDetails[0].Retried {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestDynamicAuthorizationReplacesStaticAuthOnEveryAttempt(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			target := configured("http://upstream.test/v1", protocol)
			target.Authorization = "Bearer dynamic-only-for-this-app"
			target.Provider.Headers = map[string]string{"authorization": "Bearer static-header-must-not-leak", "X-API-Key": "static-x-api-must-not-leak"}
			calls := 0
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != target.Authorization || r.Header.Get("x-api-key") != "" {
					t.Fatalf("wrong auth: dynamic=%t extra=%t", r.Header.Get("Authorization") == target.Authorization, r.Header.Get("x-api-key") != "")
				}
				if calls == 1 {
					rsp := response(429, "")
					rsp.Header.Set("Retry-After", "0")
					return rsp, nil
				}
				return response(200, whole(protocol, true)), nil
			})}
			out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{protocol, input(protocol, false), target})
			if err != nil || out.Attempts != 2 {
				t.Fatalf("%+v %v", out, err)
			}
			if target.Provider.Headers["authorization"] != "Bearer static-header-must-not-leak" {
				t.Fatal("saved options mutated")
			}
			encoded, _ := json.Marshal(target)
			if strings.Contains(string(encoded), "dynamic-only-for-this-app") {
				t.Fatal("dynamic authorization JSON exposed")
			}
		})
	}
}

func TestDynamicAuthorizationInvalidValuesFailBeforeDispatch(t *testing.T) {
	for _, auth := range []string{" ", "Bearer x\r\nother: injected", "Bearer x\x00", strings.Repeat("x", 16385)} {
		target := configured("http://upstream.test/v1", "chat")
		target.Authorization = auth
		calls := 0
		e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dispatch") })}
		out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), target})
		if err == nil || out.Attempts != 0 || calls != 0 {
			t.Fatalf("invalid auth dispatched: %+v %v", out, err)
		}
	}
}

func TestExecutionBudgetStartsBeforePreparationObservation(t *testing.T) {
	for _, count := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "count_tokens"}[count], func(t *testing.T) {
			var started time.Time
			e := Executor{Timeout: time.Hour, Observe: func(event Event) {
				if event.Kind == "request_started" {
					started = event.At
				}
			}, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok || started.IsZero() || deadline.After(started.Add(time.Hour)) {
					t.Fatal("preparation received a fresh timeout budget")
				}
				if count {
					return response(200, `{"input_tokens":0}`), nil
				}
				return response(200, whole("anthropic", true)), nil
			})}
			in := Request{"anthropic", input("anthropic", false), configured("http://upstream.test/v1", "anthropic")}
			var err error
			if count {
				in.Body = countInput()
				_, err = e.CountTokens(context.Background(), httptest.NewRecorder(), in)
			} else {
				_, err = e.Execute(context.Background(), httptest.NewRecorder(), in)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
