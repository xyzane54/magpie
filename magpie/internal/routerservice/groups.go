package routerservice

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/routerconfig"
)

type LocalRuntime = LocalModels

type groupView struct {
	routerconfig.Group
	Enabled bool `json:"enabled"`
}

type groupRequest struct {
	routerconfig.Group
	ContextRaw   any      `json:"context"`
	Enabled      *bool    `json:"enabled"`
	Disabled     *bool    `json:"disabled"`
	Alias        string   `json:"alias"`
	Patterns     []string `json:"patterns"`
	HideMembers  *bool    `json:"hideMembers"`
	Active       string   `json:"active"`
	CooldownMove *bool    `json:"cooldownMove"`
}

func parseFlexibleContext(raw any) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		if s == "" || s == "max" {
			return 0
		}
		if s == "min" {
			return routerconfig.ContextSmallest
		}
	}
	return 0
}

func toGroupView(g routerconfig.Group) groupView {
	if g.Members == nil {
		g.Members = []string{}
	}
	return groupView{Group: g, Enabled: !g.Disabled}
}

func toGroupViews(groups []routerconfig.Group) []groupView {
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		if !g.Hidden {
			out = append(out, toGroupView(g))
		}
	}
	return out
}

func groupCatalogModels(cfg routerconfig.Config, local *LocalRuntime) map[string][]string {
	var presets []struct {
		ID      string   `json:"id"`
		BaseURL string   `json:"baseURL"`
		Models  []string `json:"models"`
	}
	_ = json.Unmarshal(providerPresets, &presets)
	out := make(map[string][]string, len(cfg.Providers)+1)
	for _, p := range cfg.Providers {
		pid := strings.TrimSpace(p.ID)
		if pid == "" {
			continue
		}
		if len(p.DiscoveredModels) > 0 {
			out[pid] = append([]string(nil), p.DiscoveredModels...)
			continue
		}
		base := strings.TrimRight(pid, "-0123456789")
		for _, pr := range presets {
			if (p.Preset != "" && strings.EqualFold(pr.ID, p.Preset)) || strings.EqualFold(pr.ID, pid) ||
				(base != "" && strings.EqualFold(pr.ID, base)) ||
				(p.BaseURL != "" && strings.EqualFold(strings.TrimRight(pr.BaseURL, "/"), strings.TrimRight(p.BaseURL, "/"))) {
				out[pid] = ordinaryModelCandidates(p.BaseURL, pr.Models)
				break
			}
		}
	}
	if localModels := activeLocalModels(local); len(localModels) > 0 {
		out["local"] = localModels
	}
	return out
}

func activeLocalModels(local *LocalRuntime) []string {
	if local == nil {
		return nil
	}
	if local.Manager != nil {
		if st := local.Manager.Status(); strings.TrimSpace(st.Intent.Selected) != "" {
			return []string{strings.TrimSpace(st.Intent.Selected)}
		}
		return nil
	}
	var models []string
	for _, m := range local.Catalog.Models {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	return models
}

func effectiveGroups(cfg routerconfig.Config, local *LocalRuntime) []routerconfig.Group {
	catalog := groupCatalogModels(cfg, local)
	if localModels := catalog["local"]; len(localModels) > 0 {
		cfg.Providers = append([]routerconfig.CloudProvider{{ID: "local", Name: "Local", DiscoveredModels: localModels}}, cfg.Providers...)
	}
	return routerconfig.EffectiveGroups(cfg, catalog)
}

func validateGroupInput(g routerconfig.Group) error {
	if !g.Hidden && strings.TrimSpace(g.Name) == "" {
		return &routerconfig.GroupValidation{Message: "group name is required"}
	}
	switch strings.ToLower(strings.TrimSpace(g.Routing)) {
	case "", "order", "ordered", "weight", "weighted", "manual", "smart_split", "smart_hybrid", "smart", "auto", "rules", "rotate", "round_robin", "round-robin", "usage", "least", "least_used", "least-used", "pace", "drain-reset", "drain_reset":
	default:
		return &routerconfig.GroupValidation{Message: "unsupported routing strategy"}
	}
	switch strings.ToLower(strings.TrimSpace(g.Affinity)) {
	case "", "auto", "session", "turn", "off", "pin", "strict":
	default:
		return &routerconfig.GroupValidation{Message: "unsupported affinity mode"}
	}
	if g.Patience < -1 || g.FirstToken < -1 || g.Stickiness < 0 {
		return &routerconfig.GroupValidation{Message: "invalid timeout or patience setting"}
	}
	for k, w := range g.Weights {
		if strings.TrimSpace(k) == "" || w <= 0 || w > 100 {
			return &routerconfig.GroupValidation{Message: "group weight must be between 1 and 100"}
		}
	}
	for _, r := range g.Rules {
		switch strings.ToLower(strings.TrimSpace(r.When)) {
		case "", "prompt", "system", "model", "tools", "vision", "tokens":
		default:
			return &routerconfig.GroupValidation{Message: "unsupported routing rule when dimension"}
		}
		if strings.TrimSpace(r.Use) == "" && strings.TrimSpace(r.Target) == "" && strings.TrimSpace(r.Pause) == "" {
			return &routerconfig.GroupValidation{Message: "routing rule target is required"}
		}
	}
	return nil
}

func writeGroupErr(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg, "code": code})
}

func writeGroupStoreErr(w http.ResponseWriter, code string, err error) {
	msg, status := routerconfig.GroupError(err)
	writeGroupErr(w, status, code, msg)
}

func routingGroupsAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, local *LocalRuntime) {
	w.Header().Set("Content-Type", "application/json")
	if store == nil {
		writeGroupErr(w, http.StatusServiceUnavailable, "store_unavailable", "configuration store unavailable")
		return
	}
	if r.URL.Path == "/api/routing-groups/switch" {
		handleGroupSwitch(w, r, store, local)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		cfg, err := store.LoadConfigContext(r.Context())
		if err != nil {
			writeGroupStoreErr(w, "group_load_failed", err)
			return
		}
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "groups": toGroupViews(effectiveGroups(cfg, local))})
		}
	case http.MethodPost:
		var req groupRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeGroupErr(w, http.StatusBadRequest, "invalid_json", "invalid routing group payload")
			return
		}
		g := req.Group
		g.Context = parseFlexibleContext(req.ContextRaw)
		if g.Family == "" && strings.TrimSpace(req.Alias) != "" {
			g.Family = strings.TrimSpace(req.Alias)
		}
		if len(g.Match) == 0 && len(req.Patterns) > 0 {
			g.Match = req.Patterns
		}
		if req.HideMembers != nil && !g.Unlists {
			g.Unlists = *req.HideMembers
		}
		if g.Pick == "" && strings.TrimSpace(req.Active) != "" {
			g.Pick = strings.TrimSpace(req.Active)
		}
		if req.CooldownMove != nil && !g.Sink {
			g.Sink = *req.CooldownMove
		}
		if req.Disabled != nil {
			g.Disabled = *req.Disabled
		}
		if req.Enabled != nil {
			g.Disabled = !*req.Enabled
		}
		if err := validateGroupInput(g); err != nil {
			writeGroupStoreErr(w, "invalid_group", err)
			return
		}
		saved, err := store.SaveGroupContext(r.Context(), g)
		if err != nil {
			writeGroupStoreErr(w, "invalid_group", err)
			return
		}
		cfg, _ := store.LoadConfigContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "group": toGroupView(saved), "groups": toGroupViews(effectiveGroups(cfg, local))})
	case http.MethodDelete:
		handleGroupDelete(w, r, store, local)
	}
}

func handleGroupSwitch(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, local *LocalRuntime) {
	var req struct {
		ID       string `json:"id"`
		Enabled  *bool  `json:"enabled"`
		Disabled *bool  `json:"disabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeGroupErr(w, http.StatusBadRequest, "invalid_json", "invalid switch payload")
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" || (req.Enabled == nil && req.Disabled == nil) {
		writeGroupErr(w, http.StatusBadRequest, "invalid_group", "group id and enabled state are required")
		return
	}
	cfg, err := store.LoadConfigContext(r.Context())
	if err != nil {
		writeGroupStoreErr(w, "group_load_failed", err)
		return
	}
	target, found := findGroupForSwitch(cfg, local, id)
	if !found {
		writeGroupErr(w, http.StatusNotFound, "group_not_found", "routing group not found")
		return
	}
	if req.Disabled != nil {
		target.Disabled = *req.Disabled
	}
	if req.Enabled != nil {
		target.Disabled = !*req.Enabled
	}
	target.Hidden = false
	saved, err := store.SaveGroupContext(r.Context(), target)
	if err != nil {
		writeGroupStoreErr(w, "invalid_group", err)
		return
	}
	cfg, _ = store.LoadConfigContext(r.Context())
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "group": toGroupView(saved), "groups": toGroupViews(effectiveGroups(cfg, local))})
}

func findGroupForSwitch(cfg routerconfig.Config, local *LocalRuntime, id string) (routerconfig.Group, bool) {
	slug := routerconfig.GroupSlug(id)
	for _, list := range [][]routerconfig.Group{cfg.Groups, effectiveGroups(cfg, local)} {
		for _, g := range list {
			if !g.Hidden && (strings.EqualFold(g.ID, id) || routerconfig.GroupSlug(g.ID) == slug) {
				return g, true
			}
		}
	}
	return routerconfig.Group{}, false
}

func handleGroupDelete(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, local *LocalRuntime) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && r.Body != nil {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		id = strings.TrimSpace(body.ID)
	}
	if id == "" {
		writeGroupErr(w, http.StatusBadRequest, "invalid_group", "group id is required")
		return
	}
	cfg, err := store.LoadConfigContext(r.Context())
	if err != nil {
		writeGroupStoreErr(w, "group_load_failed", err)
		return
	}
	if _, ok := findGroupForSwitch(cfg, local, id); !ok {
		writeGroupErr(w, http.StatusNotFound, "group_not_found", "routing group not found")
		return
	}
	deleted, err := store.DeleteGroupContext(r.Context(), id)
	if err != nil || !deleted {
		if err != nil {
			writeGroupStoreErr(w, "invalid_group", err)
		} else {
			writeGroupErr(w, http.StatusNotFound, "group_not_found", "routing group not found")
		}
		return
	}
	cfg, _ = store.LoadConfigContext(r.Context())
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": true, "groups": toGroupViews(effectiveGroups(cfg, local))})
}
