package routerservice

import (
	_ "embed"
	"encoding/json"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routercontrol"
	"io"
	"net/http"
	"sync"
)

type serviceController struct {
	layout     dirs.Layout
	stop       func()
	mu         sync.Mutex
	restarting bool
}

func serviceActionPath(r *http.Request) bool {
	switch r.URL.EscapedPath() {
	case "/api/service/stop", "/api/service/restart", "/api/service/autostart":
		return true
	}
	return false
}
func serviceControlAPI(w http.ResponseWriter, r *http.Request, info Info, control *serviceController) {
	if control == nil {
		providerJSON(w, 409, map[string]string{"error": "service_control_unavailable", "message": "Use the standalone CLI to manage this service."})
		return
	}
	if expected := r.Header.Get("X-API-Router-Instance"); expected != "" && expected != info.ProcessStamp {
		providerJSON(w, 409, map[string]string{"error": "service_instance_changed"})
		return
	}
	switch r.URL.Path {
	case "/api/service/autostart":
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.Enabled == nil || decoder.Decode(&struct{}{}) != io.EOF {
			providerJSON(w, 400, map[string]string{"error": "invalid_autostart_choice", "message": "Choose enabled: true or false."})
			return
		}
		if err := routercontrol.SaveOptions(control.layout, info.ExperimentalRedaction); err != nil {
			providerJSON(w, 409, map[string]string{"error": "service_options_unavailable", "message": err.Error()})
			return
		}
		state, err := routercontrol.SetAutostart(control.layout, info.Executable, *body.Enabled)
		if err != nil {
			providerJSON(w, 409, map[string]string{"error": "autostart_unavailable", "message": err.Error()})
			return
		}
		info.Autostart = state
		providerJSON(w, 200, info)
	case "/api/service/stop":
		if err := routercontrol.SaveOptions(control.layout, info.ExperimentalRedaction); err != nil {
			providerJSON(w, 409, map[string]string{"error": "service_options_unavailable", "message": err.Error()})
			return
		}
		providerJSON(w, 202, map[string]any{"stopping": true, "startCommand": info.StartCommand})
		control.stop()
	case "/api/service/restart":
		control.mu.Lock()
		defer control.mu.Unlock()
		if control.restarting {
			providerJSON(w, 409, map[string]string{"error": "restart_in_progress"})
			return
		}
		done, err := routercontrol.SpawnRestart(control.layout)
		if err != nil {
			providerJSON(w, 409, map[string]string{"error": "restart_failed", "message": err.Error()})
			return
		}
		control.restarting = true
		go func() { <-done; control.mu.Lock(); control.restarting = false; control.mu.Unlock() }()
		providerJSON(w, 202, map[string]any{"restarting": true, "startCommand": info.StartCommand})
	}
}

//go:embed assets/service-control.js
var serviceControlScript []byte
