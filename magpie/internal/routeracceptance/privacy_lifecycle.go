package routeracceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

type privacyLifecycleEvidence struct {
	PIDBefore                  int                              `json:"pidBefore"`
	Cancel                     privacyLifecycleCancelEvidence   `json:"cancel"`
	Recovery                   privacyLifecycleRecoveryEvidence `json:"recovery"`
	Timeout                    privacyLifecycleTimeoutEvidence  `json:"timeout"`
	TimeoutRecovery            privacyLifecycleRecoveryEvidence `json:"timeoutRecovery"`
	RedactionCapabilityUnknown bool                             `json:"redactionCapabilityUnknown"`
	DurationMS                 int64                            `json:"durationMs"`
	ErrorCode                  string                           `json:"errorCode,omitempty"`
}

type privacyLifecycleSlotEvidence struct {
	PIDAfter     int  `json:"pidAfter"`
	SamePID      bool `json:"samePid"`
	ActiveAfter  int  `json:"activeAfter"`
	WaitingAfter int  `json:"waitingAfter"`
	SlotReleased bool `json:"slotReleased"`
}

type privacyLifecycleCancelEvidence struct {
	privacyLifecycleSlotEvidence
	Timing           routerruntime.GenerationDiagnosticSnapshot `json:"timing"`
	HTTPStatus       int                                        `json:"httpStatus"`
	Events           int                                        `json:"events"`
	ContentObserved  bool                                       `json:"contentObserved"`
	FirstContentMS   int64                                      `json:"firstContentMs"`
	CancelIssued     bool                                       `json:"cancelIssued"`
	ReturnedCanceled bool                                       `json:"returnedCanceled"`
	DurationMS       int64                                      `json:"durationMs"`
	ErrorCode        string                                     `json:"errorCode,omitempty"`
}

type privacyLifecycleRecoveryEvidence struct {
	privacyLifecycleSlotEvidence
	Timing       routerruntime.GenerationDiagnosticSnapshot `json:"timing"`
	HTTPStatus   int                                        `json:"httpStatus"`
	FinishReason string                                     `json:"finishReason,omitempty"`
	Usage        map[string]int                             `json:"usage,omitempty"`
	Parsed       bool                                       `json:"parsed"`
	ParseCode    string                                     `json:"parseCode,omitempty"`
	Entities     int                                        `json:"entities"`
	DurationMS   int64                                      `json:"durationMs"`
	ErrorCode    string                                     `json:"errorCode,omitempty"`
}

type privacyLifecycleTimeoutEvidence struct {
	privacyLifecycleSlotEvidence
	Timing           routerruntime.GenerationDiagnosticSnapshot `json:"timing"`
	BudgetMS         int64                                      `json:"budgetMs"`
	DeadlineObserved bool                                       `json:"deadlineObserved"`
	DurationMS       int64                                      `json:"durationMs"`
	ErrorCode        string                                     `json:"errorCode,omitempty"`
}

// runPrivacyLifecycle is a fixed synthetic CLI diagnostic. It uses the one
// service-owned Manager and never accepts a capability or sends cloud traffic.
func runPrivacyLifecycle(ctx context.Context, m *routerruntime.Manager, spec *routerredact.PrivacySpecification, outputDir string) (privacyLifecycleEvidence, error) {
	if m == nil {
		return privacyLifecycleEvidence{ErrorCode: "runtime_unavailable"}, errors.New("runtime_unavailable")
	}
	return runPrivacyLifecycleWith(ctx, m.Generate, m.Status, spec, outputDir)
}

func runPrivacyLifecycleWith(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, outputDir string) (report privacyLifecycleEvidence, result error) {
	started := time.Now()
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		if status != nil {
			capability := status().Capabilities[routerruntime.Redaction]
			report.RedactionCapabilityUnknown = capability.State == "unknown" && !capability.Ready
		}
		if result != nil && report.ErrorCode == "" {
			report.ErrorCode = safeDiagnosticError(result)
		}
	}()
	fail := func(code string) error { report.ErrorCode = code; return errors.New(code) }
	if generate == nil || status == nil || spec == nil {
		return report, fail("privacy_lifecycle_unavailable")
	}
	initial := status()
	report.PIDBefore = initial.PID
	capabilityMatches := func() bool {
		s := status()
		capability := s.Capabilities[routerruntime.Redaction]
		return capability.State == "unknown" && !capability.Ready && capability.Fingerprint == spec.RuntimeFingerprint() && s.PID == report.PIDBefore && s.Healthy
	}
	if initial.PID <= 0 || initial.Active != 0 || initial.Waiting != 0 || !capabilityMatches() {
		return report, fail("privacy_lifecycle_runtime_state")
	}
	in := routerredact.Extraction{Versions: spec.Versions(), Texts: []string{"姓名：李芳"}}
	// Every diagnostic generation gets at most the existing 15-second request
	// budget. Recovery calls are distinct requests, never renewed batches.
	cancelCtx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	body, err := spec.Build(cancelCtx, in)
	if err != nil {
		return report, err
	}
	var streaming map[string]json.RawMessage
	if json.Unmarshal(body, &streaming) != nil {
		return report, fail("privacy_lifecycle_request_invalid")
	}
	streaming["stream"] = json.RawMessage("true")
	streaming["chat_template_kwargs"] = json.RawMessage(`{"enable_thinking":false}`)
	streamBody, err := json.Marshal(streaming)
	if err != nil {
		return report, fail("privacy_lifecycle_request_invalid")
	}

	var cancelTiming routerruntime.GenerationDiagnostics
	cancelStarted := time.Now()
	err = generate(routerruntime.WithGenerationDiagnostics(cancelCtx, &cancelTiming), routerruntime.Redaction, streamBody, func(response *http.Response) error {
		if response == nil || response.Body == nil {
			return errors.New("privacy_lifecycle_stream_invalid")
		}
		report.Cancel.HTTPStatus = response.StatusCode
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			return errors.New("privacy_lifecycle_stream_invalid")
		}
		writer, close, err := privacyLifecycleRawWriter(outputDir, "lifecycle-cancel.sse")
		if err != nil {
			return err
		}
		defer close()
		scanner := bufio.NewScanner(io.TeeReader(io.LimitReader(response.Body, (1<<20)+1), writer))
		scanner.Buffer(make([]byte, 4096), 128<<10)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			report.Cancel.Events++
			if data == "[DONE]" {
				return errors.New("privacy_lifecycle_stream_completed_before_cancel")
			}
			var chunk struct {
				Choices []struct {
					Index int `json:"index"`
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 {
				return errors.New("privacy_lifecycle_stream_invalid")
			}
			choice := chunk.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				return errors.New("privacy_lifecycle_stream_completed_before_cancel")
			}
			if choice.Delta.Content != "" {
				report.Cancel.ContentObserved = true
				report.Cancel.FirstContentMS = time.Since(cancelStarted).Milliseconds()
				if cancelCtx.Err() != nil {
					return cancelCtx.Err()
				}
				report.Cancel.CancelIssued = true
				cancel()
				return cancelCtx.Err()
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return errors.New("privacy_lifecycle_no_stream_content")
	})
	report.Cancel.Timing = cancelTiming.Snapshot()
	if report.Cancel.HTTPStatus == 0 {
		report.Cancel.HTTPStatus = report.Cancel.Timing.HTTPStatus
	}
	report.Cancel.DurationMS = time.Since(cancelStarted).Milliseconds()
	report.Cancel.ReturnedCanceled = report.Cancel.CancelIssued && errors.Is(err, context.Canceled)
	report.Cancel.privacyLifecycleSlotEvidence = privacyLifecycleSlot(status(), report.PIDBefore)
	cancel()
	if !report.Cancel.ReturnedCanceled || !report.Cancel.ContentObserved || !report.Cancel.SlotReleased || !report.Cancel.SamePID || !capabilityMatches() {
		report.Cancel.ErrorCode = safeDiagnosticError(err)
		return report, fail("privacy_lifecycle_cancel_not_verified")
	}

	report.Recovery, err = privacyLifecycleRecover(parent, generate, status, spec, in, outputDir, "lifecycle-recovery.json", report.PIDBefore)
	if err != nil {
		return report, err
	}
	if !capabilityMatches() {
		return report, fail("privacy_lifecycle_runtime_state")
	}

	// This intentionally shorter parent deadline measures a timeout, then a
	// second immediate recovery on the same process. No token count is estimated.
	const timeoutBudget = 100 * time.Millisecond
	timeoutCtx, timeoutCancel := context.WithTimeout(parent, timeoutBudget)
	var timeoutTiming routerruntime.GenerationDiagnostics
	timeoutStarted := time.Now()
	err = generate(routerruntime.WithGenerationDiagnostics(timeoutCtx, &timeoutTiming), routerruntime.Redaction, body, func(response *http.Response) error {
		if response == nil || response.Body == nil {
			return errors.New("privacy_lifecycle_timeout_response_invalid")
		}
		writer, close, err := privacyLifecycleRawWriter(outputDir, "lifecycle-timeout.json")
		if err != nil {
			return err
		}
		defer close()
		_, err = io.Copy(writer, io.LimitReader(response.Body, (1<<20)+1))
		return err
	})
	report.Timeout.BudgetMS = timeoutBudget.Milliseconds()
	report.Timeout.DurationMS = time.Since(timeoutStarted).Milliseconds()
	report.Timeout.DeadlineObserved = errors.Is(err, context.DeadlineExceeded) && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded)
	report.Timeout.Timing = timeoutTiming.Snapshot()
	report.Timeout.privacyLifecycleSlotEvidence = privacyLifecycleSlot(status(), report.PIDBefore)
	timeoutCancel()
	if !report.Timeout.DeadlineObserved || !report.Timeout.SlotReleased || !report.Timeout.SamePID || !capabilityMatches() {
		report.Timeout.ErrorCode = safeDiagnosticError(err)
		return report, fail("privacy_lifecycle_timeout_not_verified")
	}
	report.TimeoutRecovery, err = privacyLifecycleRecover(parent, generate, status, spec, in, outputDir, "lifecycle-timeout-recovery.json", report.PIDBefore)
	if err != nil {
		return report, err
	}
	if !capabilityMatches() {
		return report, fail("privacy_lifecycle_runtime_state")
	}
	return report, nil
}

func privacyLifecycleSlot(s routerruntime.Status, pid int) privacyLifecycleSlotEvidence {
	return privacyLifecycleSlotEvidence{PIDAfter: s.PID, SamePID: pid > 0 && s.PID == pid, ActiveAfter: s.Active, WaitingAfter: s.Waiting, SlotReleased: s.Active == 0 && s.Waiting == 0}
}

func privacyLifecycleRawWriter(directory, name string) (io.Writer, func(), error) {
	if directory == "" {
		return io.Discard, func() {}, nil
	}
	directory = filepath.Join(directory, "synthetic-only")
	if os.MkdirAll(directory, 0o700) != nil {
		return nil, nil, errors.New("privacy_lifecycle_evidence_write_failed")
	}
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, errors.New("privacy_lifecycle_evidence_write_failed")
	}
	return file, func() { _ = file.Close() }, nil
}

func privacyLifecycleRecover(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, in routerredact.Extraction, directory, filename string, pid int) (report privacyLifecycleRecoveryEvidence, result error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var timing routerruntime.GenerationDiagnostics
	defer func() {
		report.Timing = timing.Snapshot()
		if report.HTTPStatus == 0 {
			report.HTTPStatus = report.Timing.HTTPStatus
		}
		report.DurationMS = time.Since(started).Milliseconds()
		report.privacyLifecycleSlotEvidence = privacyLifecycleSlot(status(), pid)
		if result != nil {
			report.ErrorCode = safeDiagnosticError(result)
		}
	}()
	consumed := 0
	body, err := spec.Build(ctx, in)
	if err != nil {
		return report, err
	}
	err = generate(routerruntime.WithGenerationDiagnostics(ctx, &timing), routerruntime.Redaction, body, func(response *http.Response) error {
		consumed++
		if consumed != 1 || response == nil || response.Body == nil {
			return errors.New("privacy_lifecycle_recovery_response_invalid")
		}
		report.HTTPStatus = response.StatusCode
		if response.StatusCode != http.StatusOK {
			return errors.New("privacy_lifecycle_recovery_response_invalid")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return errors.New("privacy_lifecycle_recovery_response_invalid")
		}
		writer, close, err := privacyLifecycleRawWriter(directory, filename)
		if err != nil {
			return err
		}
		defer close()
		if _, err := writer.Write(raw); err != nil {
			return errors.New("privacy_lifecycle_evidence_write_failed")
		}
		var metadata privacyCallEvidence
		privacyResponseMetadata(raw, &metadata)
		report.FinishReason, report.Usage = metadata.FinishReason, metadata.Usage
		parsed, err := spec.Parse(ctx, in, raw)
		report.ParseCode = routerredact.PrivacyParseDiagnosticCode(err)
		report.Parsed = err == nil && parsed.Complete
		if report.Parsed {
			report.Entities = len(parsed.Entities)
		}
		return err
	})
	if err != nil {
		return report, err
	}
	after := privacyLifecycleSlot(status(), pid)
	if consumed != 1 || !report.Parsed || report.FinishReason != "stop" || !after.SlotReleased || !after.SamePID {
		return report, errors.New("privacy_lifecycle_recovery_not_verified")
	}
	return report, nil
}
