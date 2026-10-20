package routerservice

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerhost"
)

// hostAutoAPI manages fast probe-free connection and restoration for WorkBuddy
// and Loomy, supporting multi-select routing groups/models and status inspection.
func hostAutoAPI(w http.ResponseWriter, r *http.Request, info Info, token string, store *routerconfig.Store, stateDir string) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		items := inspectAutoHosts(r.Context(), store)
		if r.Method == http.MethodHead {
			return
		}
		providerJSON(w, http.StatusOK, map[string]any{"items": items, "hosts": items})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetReadDeadline(deadline)
	var in struct {
		Host       string   `json:"host"`
		Action     string   `json:"action"`
		Enabled    *bool    `json:"enabled"`
		Model      string   `json:"model"`
		Models     []string `json:"models"`
		ConfigRoot string   `json:"configRoot"`
		HomeDir    string   `json:"homeDir"`
		AppDataDir string   `json:"appDataDir"`
	}
	if !providerInput(w, r, &in) {
		w.Header().Set("Connection", "close")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	fail := func(status int, code, message string) {
		providerJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	if stateDir == "" {
		fail(http.StatusServiceUnavailable, "host_recovery_unavailable", "Host recovery directory unavailable")
		return
	}
	if token != "" && (strings.Contains(in.Model, token) || strings.Contains(in.ConfigRoot, token) || containsSecret(in.Models, token)) {
		fail(http.StatusForbidden, "credential_misuse", "Management credentials cannot be used in host settings")
		return
	}
	if in.Host != "workbuddy" && in.Host != "loomy" {
		fail(http.StatusBadRequest, "unsupported_host", "Automatic connection supports WorkBuddy and Loomy only")
		return
	}
	disconnect := (in.Enabled != nil && !*in.Enabled) || in.Action == "disconnect" || in.Action == "restore"
	root, err := resolveAutoHostRoot(in.Host, in.ConfigRoot, in.HomeDir, in.AppDataDir)
	if err != nil && !disconnect {
		fail(http.StatusConflict, "host_config_unavailable", err.Error())
		return
	}
	if token != "" && strings.Contains(root, token) {
		fail(http.StatusForbidden, "credential_misuse", "Management credentials cannot be used in host settings")
		return
	}
	e := autoHostEngine(in.Host, stateDir, token)
	models := routerhost.Options{Model: in.Model, Models: in.Models}.EffectiveModels()
	if len(models) == 0 && store != nil {
		if saved := findSavedApp(ctx, store, in.Host); saved != nil {
			models = routerhost.Options{Model: saved.Cloud.Model, Models: saved.Models}.EffectiveModels()
		}
	}
	if disconnect {
		if root != "" {
			if p, err := e.PreviewRestore(in.Host, root); err == nil {
				if err := e.Restore(p); err != nil {
					fail(http.StatusConflict, "host_operation_failed", "Host configuration restore failed; recovery record is retained")
					return
				}
			}
		}
		syncAutoHostStore(ctx, store, in.Host, false, models)
		primary := ""
		if len(models) > 0 {
			primary = models[0]
		}
		if models == nil {
			models = []string{}
		}
		providerJSON(w, http.StatusOK, map[string]any{
			"connected": false, "enabled": false, "host": in.Host, "model": primary, "models": models, "configRoot": root,
		})
		return
	}
	o := routerhost.NormalizeOptions(routerhost.Options{Host: in.Host, ConfigRoot: root, RouterURL: "http://" + info.Address, Model: in.Model, Models: models})
	p, err := e.Connect(ctx, o)
	if err != nil {
		fail(http.StatusConflict, "host_operation_failed", "Host connection checks or configuration apply failed; the Profile remains saved and the recovery record is retained")
		return
	}
	effective := o.EffectiveModels()
	syncAutoHostStore(ctx, store, in.Host, true, effective)
	primary := o.PrimaryModel()
	providerJSON(w, http.StatusOK, map[string]any{
		"connected":  true,
		"enabled":    true,
		"host":       in.Host,
		"model":      primary,
		"models":     effective,
		"configRoot": root,
		"selection":  map[string]string{"workbuddy": "custom-local:" + primary, "loomy": "api_router/" + primary}[in.Host],
		"plan":       p.Summary(),
	})
}

func autoHostEngine(host, stateDir, token string) routerhost.Engine {
	var adapter routerhost.Adapter = routerhost.WorkBuddyAdapter{}
	if host == "loomy" {
		adapter = routerhost.LoomyAdapter{}
	}
	return routerhost.Engine{StateDir: stateDir, Adapter: adapter, Checker: routerhost.FastChecker(token)}
}

func findSavedApp(ctx context.Context, store *routerconfig.Store, host string) *routerconfig.AppProfile {
	if store == nil {
		return nil
	}
	apps, err := store.AppsContext(ctx)
	if err != nil {
		return nil
	}
	for i := range apps {
		if apps[i].ID == host {
			return &apps[i]
		}
	}
	return nil
}

func syncAutoHostStore(ctx context.Context, store *routerconfig.Store, host string, enabled bool, models []string) {
	cur := findSavedApp(ctx, store, host)
	if cur == nil {
		return
	}
	edit := routerconfig.AppEdit{
		ID: host, Revision: cur.Revision, Name: cur.Name, Enabled: &enabled,
		Cloud: cur.Cloud, CredentialMode: cur.CredentialMode,
	}
	if len(models) > 0 {
		cp := append([]string(nil), models...)
		edit.Models = &cp
		if strings.HasPrefix(models[0], "group/") || strings.HasPrefix(models[0], "local/") {
			edit.Cloud = routerconfig.AppCloud{}
		}
	} else if len(cur.Models) > 0 {
		cp := append([]string(nil), cur.Models...)
		edit.Models = &cp
	}
	_, _ = store.SaveAppContext(ctx, edit)
}

func inspectAutoHosts(ctx context.Context, store *routerconfig.Store) []map[string]any {
	var out []map[string]any
	for _, host := range []string{"loomy", "workbuddy"} {
		root, _ := autoHostConfigRoot(host)
		connected, liveModels := inspectHostLiveFiles(host, root)
		saved := findSavedApp(ctx, store, host)
		models, enabled := liveModels, connected
		if saved != nil {
			if len(saved.Models) > 0 {
				models = append([]string(nil), saved.Models...)
			} else if len(models) == 0 && saved.Cloud.Model != "" {
				models = []string{saved.Cloud.Model}
			}
			if saved.Configured {
				enabled = saved.Enabled || connected
			}
		}
		if models == nil {
			models = []string{}
		}
		primary := ""
		if len(models) > 0 {
			primary = models[0]
		}
		out = append(out, map[string]any{
			"host": host, "enabled": enabled, "connected": connected, "models": models, "model": primary, "configRoot": root,
		})
	}
	return out
}

func inspectHostLiveFiles(host, root string) (bool, []string) {
	if root == "" {
		return false, nil
	}
	if host == "loomy" {
		raw, err := os.ReadFile(filepath.Join(root, "opencode.json"))
		var doc map[string]any
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			return false, nil
		}
		sel, _ := doc["model"].(string)
		primary := strings.TrimPrefix(sel, "api_router/")
		if primary == "" || primary == sel {
			return false, nil
		}
		models := []string{primary}
		if prov, ok := doc["provider"].(map[string]any); ok {
			if ar, ok := prov["api_router"].(map[string]any); ok {
				if mMap, ok := ar["models"].(map[string]any); ok {
					var extra []string
					for k := range mMap {
						if k != primary {
							extra = append(extra, k)
						}
					}
					sort.Strings(extra)
					models = append(models, extra...)
				}
			}
		}
		return true, models
	}
	sRaw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	var sDoc map[string]any
	if err != nil || json.Unmarshal(sRaw, &sDoc) != nil {
		return false, nil
	}
	sel, _ := sDoc["model"].(string)
	primary := strings.TrimPrefix(sel, "custom-local:")
	if primary == "" || primary == sel {
		return false, nil
	}
	models := []string{primary}
	if mRaw, err := os.ReadFile(filepath.Join(root, "models.json")); err == nil {
		var mDoc struct {
			Models []map[string]any `json:"models"`
		}
		if json.Unmarshal(mRaw, &mDoc) == nil {
			for _, item := range mDoc.Models {
				id, _ := item["id"].(string)
				if id != "" && id != primary && item["name"] == "API Router" {
					models = append(models, id)
				}
			}
		}
	}
	return true, models
}
