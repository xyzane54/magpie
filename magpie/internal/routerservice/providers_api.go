package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/routerconfig"
	"io"
	"net/http"
	"strings"
	"time"
)

func parseBoolQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func providerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func providerFailure(w http.ResponseWriter, err error) {
	message, status := routerconfig.ProviderError(err)
	providerJSON(w, status, map[string]string{"error": message})
}
func providerInput(w http.ResponseWriter, r *http.Request, value any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		providerJSON(w, 415, map[string]string{"error": "application/json required"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		providerJSON(w, 400, map[string]string{"error": "invalid provider JSON"})
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		providerJSON(w, 400, map[string]string{"error": "invalid provider JSON"})
		return false
	}
	return true
}

func providerAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, managementToken string, localRuntime ...*LocalModels) {
	if r.URL.Path == "/api/cloud-providers/models" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	if store == nil {
		providerJSON(w, 503, map[string]string{"error": "provider configuration unavailable"})
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		providers, err := store.CloudProvidersContext(r.Context())
		if err != nil {
			providerFailure(w, err)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			return
		}
		redacted, localModels, candidateModels := aggregateCandidateModels(r.Context(), store, providers, localRuntime...)
		providerJSON(w, 200, map[string]any{
			"providers":       redacted,
			"localModels":     localModels,
			"candidateModels": candidateModels,
		})
		return
	}
	if r.Method == http.MethodDelete {
		var in struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		}
		if !providerInput(w, r, &in) {
			return
		}
		if err := store.DeleteCloudProviderContext(r.Context(), in.ID, in.Revision); err != nil {
			providerFailure(w, err)
			return
		}
		providerJSON(w, 200, map[string]bool{"deleted": true})
		return
	}
	var reqIn providerModelRequest
	if !providerInput(w, r, &reqIn) {
		return
	}
	in := reqIn.ProviderEdit
	persist := reqIn.Persist || parseBoolQuery(r.URL.Query().Get("persist"))
	fallback := reqIn.Fallback || persist || parseBoolQuery(r.URL.Query().Get("fallback"))

	// A management token is never a cloud credential, including custom headers
	// and proxy credentials supplied by mistake. Do not echo the offending value.
	if (in.APIKey != nil && strings.Contains(*in.APIKey, managementToken)) || (in.ProxyURL != nil && strings.Contains(*in.ProxyURL, managementToken)) {
		providerJSON(w, 400, map[string]string{"error": "management token cannot be used as a cloud credential"})
		return
	}
	if in.Headers != nil {
		for _, value := range *in.Headers {
			if strings.Contains(value, managementToken) {
				providerJSON(w, 400, map[string]string{"error": "management token cannot be used as a cloud credential"})
				return
			}
		}
	}
	if in.ExtraBody != nil {
		body, _ := json.Marshal(in.ExtraBody)
		// Decode JSON strings before comparing so escaped ASCII cannot hide
		// this instance's management token in a private extra-body option.
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var normalized any
		if decoder.Decode(&normalized) != nil {
			providerJSON(w, 400, map[string]string{"error": "invalid extra body"})
			return
		}
		body, _ = json.Marshal(normalized)
		if strings.Contains(string(body), managementToken) {
			providerJSON(w, 400, map[string]string{"error": "management token cannot be used as a cloud credential"})
			return
		}
	}
	if r.URL.Path == "/api/cloud-providers/models" {
		if in.ID != "" && (persist || fallback || (in.Revision == 0 && strings.TrimSpace(in.Name) == "" && in.Protocol == "" && strings.TrimSpace(in.BaseURL) == "")) {
			if existingList, err := store.CloudProvidersContext(r.Context()); err == nil {
				for _, sp := range existingList {
					if sp.ID == in.ID {
						if in.Revision == 0 {
							in.Revision = sp.Revision
						}
						if strings.TrimSpace(in.Name) == "" {
							in.Name = sp.Name
						}
						if in.Preset == "" {
							in.Preset = sp.Preset
						}
						if in.Protocol == "" {
							in.Protocol = sp.Protocol
						}
						if strings.TrimSpace(in.BaseURL) == "" {
							in.BaseURL = sp.BaseURL
						}
						if in.ProxyMode == "" {
							in.ProxyMode = sp.ProxyMode
						}
						break
					}
				}
			}
		}
		resolved, err := store.ResolveCloudProviderContext(r.Context(), in)
		if err != nil {
			providerFailure(w, err)
			return
		}
		if strings.Contains(resolved.APIKey, managementToken) || strings.Contains(resolved.ProxyURL, managementToken) {
			providerJSON(w, 400, map[string]string{"error": "management token cannot be forwarded upstream"})
			return
		}
		for _, value := range resolved.Headers {
			if strings.Contains(value, managementToken) {
				providerJSON(w, 400, map[string]string{"error": "management token cannot be forwarded upstream"})
				return
			}
		}
		rawModels, rawErr := providerModels(r.Context(), resolved)
		result, err := completeProviderModelList(resolved.Config, rawModels, rawErr)
		if !persist && !fallback {
			if err != nil {
				code := "model_list_failed"
				var failure *modelListError
				if errors.As(err, &failure) {
					code = failure.Code
				}
				providerJSON(w, 502, map[string]string{"error": err.Error(), "code": code})
				return
			}
			providerJSON(w, 200, result)
			return
		}
		if rawErr == nil && len(rawModels) > 0 {
			discovered := deduplicateModelIDs(resolved.Config.BaseURL, result.Models)
			if len(discovered) == 0 {
				discovered = deduplicateModelIDs(resolved.Config.BaseURL, rawModels)
			}
			if persist && strings.TrimSpace(in.ID) != "" && len(discovered) > 0 {
				if _, saveErr := store.SaveProviderDiscoveredModelsContext(r.Context(), in.ID, discovered); saveErr != nil {
					providerFailure(w, saveErr)
					return
				}
			}
			source := result.Source
			if source == "" || source == "upstream" {
				source = "discovered"
			}
			providerJSON(w, 200, providerModelListResult{
				Models:           discovered,
				DocumentedModels: result.DocumentedModels,
				Source:           source,
				DocsURL:          result.DocsURL,
				Fallback:         false,
			})
			return
		}
		if cached := deduplicateModelIDs(resolved.Config.BaseURL, resolved.Config.DiscoveredModels); len(cached) > 0 {
			providerJSON(w, 200, providerModelListResult{
				Models:           cached,
				DocumentedModels: result.DocumentedModels,
				Source:           "discovered",
				DocsURL:          result.DocsURL,
				Fallback:         true,
			})
			return
		}
		presetModels := presetModelsForProvider(resolved.Config)
		if len(presetModels) > 0 {
			providerJSON(w, 200, providerModelListResult{
				Models:           presetModels,
				DocumentedModels: result.DocumentedModels,
				Source:           "preset",
				DocsURL:          result.DocsURL,
				Fallback:         true,
			})
			return
		}
		providerJSON(w, 200, providerModelListResult{
			Models:           []string{},
			DocumentedModels: result.DocumentedModels,
			Source:           "none",
			DocsURL:          result.DocsURL,
			Fallback:         true,
		})
		return
	}
	saved, err := store.SaveCloudProviderContext(r.Context(), in)
	if err != nil {
		providerFailure(w, err)
		return
	}
	providerJSON(w, 200, map[string]any{"provider": redactProvider(saved)})
}
