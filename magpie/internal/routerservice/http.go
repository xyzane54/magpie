package routerservice

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"

	clientassets "github.com/yetone/magpie/internal/gui/assets"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routercontrol"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerredact"
)

//go:embed assets/index.html
var index []byte

//go:embed assets/favicon.svg
var favicon []byte

//go:embed assets/provider-presets.json
var providerPresets []byte

// Metadata is a generated, passive snapshot of the original API-key presets.
// Sharing its icons never imports provider/account/catalog startup code.
func init() {
	var presets []struct {
		Icon string `json:"icon"`
	}
	if err := json.Unmarshal(providerPresets, &presets); err != nil {
		panic(err)
	}
	for _, preset := range presets {
		if preset.Icon != "" {
			visualAssets["/icons/"+preset.Icon+".svg"] = "image/svg+xml"
		}
	}
	if entries, err := clientassets.Router.ReadDir("icons"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".svg") {
				visualAssets["/icons/"+entry.Name()] = "image/svg+xml"
			}
		}
	}
}

// The original visual resources are shared directly, without copying styles
// or importing the GUI backend. Only these named resources can be served.
var visualAssets = map[string]string{
	"/plugins/routing/routing.css": "text/css; charset=utf-8",
	"/app.css":                     "text/css; charset=utf-8",
	"/context.css":                 "text/css; charset=utf-8",
	"/i18n.js":                     "text/javascript; charset=utf-8",
	"/icons/codex-color.svg":       "image/svg+xml",
	"/icons/claudecode-color.svg":  "image/svg+xml",
	"/icons/loomy.png":             "image/png",
	"/icons/workbuddy-color.svg":   "image/svg+xml",
	"/icons/spark-color.svg":       "image/svg+xml",
}

// This is the complete I0 allow-list. There is no fallback to gui.Handler or
// gateway.Handler: future model/management routes must be registered explicitly.
func handler(info Info, token string, stores ...*routerconfig.Store) http.Handler {
	var store *routerconfig.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	return handlerWithModelOptions(info, token, store, ModelOptions{})
}

func handlerWithModelOptions(info Info, token string, store *routerconfig.Store, options ModelOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || r.Host != info.Address || !sameOrigin(r, info.Address) {
			http.Error(w, "local same-origin access required", http.StatusForbidden)
			return
		}
		if r.URL.EscapedPath() == "/api/management/session" {
			managementSession(w, r, info.Address, token)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app/") || strings.HasPrefix(r.URL.EscapedPath(), "/app/") {
			appModelAPI(w, r, store, token, options)
			return
		}
		switch r.URL.EscapedPath() {
		case "/", "/icons/favicon.svg", "/app.js", "/provider.js", "/profiles.js", "/observations.js", "/loomy.js", "/local-model.js", "/redaction.js", "/service-control.js", "/tutorial.js", "/routing-groups.js", "/routing-groups-editor.js", "/provider-presets.json", "/router.css", "/healthz":
		default:
			if _, ok := visualAssets[r.URL.EscapedPath()]; !ok && !allowedAPIPath(r.URL.EscapedPath()) && !requestAPIPath(r) && !localModelPath(r) {
				http.NotFound(w, r)
				return
			}
		}
		isApp := r.URL.Path == "/api/apps" || r.URL.Path == "/api/apps/check"
		isLoomy, isHostAuto := r.URL.Path == "/api/hosts/loomy", r.URL.Path == "/api/hosts/auto"
		isRequest, isLocal, isControl := requestAPIPath(r), localModelPath(r), serviceActionPath(r)
		isProvider := r.URL.Path == "/api/cloud-providers" || r.URL.Path == "/api/cloud-providers/models"
		isGroup := r.URL.Path == "/api/routing-groups" || r.URL.Path == "/api/routing-groups/switch"
		isRedaction := r.URL.Path == "/api/redaction"
		allowed, methods := r.Method == http.MethodGet || r.Method == http.MethodHead, "GET, HEAD"
		if r.URL.Path == "/api/cloud-providers" || r.URL.Path == "/api/routing-groups" || isApp {
			allowed = allowed || r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete
			methods = "GET, HEAD, POST, PUT, DELETE"
		}
		if r.URL.Path == "/api/cloud-providers/models" || r.URL.Path == "/api/routing-groups/switch" || r.URL.Path == "/api/apps/check" || isLoomy || (isLocal && !localModelRead(r)) || isControl {
			allowed, methods = r.Method == http.MethodPost, "POST"
		}
		if r.URL.Path == "/api/local-model/settings" || isRedaction || isHostAuto {
			allowed = r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodPost
			methods = "GET, HEAD, POST"
		}
		if !allowed {
			w.Header().Set("Allow", methods)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if (r.URL.Path == "/api/service" || isControl || isProvider || isGroup || isApp || isRequest || isLoomy || isHostAuto || isLocal || isRedaction) && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "management token required", http.StatusUnauthorized)
			return
		}
		if isControl {
			serviceControlAPI(w, r, info, options.serviceControl)
			return
		}
		if isLoomy {
			loomyHostAPI(w, r, info, token, options.HostStateDir)
			return
		}
		if isHostAuto {
			hostAutoAPI(w, r, info, token, store, options.HostStateDir)
			return
		}
		if isRedaction {
			var stats func() routerredact.Stats
			var state func() redactionRuntimeState
			if options.Routing != nil {
				if production, ok := options.Routing.Redactor.(*runtimeRedactor); ok {
					stats, state = production.Stats, production.state
				}
			}
			redactionAPI(w, r, store, stats, state)
			return
		}
		if r.URL.Path == "/api/apps/check" {
			appCheckAPI(w, r, store, token, options)
			return
		}
		if isLocal {
			r = r.WithContext(routerobserve.WithLocalObserver(r.Context(), options.LocalCalls, "", true, nil))
			localModelAPI(w, r, options.LocalRuntime)
			return
		}
		if isRequest {
			requestsAPI(w, r, options.Ledger, options.LedgerErrorCode)
			return
		}
		if isApp {
			capability := routerconfig.RedactionUnavailable
			if options.Routing != nil {
				if production, ok := options.Routing.Redactor.(*runtimeRedactor); ok {
					state := production.state()
					capability = state.Capability
					if !state.RecognitionReady && !state.TrialReady {
						capability = routerconfig.RedactionUnknown
					}
				}
			}
			appsAPI(w, r, store, token, capability)
			return
		}
		if isProvider {
			providerAPI(w, r, store, token, options.LocalRuntime)
			return
		}
		if contentType, ok := visualAssets[r.URL.Path]; ok {
			b, err := clientassets.Router.ReadFile(r.URL.Path[1:])
			if err != nil {
				http.Error(w, "client resource unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", contentType)
			if r.Method != http.MethodHead {
				_, _ = w.Write(b)
			}
			return
		}
		writeAsset := func(contentType string, data []byte) {
			w.Header().Set("Content-Type", contentType)
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
		}
		jsAssets := map[string][]byte{
			"/app.js": script, "/service-control.js": serviceControlScript, "/tutorial.js": tutorialScript,
			"/profiles.js": profilesScript, "/loomy.js": loomyScript, "/observations.js": observationsScript,
			"/provider.js": providerScript, "/local-model.js": localModelScript, "/redaction.js": redactionScript,
			"/routing-groups.js": routingGroupsScript, "/routing-groups-editor.js": routingGroupsEditorScript,
		}
		switch r.URL.Path {
		case "/api/routing-groups", "/api/routing-groups/switch":
			routingGroupsAPI(w, r, store, options.LocalRuntime)
		case "/icons/favicon.svg":
			writeAsset("image/svg+xml", favicon)
		case "/":
			writeAsset("text/html; charset=utf-8", index)
		case "/app.js", "/service-control.js", "/tutorial.js", "/profiles.js", "/loomy.js", "/observations.js", "/provider.js", "/local-model.js", "/redaction.js", "/routing-groups.js", "/routing-groups-editor.js":
			writeAsset("text/javascript; charset=utf-8", jsAssets[r.URL.Path])
		case "/provider-presets.json":
			writeAsset("application/json", providerPresets)
		case "/router.css":
			writeAsset("text/css; charset=utf-8", routerCSS)
		default:
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodHead {
				return
			}
			if r.URL.Path == "/healthz" {
				_ = json.NewEncoder(w).Encode(map[string]any{"service": "api-router", "status": "ok", "pid": info.PID, "startedAt": info.StartedAt})
			} else {
				current := info
				if options.serviceControl != nil {
					var err error
					current.Autostart, err = routercontrol.AutostartStatus(options.serviceControl.layout, info.Executable)
					if err != nil {
						current.Autostart.Error = err.Error()
					}
				}
				if options.LocalRuntime != nil {
					status := options.LocalRuntime.Manager.Status()
					current.Runtime = status.Healthy && status.Intent.Enabled && status.Runtime != "unloading"
				}
				_ = json.NewEncoder(w).Encode(current)
			}
		}
	})
}

func allowedAPIPath(path string) bool {
	switch path {
	case "/api/service", "/api/cloud-providers", "/api/cloud-providers/models", "/api/routing-groups", "/api/routing-groups/switch", "/api/apps", "/api/apps/check", "/api/hosts/loomy", "/api/hosts/auto", "/api/redaction", "/api/service/stop", "/api/service/restart", "/api/service/autostart":
		return true
	}
	return false
}

func sameOrigin(r *http.Request, address string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	for _, origin := range r.Header.Values("Origin") {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host != address || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}
