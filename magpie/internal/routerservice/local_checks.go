package routerservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
)

const (
	localModelID    = "spark-x2.5-1.7B"
	localTextAnswer = "ROUTER_EXECUTION_OK"
	localToolName   = "router_check"
)

// localCapabilityChecks are server-owned and are registered only for the
// current product build. They perform real loopback I/O inside Manager's
// already acquired slot; they never call Manager.Generate/Invoke recursively.
func localCapabilityChecks(identity routerruntime.LocalCallIdentity, promptVersion string) []routerruntime.Check {
	if identity.ModelID == "" || identity.ModelFingerprint == "" || identity.RuntimeFingerprint == "" {
		return nil
	}
	judgmentSuite := fmt.Sprintf("w4-judgment-contract-v1:%s:%s:%s", promptVersion, identity.ModelFingerprint, identity.RuntimeFingerprint)
	executionSuite := fmt.Sprintf("w4-execution-tool-contract-v1:%s:%s:%s", promptVersion, identity.ModelFingerprint, identity.RuntimeFingerprint)
	return []routerruntime.Check{
		{Purpose: routerruntime.Judgment, Suite: judgmentSuite, Trial: true, Run: func(ctx context.Context, endpoint string) (string, error) {
			return runJudgmentCapabilityCheck(ctx, endpoint, identity.ModelID, judgmentSuite)
		}},
		{Purpose: routerruntime.Execution, Suite: executionSuite, Trial: true, Run: func(ctx context.Context, endpoint string) (string, error) {
			return runExecutionCapabilityCheck(ctx, endpoint, identity.ModelID, executionSuite)
		}},
	}
}

type localCheckCallObserverKey struct{}

type localCheckCallObserver struct {
	target   routerruntime.LocalCallObserver
	manager  *routerruntime.Manager
	purpose  routerruntime.Purpose
	suite    string
	prompt   string
	material string
}

func withLocalCheckCallObserver(ctx context.Context, value localCheckCallObserver) context.Context {
	return context.WithValue(ctx, localCheckCallObserverKey{}, value)
}

func localCheckCallObserverFrom(ctx context.Context) (localCheckCallObserver, bool) {
	value, ok := ctx.Value(localCheckCallObserverKey{}).(localCheckCallObserver)
	return value, ok && value.target != nil && value.manager != nil
}

func localCheckRequest(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	started := time.Now()
	attempted := false
	var responseBody []byte
	var resultErr error
	defer func() {
		meta, ok := localCheckCallObserverFrom(ctx)
		if !ok {
			return
		}
		identity := meta.manager.Identity()
		settings := meta.manager.Settings()
		ended := time.Now().UTC()
		event := routerruntime.LocalCallObservation{
			Settings: settings, SettingsFingerprint: settings.Fingerprint(),
			CallID: nextLocalCallID(), Check: true, Purpose: routerruntime.LocalCallPurpose(meta.purpose), Mode: routerruntime.LocalCallDiagnostic,
			ModelID: identity.ModelID, ModelFingerprint: identity.ModelFingerprint, RuntimeID: identity.RuntimeID, RuntimeFingerprint: identity.RuntimeFingerprint,
			MaterialVersion: meta.material, PromptVersion: meta.prompt, Suite: meta.suite,
			StartedAt: started.UTC(), EndedAt: ended, Outcome: localOutcome(resultErr), ErrorCode: localErrorCode(resultErr), Attempted: attempted,
			Usage: reportedUsageFromBody(responseBody), TotalMS: phase(float64(ended.Sub(started)) / float64(time.Millisecond)),
		}
		meta.target.ObserveLocalCall(event)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		resultErr = errors.New("local_check_request_invalid")
		return nil, resultErr
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := routerprocess.NewHTTPClient()
	defer client.CloseIdleConnections()
	attempted = true
	response, err := client.Do(request)
	if err != nil {
		resultErr = errors.New("local_check_transport_failed")
		return nil, resultErr
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		resultErr = errors.New("local_check_response_unreadable")
		return nil, resultErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		resultErr = errors.New("local_check_http_failed")
		return nil, resultErr
	}
	return responseBody, nil
}

func checkEvidence(label string, values ...[]byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, label)
	for _, value := range values {
		_, _ = h.Write(value)
	}
	return fmt.Sprintf("%s:%s", label, hex.EncodeToString(h.Sum(nil)))
}

func runJudgmentCapabilityCheck(ctx context.Context, endpoint, modelID, suite string) (string, error) {
	// Both valid decisions are exercised. The third fixture proves malformed or
	// unsupported output is rejected by the same strict parser used in routing.
	probes := []struct {
		label    string
		expected Decision
	}{
		{label: "local", expected: DecisionLocal},
		{label: "cloud", expected: DecisionCloud},
	}
	var responses [][]byte
	for _, probe := range probes {
		payload, _ := json.Marshal(map[string]any{
			"model": modelID,
			"messages": []map[string]string{
				{"role": "system", "content": "Return exactly JSON with one key decision and no explanation. The accepted values are local and cloud."},
				{"role": "user", "content": "This capability case is explicitly labeled " + probe.label + ". Return {\"decision\":\"" + probe.label + "\"}."},
			},
			"stream":               false,
			"max_tokens":           64,
			"temperature":          0,
			"chat_template_kwargs": map[string]bool{"enable_thinking": false},
			"cache_prompt":         true,
		})
		body, err := localCheckRequest(ctx, endpoint, payload)
		if err != nil {
			return "", err
		}
		decision, err := parseDecision(body)
		if err != nil || decision != probe.expected {
			return "", errors.New("local_check_judgment_contract_failed")
		}
		responses = append(responses, body)
	}
	if _, err := parseDecision([]byte(`{"decision":"unsupported"}`)); !errors.Is(err, ErrInvalidDecision) {
		return "", errors.New("local_check_invalid_decision_not_rejected")
	}
	if _, err := parseDecision([]byte(`{"decision":}`)); !errors.Is(err, ErrInvalidDecision) {
		return "", errors.New("local_check_malformed_decision_not_rejected")
	}
	return checkEvidence(suite, responses...), nil
}

func runExecutionCapabilityCheck(ctx context.Context, endpoint, modelID, suite string) (string, error) {
	textPayload, _ := json.Marshal(map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "system", "content": "Follow the user's exact instruction and return only the requested token."},
			{"role": "user", "content": "Reply with exactly " + localTextAnswer + "."},
		},
		"stream":               false,
		"max_tokens":           64,
		"temperature":          0,
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
		"cache_prompt":         true,
	})
	textBody, err := localCheckRequest(ctx, endpoint, textPayload)
	if err != nil {
		return "", err
	}
	textReply, err := parseLocalReply(textBody)
	if err != nil || strings.TrimSpace(textReply.Text) != localTextAnswer || len(textReply.ToolCalls) != 0 {
		return "", errors.New("local_check_execution_text_contract_failed")
	}

	toolPayload, _ := json.Marshal(map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{"role": "system", "content": "Use the provided function when requested. Do not answer before using it."},
			{"role": "user", "content": "Call router_check with path README.md."},
		},
		"tools": []map[string]any{{"type": "function", "function": map[string]any{
			"name": localToolName, "description": "Read a fixture path", "parameters": map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}, "required": []string{"path"},
			},
		}}},
		"tool_choice":          map[string]any{"type": "function", "function": map[string]string{"name": localToolName}},
		"stream":               false,
		"max_tokens":           128,
		"temperature":          0,
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
		"cache_prompt":         true,
	})
	toolBody, err := localCheckRequest(ctx, endpoint, toolPayload)
	if err != nil {
		return "", err
	}
	toolReply, err := parseLocalReply(toolBody)
	if err != nil || len(toolReply.ToolCalls) != 1 {
		return "", errors.New("local_check_execution_tool_contract_failed")
	}
	call := toolReply.ToolCalls[0]
	if call.ID == "" || call.Name != localToolName || !jsonObject([]byte(call.Arguments)) {
		return "", errors.New("local_check_execution_tool_association_failed")
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil || args.Path != "README.md" {
		return "", errors.New("local_check_execution_tool_arguments_failed")
	}

	var assistant map[string]any
	if json.Unmarshal(toolBody, &assistant) != nil {
		return "", errors.New("local_check_execution_tool_response_invalid")
	}
	choices, _ := assistant["choices"].([]any)
	if len(choices) != 1 {
		return "", errors.New("local_check_execution_tool_response_invalid")
	}
	followupMessages := []any{
		map[string]any{"role": "system", "content": "After the tool result, reply with exactly " + localTextAnswer + "."},
		map[string]any{"role": "user", "content": "Call router_check with path README.md."},
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}}}},
		map[string]any{"role": "tool", "tool_call_id": call.ID, "content": "fixture contents"},
	}
	followupPayload, _ := json.Marshal(map[string]any{"model": modelID, "messages": followupMessages, "stream": false, "max_tokens": 64, "temperature": 0, "chat_template_kwargs": map[string]bool{"enable_thinking": false}, "cache_prompt": true})
	followupBody, err := localCheckRequest(ctx, endpoint, followupPayload)
	if err != nil {
		return "", err
	}
	followup, err := parseLocalReply(followupBody)
	if err != nil || strings.TrimSpace(followup.Text) != localTextAnswer || len(followup.ToolCalls) != 0 {
		return "", errors.New("local_check_execution_tool_result_contract_failed")
	}
	return checkEvidence(suite, textBody, toolBody, followupBody), nil
}
