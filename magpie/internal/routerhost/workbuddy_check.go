package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type workBuddyHTTPChecker struct{ managementToken string }

// ProductionWorkBuddyChecker checks the stored WorkBuddy application binding
// and the Chat Completions entry used by WorkBuddy 5.3.8 custom models. The
// management token is sent only to GET /api/apps, never to a model endpoint.
func ProductionWorkBuddyChecker(managementToken string) Checker {
	return workBuddyHTTPChecker{managementToken: managementToken}
}

func (c workBuddyHTTPChecker) Check(ctx context.Context, o Options) (result error) {
	defer func() {
		if result != nil {
			result = safeCheckError{result}
		}
	}()
	if o.Host != "workbuddy" {
		return fmt.Errorf("WorkBuddy checker requires host workbuddy")
	}
	if err := validateOptions(o); err != nil {
		return err
	}
	if strings.TrimSpace(c.managementToken) == "" || strings.ContainsAny(c.managementToken, "\r\n") {
		return fmt.Errorf("application validation unavailable: set --management-token-env to a current management token variable; it is used only to read /api/apps")
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	request := func(method, path string, body []byte, management bool) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(o.RouterURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("cannot construct local check")
		}
		if management {
			req.Header.Set("Authorization", "Bearer "+c.managementToken)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("local endpoint unavailable; start Router and retry")
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return nil, fmt.Errorf("invalid or oversized local check response")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("local check returned HTTP %d; inspect Router app/provider status", resp.StatusCode)
		}
		return data, nil
	}
	health, err := request(http.MethodGet, "/healthz", nil, false)
	if err != nil {
		return fmt.Errorf("Router health: %w", err)
	}
	var h struct{ Service, Status string }
	if !workBuddyDecodeJSON(health, &h) || h.Service != "api-router" || h.Status != "ok" {
		return fmt.Errorf("Router health: endpoint is not a healthy API-router service")
	}
	apps, err := request(http.MethodGet, "/api/apps", nil, true)
	if err != nil {
		return fmt.Errorf("application validation: %w", err)
	}
	var projection json.RawMessage
	if !workBuddyDecodeJSON(apps, &projection) {
		return fmt.Errorf("application validation: invalid or ambiguous /api/apps response")
	}
	if err := validateAppProjection(apps, o); err != nil {
		return err
	}
	// This is the installed custom-model URL builder's business path, not a
	// health or discovery request. Preserve the installed host's message agent
	// semantics so incompatible cross-protocol targets fail before writing.
	body, _ := json.Marshal(map[string]any{
		"model": o.Model, "stream": false, "max_tokens": 128,
		"messages": []any{map[string]any{"role": "user", "content": "Reply with OK.", "agent": "connection-check"}},
	})
	response, err := request(http.MethodPost, "/app/workbuddy/v1/chat/completions", body, false)
	if err != nil {
		return fmt.Errorf("WorkBuddy model business entry: %w", err)
	}
	if !validWorkBuddyProbe(response, o.Model) {
		return fmt.Errorf("WorkBuddy model business entry did not return a completed text result for the exact target model")
	}
	return nil
}

// A duplicate field can give different consumers different identities or
// completion states. Reject it before interpreting a successful HTTP reply.
func workBuddyDecodeJSON(raw []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := uniqueJSONValue(dec); err != nil {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	return json.Unmarshal(raw, into) == nil
}

func validWorkBuddyProbe(raw []byte, model string) bool {
	var r struct {
		Object, Model string
		Error         json.RawMessage
		Choices       []struct {
			Index        int
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role, Content string
				ToolCalls     []json.RawMessage `json:"tool_calls"`
				FunctionCall  json.RawMessage   `json:"function_call"`
			}
		}
	}
	if !workBuddyDecodeJSON(raw, &r) || r.Object != "chat.completion" || r.Model != model || (len(r.Error) != 0 && string(r.Error) != "null") || len(r.Choices) != 1 {
		return false
	}
	c := r.Choices[0]
	return c.Index == 0 && c.FinishReason == "stop" && c.Message.Role == "assistant" && strings.TrimSpace(c.Message.Content) != "" &&
		len(c.Message.ToolCalls) == 0 &&
		(len(c.Message.FunctionCall) == 0 || string(c.Message.FunctionCall) == "null")
}
