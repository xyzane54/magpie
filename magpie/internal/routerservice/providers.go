package routerservice

import (
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/routerconfig"
	"net/url"
	"strings"
	"sync"
)

// Model-list failures expose only fixed messages and codes, never upstream data.
type modelListError struct {
	Code    string
	Message string
}

func (e *modelListError) Error() string { return e.Message }
func modelListFailure(code, message string) error {
	return &modelListError{Code: code, Message: message}
}

type providerPresetCatalogEntry struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Endpoints map[string]string `json:"endpoints"`
	Models    []string          `json:"models"`
}

var (
	providerPresetsOnce sync.Once
	parsedPresets       []providerPresetCatalogEntry
)

func loadProviderPresets() []providerPresetCatalogEntry {
	providerPresetsOnce.Do(func() {
		_ = json.Unmarshal(providerPresets, &parsedPresets)
	})
	return parsedPresets
}

func deduplicateModelIDs(baseURL string, raw []string) []string {
	cleaned := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		m := strings.TrimSpace(item)
		if m == "" || len(m) > 256 || strings.ContainsAny(m, "\r\n\x00") || seen[m] {
			continue
		}
		seen[m] = true
		cleaned = append(cleaned, m)
	}
	candidates := ordinaryModelCandidates(baseURL, cleaned)
	if len(candidates) == 0 {
		return []string{}
	}
	return candidates
}

func presetModelsForProvider(p routerconfig.CloudProvider) []string {
	presets := loadProviderPresets()
	var matched []string
	presetID := strings.TrimSpace(p.Preset)
	if presetID != "" && !strings.EqualFold(presetID, "custom") {
		for _, entry := range presets {
			if strings.EqualFold(entry.ID, presetID) && len(entry.Models) > 0 {
				matched = append(matched, entry.Models...)
				break
			}
		}
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if len(matched) == 0 && base != "" {
		for _, entry := range presets {
			if len(entry.Models) == 0 {
				continue
			}
			for _, ep := range entry.Endpoints {
				if strings.EqualFold(strings.TrimRight(strings.TrimSpace(ep), "/"), base) {
					matched = append(matched, entry.Models...)
					break
				}
			}
			if len(matched) > 0 {
				break
			}
		}
	}
	if len(matched) == 0 && base != "" {
		if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
			host := u.Hostname()
			for _, entry := range presets {
				if len(entry.Models) == 0 {
					continue
				}
				for _, ep := range entry.Endpoints {
					if eu, err := url.Parse(ep); err == nil && strings.EqualFold(eu.Hostname(), host) {
						matched = append(matched, entry.Models...)
						break
					}
				}
				if len(matched) > 0 {
					break
				}
			}
		}
	}
	if completed, err := completeProviderModelList(p, nil, errors.New("preset_fallback")); err == nil {
		matched = append(matched, completed.Models...)
		matched = append(matched, completed.DocumentedModels...)
	}
	return deduplicateModelIDs(p.BaseURL, matched)
}

func effectiveProviderModels(p routerconfig.CloudProvider) []string {
	if len(p.DiscoveredModels) > 0 {
		if completed, err := completeProviderModelList(p, p.DiscoveredModels, nil); err == nil && len(completed.Models) > 0 {
			return deduplicateModelIDs(p.BaseURL, completed.Models)
		}
		if discovered := deduplicateModelIDs(p.BaseURL, p.DiscoveredModels); len(discovered) > 0 {
			return discovered
		}
	}
	return presetModelsForProvider(p)
}

type redactedCloudProvider struct {
	ID               string   `json:"id"`
	Revision         int      `json:"revision"`
	Name             string   `json:"name"`
	Preset           string   `json:"preset"`
	Protocol         string   `json:"protocol"`
	BaseURL          string   `json:"baseURL"`
	HasKey           bool     `json:"hasKey"`
	HeaderNames      []string `json:"headerNames,omitempty"`
	HasExtraBody     bool     `json:"hasExtraBody"`
	ProxyMode        string   `json:"proxyMode"`
	HasProxyURL      bool     `json:"hasProxyURL"`
	DiscoveredModels []string `json:"discoveredModels"`
	Models           []string `json:"models"`
	EffectiveModels  []string `json:"effectiveModels"`
	ModelSource      string   `json:"modelSource,omitempty"`
}

func redactProvider(p routerconfig.CloudProvider) redactedCloudProvider {
	discovered := deduplicateModelIDs(p.BaseURL, p.DiscoveredModels)
	effective := effectiveProviderModels(p)
	source := ""
	if len(discovered) > 0 {
		source = "discovered"
	} else if len(effective) > 0 {
		source = "preset"
	}
	return redactedCloudProvider{
		ID:               p.ID,
		Revision:         p.Revision,
		Name:             p.Name,
		Preset:           p.Preset,
		Protocol:         p.Protocol,
		BaseURL:          p.BaseURL,
		HasKey:           p.HasKey,
		HeaderNames:      p.HeaderNames,
		HasExtraBody:     p.HasExtraBody,
		ProxyMode:        p.ProxyMode,
		HasProxyURL:      p.HasProxyURL,
		DiscoveredModels: discovered,
		Models:           effective,
		EffectiveModels:  effective,
		ModelSource:      source,
	}
}

func redactProviders(providers []routerconfig.CloudProvider) []redactedCloudProvider {
	out := make([]redactedCloudProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, redactProvider(p))
	}
	return out
}

type localModelCandidate struct {
	ID        string `json:"id"`
	ModelID   string `json:"modelId"`
	Name      string `json:"name"`
	Tier      string `json:"tier"`
	Selector  string `json:"selector"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Selected  bool   `json:"selected"`
	Status    string `json:"status"`
}

type candidateModelEntry struct {
	Selector     string `json:"selector"`
	SourceType   string `json:"sourceType"`
	ProviderID   string `json:"providerId,omitempty"`
	ProviderName string `json:"providerName,omitempty"`
	Preset       string `json:"preset,omitempty"`
	ModelID      string `json:"modelId"`
	Name         string `json:"name,omitempty"`
	ModelSource  string `json:"modelSource,omitempty"`
	Installed    bool   `json:"installed,omitempty"`
	Status       string `json:"status,omitempty"`
}
