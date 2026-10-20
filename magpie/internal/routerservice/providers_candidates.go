package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerruntime"
	"runtime"
	"strings"
)

func aggregateLocalModelCandidates(ctx context.Context, store *routerconfig.Store, localRuntime ...*LocalModels) []localModelCandidate {
	catalog := routermanifest.Fixed()
	var runtimeCfg struct {
		Enabled      bool   `json:"enabled"`
		Selected     string `json:"selected"`
		Installed    bool   `json:"installed"`
		Installation string `json:"installation"`
		Status       string `json:"status"`
		Models       map[string]struct {
			Installed bool   `json:"installed"`
			Enabled   bool   `json:"enabled"`
			Status    string `json:"status"`
		} `json:"models"`
	}
	type localModelCfgEntry struct {
		ID        string `json:"id"`
		Installed bool   `json:"installed"`
		Enabled   bool   `json:"enabled"`
		Status    string `json:"status"`
	}
	localCfgByID := map[string]localModelCfgEntry{}
	if store != nil {
		if doc, err := store.LoadContext(ctx); err == nil {
			_ = json.Unmarshal(doc["runtime"], &runtimeCfg)
			for k, v := range runtimeCfg.Models {
				localCfgByID[strings.ToLower(strings.TrimSpace(k))] = localModelCfgEntry{
					ID:        strings.ToLower(strings.TrimSpace(k)),
					Installed: v.Installed,
					Enabled:   v.Enabled,
					Status:    v.Status,
				}
			}
			if rawLocal, ok := doc["localModels"]; ok && len(bytes.TrimSpace(rawLocal)) > 0 {
				var byMap map[string]localModelCfgEntry
				if json.Unmarshal(rawLocal, &byMap) == nil && len(byMap) > 0 {
					for k, v := range byMap {
						key := strings.ToLower(strings.TrimSpace(k))
						if v.ID != "" {
							key = strings.ToLower(strings.TrimSpace(v.ID))
						}
						localCfgByID[key] = v
					}
				} else {
					var bySlice []localModelCfgEntry
					if json.Unmarshal(rawLocal, &bySlice) == nil {
						for _, v := range bySlice {
							key := strings.ToLower(strings.TrimSpace(v.ID))
							if key != "" {
								localCfgByID[key] = v
							}
						}
					}
				}
			}
		}
	}
	var lm *LocalModels
	if len(localRuntime) > 0 {
		lm = localRuntime[0]
	}
	var mgrStatus routerruntime.Status
	hasMgrStatus := false
	if lm != nil && lm.Manager != nil {
		mgrStatus = lm.Manager.Status()
		hasMgrStatus = true
	}
	rtID := routerruntime.PlatformRuntime(catalog, runtime.GOOS, runtime.GOARCH)
	rtSpec, hasRtSpec := catalog.Runtime(rtID)

	out := make([]localModelCandidate, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		normID := strings.ToLower(strings.TrimSpace(m.ID))
		displayName := "Spark-X2.5-" + m.Tier
		selected := strings.EqualFold(runtimeCfg.Selected, m.ID) || strings.EqualFold(runtimeCfg.Selected, normID)
		enabled := selected && runtimeCfg.Enabled
		installed := false
		status := ""

		if selected {
			if runtimeCfg.Installed || strings.EqualFold(runtimeCfg.Installation, "installed") || strings.EqualFold(runtimeCfg.Status, "installed") || strings.EqualFold(runtimeCfg.Status, "ready") {
				installed = true
			}
			if runtimeCfg.Status != "" {
				status = runtimeCfg.Status
			} else if runtimeCfg.Installation != "" {
				status = runtimeCfg.Installation
			}
		}
		if cfg, ok := localCfgByID[normID]; ok {
			if cfg.Installed || strings.EqualFold(cfg.Status, "installed") || strings.EqualFold(cfg.Status, "ready") {
				installed = true
			}
			if cfg.Enabled {
				enabled = true
			}
			if cfg.Status != "" {
				status = cfg.Status
			}
		}
		if hasMgrStatus {
			if strings.EqualFold(mgrStatus.Intent.Selected, m.ID) || strings.EqualFold(mgrStatus.Intent.Selected, normID) {
				selected = true
				enabled = mgrStatus.Intent.Enabled
				if strings.EqualFold(mgrStatus.Installation, "installed") || mgrStatus.Runtime == routerruntime.Ready || mgrStatus.Runtime == routerruntime.Busy || mgrStatus.Runtime == routerruntime.Idle {
					installed = true
				}
				if mgrStatus.Runtime != "" {
					status = string(mgrStatus.Runtime)
				}
			}
		}
		if !installed && lm != nil && lm.Layout != (dirs.Layout{}) && hasRtSpec {
			installer := routerruntime.FileInstaller{Layout: lm.Layout}
			if _, err := installer.Verify(ctx, m, rtSpec); err == nil {
				installed = true
			}
		}
		if status == "" {
			if installed {
				status = "installed"
			} else {
				status = "not_installed"
			}
		}
		out = append(out, localModelCandidate{
			ID:        normID,
			ModelID:   normID,
			Name:      displayName,
			Tier:      m.Tier,
			Selector:  "local/" + normID,
			Installed: installed,
			Enabled:   enabled,
			Selected:  selected,
			Status:    status,
		})
	}
	return out
}

func aggregateCandidateModels(ctx context.Context, store *routerconfig.Store, providers []routerconfig.CloudProvider, localRuntime ...*LocalModels) ([]redactedCloudProvider, []localModelCandidate, []candidateModelEntry) {
	redacted := redactProviders(providers)
	localModels := aggregateLocalModelCandidates(ctx, store, localRuntime...)
	candidates := make([]candidateModelEntry, 0, len(localModels)+len(redacted)*4)
	for _, p := range redacted {
		for _, modelID := range p.EffectiveModels {
			candidates = append(candidates, candidateModelEntry{
				Selector:     p.ID + "/" + modelID,
				SourceType:   "cloud",
				ProviderID:   p.ID,
				ProviderName: p.Name,
				Preset:       p.Preset,
				ModelID:      modelID,
				Name:         modelID,
				ModelSource:  p.ModelSource,
				Installed:    true,
				Status:       "available",
			})
		}
	}
	for _, lm := range localModels {
		candidates = append(candidates, candidateModelEntry{
			Selector:    lm.Selector,
			SourceType:  "local",
			ModelID:     lm.ID,
			Name:        lm.Name,
			ModelSource: "local",
			Installed:   lm.Installed,
			Status:      lm.Status,
		})
	}
	return redacted, localModels, candidates
}

type providerModelRequest struct {
	routerconfig.ProviderEdit
	Persist  bool `json:"persist,omitempty"`
	Fallback bool `json:"fallback,omitempty"`
}

type providerModelListResult struct {
	Models           []string `json:"models"`
	DocumentedModels []string `json:"documentedModels,omitempty"`
	Source           string   `json:"source,omitempty"`
	DocsURL          string   `json:"docsURL,omitempty"`
	Fallback         bool     `json:"fallback,omitempty"`
}
