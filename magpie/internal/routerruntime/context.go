package routerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/routerprocess"
)

const JudgmentInputLimit = 2048

// ContextUsage contains counts from the selected local runtime tokenizer. Zero
// input is unknown until TokenCount/Generate successfully publishes a count.
type ContextUsage struct {
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
	ContextTokens int `json:"contextTokens"`
}

type contextUsageKey struct{}

// WithContextUsage publishes one completed call's precise capacity check. Use a
// fresh value per call and read it only after the call returns.
func WithContextUsage(ctx context.Context, usage *ContextUsage) context.Context {
	return context.WithValue(ctx, contextUsageKey{}, usage)
}

func publishContextUsage(ctx context.Context, usage ContextUsage) {
	if target, _ := ctx.Value(contextUsageKey{}).(*ContextUsage); target != nil {
		*target = usage
	}
}

func requestOutputReserve(request map[string]json.RawMessage) (int, error) {
	// b10828 server-schema gives the native name priority, then these aliases;
	// null values are absent for every name.
	for _, name := range []string{"n_predict", "max_completion_tokens", "max_tokens"} {
		if raw, ok := request[name]; ok && string(raw) != "null" {
			var limit int
			if err := json.Unmarshal(raw, &limit); err != nil || limit <= 0 {
				return 0, ErrContextExceeded
			}
			return limit, nil
		}
	}
	return 0, nil
}

// TokenCount holds the same single scheduler slot as generation. It must not be
// called recursively from a Use callback. It never truncates the request.
func (m *Manager) TokenCount(ctx context.Context, body []byte) (usage ContextUsage, err error) {
	if len(body) > 16<<20 {
		return usage, ErrTokenCountUnavailable
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return usage, ErrTokenCountUnavailable
	}
	output, err := requestOutputReserve(request)
	if err != nil {
		return usage, err
	}
	err = m.Use(ctx, Execution, func(call context.Context, endpoint string) error {
		var e error
		usage, e = m.countContext(call, endpoint, body, output)
		return e
	})
	if err == nil {
		publishContextUsage(ctx, usage)
	}
	return usage, err
}

func (m *Manager) CheckContext(ctx context.Context, body []byte) (ContextUsage, error) {
	usage, err := m.TokenCount(ctx, body)
	if err == nil {
		err = checkContextUsage(usage)
	}
	if err == nil {
		var request map[string]json.RawMessage
		_ = json.Unmarshal(body, &request)
		output, _ := requestOutputReserve(request)
		if output == 0 && usage.OutputTokens < 32 {
			err = ErrContextExceeded
		}
	}
	return usage, err
}

func checkContextUsage(usage ContextUsage) error {
	// llama-server requires at least one position beyond the prompt. Reserve all
	// requested output so context shifting cannot silently discard input.
	if usage.ContextTokens <= 0 || usage.InputTokens < 0 || usage.InputTokens >= usage.ContextTokens || usage.OutputTokens <= 0 || usage.OutputTokens > usage.ContextTokens-usage.InputTokens {
		return ErrContextExceeded
	}
	return nil
}

func (m *Manager) countContext(ctx context.Context, endpoint string, body []byte, output int) (ContextUsage, error) {
	usage := ContextUsage{OutputTokens: output, ContextTokens: m.EffectiveSettings().ContextTokens}
	// b10828 uses the same oaicompat_chat_params_parse and tokenizer as chat
	// generation here, including messages, tools, template kwargs and BOS.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/chat/completions/input_tokens", bytes.NewReader(body))
	if err != nil {
		return usage, ErrTokenCountUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	client := routerprocess.NewHTTPClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return usage, ctx.Err()
		}
		return usage, ErrTokenCountUnavailable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return usage, ErrTokenCountUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return usage, errors.Join(ErrTokenCountUnavailable, newGenerationHTTPError(response.StatusCode, data, false))
	}
	var result struct {
		InputTokens *int `json:"input_tokens"`
	}
	if json.Unmarshal(data, &result) != nil || result.InputTokens == nil || *result.InputTokens < 0 {
		return usage, ErrTokenCountUnavailable
	}
	usage.InputTokens = *result.InputTokens
	if output == 0 {
		// A rejected prompt may occupy or exceed the complete window. Keep
		// that measured capacity snapshot valid for observation and persistence.
		usage.OutputTokens = max(0, usage.ContextTokens-usage.InputTokens)
	}
	return usage, nil
}

func newGenerationHTTPError(status int, data []byte, diagnostic bool) *GenerationHTTPError {
	e := &GenerationHTTPError{HTTPStatus: status}
	var response struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if status == http.StatusBadRequest && json.Unmarshal(data, &response) == nil && response.Error.Type == "exceed_context_size_error" {
		e.contextExceeded = true
	}
	if diagnostic {
		e.body = bytes.Clone(data)
	}
	return e
}
