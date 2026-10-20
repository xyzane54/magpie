package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// providerModels runs only for an explicit management action. It never detects
// protocols, scans accounts, retries a different target, or refreshes a catalog.
func providerModels(ctx context.Context, p routerconfig.ResolvedProvider) ([]string, error) {
	if address, err := url.Parse(p.Config.BaseURL); err == nil && strings.EqualFold(address.Hostname(), "api.openai.com") && p.APIKey == "" {
		hasAuthorization := false
		for name, value := range p.Headers {
			if strings.EqualFold(name, "Authorization") && strings.TrimSpace(value) != "" {
				hasAuthorization = true
			}
		}
		if !hasAuthorization {
			return nil, modelListFailure("model_key_required", "enter an API key before fetching OpenAI models")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 8 * time.Second}
	switch p.Config.ProxyMode {
	case "direct":
		transport.Proxy = nil
	case "custom":
		proxy, err := url.Parse(p.ProxyURL)
		if err != nil {
			return nil, errors.New("invalid proxy configuration")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(p.Config.BaseURL, "/")
	if p.Config.Protocol == "anthropic" && !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	endpoint := base + "/models"
	models := []string{}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, errors.New("invalid model-list address")
		}
		req.Header.Set("Accept", "application/json")
		if p.APIKey != "" {
			if p.Config.Protocol == "anthropic" {
				req.Header.Set("x-api-key", p.APIKey)
			} else {
				req.Header.Set("Authorization", "Bearer "+p.APIKey)
			}
		}
		if p.Config.Protocol == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		for key, value := range p.Headers {
			req.Header.Set(key, value)
		}
		response, err := client.Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, modelListFailure("model_list_timeout", "model list request timed out; check address and proxy")
			}
			return nil, modelListFailure("model_list_connection_failed", "could not connect to the model list; check address and proxy")
		}
		var result struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			// Upstream error bodies and URLs can contain keys. Never relay them.
			switch response.StatusCode {
			case 401:
				return nil, modelListFailure("model_list_auth_failed", "upstream rejected the API key (HTTP 401)")
			case 403:
				return nil, modelListFailure("model_list_forbidden", "upstream denied model-list access (HTTP 403)")
			case 404, 405:
				return nil, modelListFailure("model_list_unavailable", "upstream does not provide a model list at this address; enter a model ID manually")
			case 429:
				return nil, modelListFailure("model_list_rate_limited", "upstream model-list rate limit reached (HTTP 429); try again later")
			default:
				return nil, modelListFailure("model_list_upstream_failed", "upstream model-list request failed; check provider status or enter a model ID manually")
			}
		}
		if readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &result) != nil || result.Data == nil {
			if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, modelListFailure("model_list_timeout", "model list request timed out; check address and proxy")
			}
			return nil, modelListFailure("model_list_invalid_response", "upstream returned an unsupported model list; enter a model ID manually")
		}
		added := 0
		for _, model := range result.Data {
			if model.ID == "" || len(model.ID) > 256 || strings.ContainsAny(model.ID, "\r\n\x00") {
				return nil, errors.New("upstream returned an invalid model ID")
			}
			if !seen[model.ID] {
				seen[model.ID] = true
				models = append(models, model.ID)
				added++
			}
			if len(models) > 1000 {
				return nil, errors.New("model list is too large; enter a model ID manually")
			}
		}
		if !result.HasMore {
			return ordinaryModelCandidates(p.Config.BaseURL, models), nil
		}
		if p.Config.Protocol != "anthropic" || result.LastID == "" || added == 0 {
			return nil, errors.New("upstream model pagination is unsupported; enter a model ID manually")
		}
		query := url.Values{"after_id": {result.LastID}, "limit": {"100"}}
		endpoint = base + "/models?" + query.Encode()
	}
	return nil, errors.New("model list has too many pages; enter a model ID manually")
}
