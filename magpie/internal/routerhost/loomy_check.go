package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type loomyChecker struct{ managementToken string }

// LoomyChecker verifies the stored-credential Loomy Profile and its installed
// OpenAI-compatible protocol. Explicit dynamic sessions require a real host
// request and are never substituted into this automatic configuration flow.
func LoomyChecker(managementToken string) Checker { return loomyChecker{managementToken} }

func (c loomyChecker) Check(ctx context.Context, o Options) (result error) {
	defer func() {
		if result != nil {
			result = safeCheckError{result}
		}
	}()
	if o.Host != "loomy" {
		return errors.New("Loomy checker requires the Loomy application")
	}
	if err := validateOptions(o); err != nil {
		return err
	}
	if strings.TrimSpace(c.managementToken) == "" || strings.ContainsAny(c.managementToken, "\r\n") {
		return errors.New("Loomy application validation requires an explicit management token")
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request := func(method, path string, body []byte, management bool) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(o.RouterURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("cannot construct Loomy local check")
		}
		if management {
			req.Header.Set("Authorization", "Bearer "+c.managementToken)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("Loomy local endpoint unavailable; start Router and retry")
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("invalid or oversized Loomy check response")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Loomy local check returned HTTP %d; inspect Router app/provider status", resp.StatusCode)
		}
		return data, nil
	}
	health, err := request(http.MethodGet, "/healthz", nil, false)
	if err != nil {
		return err
	}
	var h struct{ Service, Status string }
	if json.Unmarshal(health, &h) != nil || h.Service != "api-router" || h.Status != "ok" {
		return errors.New("Loomy endpoint is not a healthy API-router service")
	}
	apps, err := request(http.MethodGet, "/api/apps", nil, true)
	if err != nil {
		return err
	}
	if err := validateAppProjection(apps, o); err != nil {
		return fmt.Errorf("Loomy requires an exact stored provider/model binding; dynamic session configuration must be verified from the real host: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"model": o.Model, "stream": false, "max_tokens": 128,
		"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}})
	response, err := request(http.MethodPost, "/app/loomy/v1/chat/completions", body, false)
	if err != nil {
		return err
	}
	if !validLoomyProbe(response) {
		return errors.New("Loomy model entry did not return completed nonempty chat text")
	}
	return nil
}

func validLoomyProbe(raw []byte) bool {
	var r struct {
		Object  string          `json:"object"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Object != "chat.completion" || len(r.Error) > 0 && string(r.Error) != "null" {
		return false
	}
	for _, choice := range r.Choices {
		if choice.FinishReason == "stop" && choice.Message.Role == "assistant" && strings.TrimSpace(choice.Message.Content) != "" {
			return true
		}
	}
	return false
}
