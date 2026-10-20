package routeracceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

type businessPreacceptance struct {
	Requested      bool                            `json:"requested"`
	Started        time.Time                       `json:"started,omitempty"`
	Finished       time.Time                       `json:"finished,omitempty"`
	Judgment       businessJudgmentEvidence        `json:"judgment"`
	Execution      businessExecutionEvidence       `json:"execution"`
	Redaction      *routerredact.PrivacyEvaluation `json:"redaction,omitempty"`
	RedactionCases int                             `json:"redactionCases,omitempty"`
}

type businessJudgmentEvidence struct {
	State            string `json:"state"`
	Decision         string `json:"decision,omitempty"`
	ThinkingDisabled bool   `json:"thinkingDisabled"`
	BudgetMS         int64  `json:"budgetMs"`
	ResponseSHA256   string `json:"responseSha256,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
}

type businessExecutionEvidence struct {
	State            string `json:"state"`
	ToolCallObserved bool   `json:"toolCallObserved"`
	ArgumentsValid   bool   `json:"argumentsValid"`
	ToolResultRound  bool   `json:"toolResultRound"`
	FinishedNormally bool   `json:"finishedNormally"`
	BudgetMS         int64  `json:"budgetMs"`
	FirstResponseSHA string `json:"firstResponseSha256,omitempty"`
	FollowupSHA      string `json:"followupResponseSha256,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
}

func safeDiagnosticError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "budget_exhausted"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, routerruntime.ErrFault):
		return "runtime_fault"
	case errors.Is(err, routerruntime.ErrDisabled):
		return "runtime_disabled"
	case errors.Is(err, routerruntime.ErrNotInstalled):
		return "runtime_not_installed"
	default:
		return "diagnostic_failed"
	}
}

func hashDiagnostic(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readDiagnosticResponse(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("diagnostic_response_unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("diagnostic_response_invalid")
	}
	return data, nil
}

func parseBusinessText(data []byte) (string, string, []map[string]any, error) {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content   string           `json:"content"`
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Choices) == 0 {
		return "", "", nil, errors.New("diagnostic_response_invalid")
	}
	choice := envelope.Choices[0]
	return choice.Message.Content, choice.FinishReason, choice.Message.ToolCalls, nil
}

func runBusinessPreacceptance(ctx context.Context, manager *routerruntime.Manager, model routermanifest.Model, runtimeArtifact routermanifest.Runtime) (report businessPreacceptance, result error) {
	report = businessPreacceptance{Requested: true, Started: time.Now().UTC()}
	defer func() { report.Finished = time.Now().UTC() }()
	if manager == nil {
		return report, errors.New("runtime_unavailable")
	}

	judgmentStarted := time.Now()
	judgmentBody := map[string]any{
		"model": model.ID,
		"messages": []any{
			map[string]any{"role": "system", "content": `Return exactly JSON {"decision":"local"} or {"decision":"cloud"}.`},
			map[string]any{"role": "user", "content": "Classify this fixed routing sample for engineering pre-acceptance."},
		},
		"stream": false, "chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	judgmentBytes, _ := json.Marshal(judgmentBody)
	judgmentCtx, judgmentCancel := context.WithTimeout(ctx, 5*time.Second)
	var judgmentResponse []byte
	judgmentErr := manager.Generate(judgmentCtx, routerruntime.Judgment, judgmentBytes, func(response *http.Response) error {
		var err error
		judgmentResponse, err = readDiagnosticResponse(response)
		return err
	})
	judgmentCancel()
	report.Judgment.BudgetMS = time.Since(judgmentStarted).Milliseconds()
	report.Judgment.ThinkingDisabled = true
	report.Judgment.ResponseSHA256 = hashDiagnostic(judgmentResponse)
	if judgmentErr != nil {
		report.Judgment.State, report.Judgment.ErrorCode = "failed", safeDiagnosticError(judgmentErr)
	} else {
		text, _, _, parseErr := parseBusinessText(judgmentResponse)
		var decision struct {
			Decision string `json:"decision"`
		}
		if parseErr == nil {
			parseErr = json.Unmarshal([]byte(strings.TrimSpace(text)), &decision)
		}
		if parseErr == nil && (decision.Decision == "local" || decision.Decision == "cloud") {
			report.Judgment.State, report.Judgment.Decision = "observed", decision.Decision
		} else {
			report.Judgment.State, report.Judgment.ErrorCode = "failed", "invalid_decision_output"
		}
	}

	// Execution uses two separate Manager.Generate calls. The first must produce
	// a tool call; only after its slot is released is the tool result sent back.
	executionStarted := time.Now()
	firstBody := map[string]any{
		"model": model.ID, "stream": false, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup_case", "description": "Read the fixed case fixture.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"}}}}},
		"tool_choice": "required", "messages": []any{map[string]any{"role": "user", "content": "Call lookup_case with key alpha, then use its result."}},
	}
	firstBytes, _ := json.Marshal(firstBody)
	var firstResponse []byte
	firstErr := manager.Generate(ctx, routerruntime.Execution, firstBytes, func(response *http.Response) error {
		var err error
		firstResponse, err = readDiagnosticResponse(response)
		return err
	})
	report.Execution.BudgetMS = time.Since(executionStarted).Milliseconds()
	report.Execution.FirstResponseSHA = hashDiagnostic(firstResponse)
	if firstErr != nil {
		report.Execution.State, report.Execution.ErrorCode = "failed", safeDiagnosticError(firstErr)
	} else {
		_, _, calls, parseErr := parseBusinessText(firstResponse)
		if parseErr == nil && len(calls) > 0 {
			report.Execution.ToolCallObserved = true
			call := calls[0]
			if function, ok := call["function"].(map[string]any); ok {
				if args, ok := function["arguments"].(string); ok {
					var decoded any
					report.Execution.ArgumentsValid = json.Unmarshal([]byte(args), &decoded) == nil
				}
			}
			followupBody := map[string]any{
				"model": model.ID, "stream": false, "chat_template_kwargs": map[string]any{"enable_thinking": false},
				"messages": []any{
					map[string]any{"role": "user", "content": "Call lookup_case with key alpha, then use its result."},
					map[string]any{"role": "assistant", "content": nil, "tool_calls": calls},
					map[string]any{"role": "tool", "tool_call_id": call["id"], "content": `{"value":"BUSINESS_TOOL_OK"}`},
					map[string]any{"role": "user", "content": "Reply with exactly the tool value."},
				},
			}
			followupBytes, _ := json.Marshal(followupBody)
			var followupResponse []byte
			followupErr := manager.Generate(ctx, routerruntime.Execution, followupBytes, func(response *http.Response) error {
				var err error
				followupResponse, err = readDiagnosticResponse(response)
				return err
			})
			report.Execution.FollowupSHA = hashDiagnostic(followupResponse)
			if followupErr == nil {
				report.Execution.ToolResultRound = true
				text, followupFinish, _, err := parseBusinessText(followupResponse)
				report.Execution.FinishedNormally = err == nil && followupFinish == "stop" && strings.TrimSpace(text) != ""
			} else {
				report.Execution.ErrorCode = safeDiagnosticError(followupErr)
			}
		}
		if report.Execution.ToolCallObserved && report.Execution.ArgumentsValid && report.Execution.ToolResultRound && report.Execution.FinishedNormally {
			report.Execution.State = "observed"
		} else if report.Execution.ErrorCode == "" {
			report.Execution.State = "failed"
			report.Execution.ErrorCode = "tool_round_incomplete"
		}
	}

	spec, specErr := routerredact.NewPrivacySpecification(routerredact.ModelBinding{ModelID: model.ID, RuntimeID: runtimeArtifact.ID})
	if specErr != nil {
		return report, specErr
	}
	cases, casesErr := routerredact.PrivacyEvaluationCases(spec)
	if casesErr != nil {
		return report, casesErr
	}
	report.RedactionCases = len(cases)
	redactionCtx, redactionCancel := context.WithTimeout(ctx, 20*time.Minute)
	evaluation, evaluationErr := routerredact.EvaluatePrivacyDiagnostic(redactionCtx, manager, spec, cases)
	redactionCancel()
	report.Redaction = &evaluation
	return report, evaluationErr
}
