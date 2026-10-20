package routerservice

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
)

//go:embed assets/app.js
var script []byte

//go:embed assets/tutorial.js
var tutorialScript []byte

//go:embed assets/router.css
var routerCSS []byte

//go:embed assets/provider.js
var providerScript []byte

//go:embed assets/profiles.js
var profilesScript []byte

//go:embed assets/observations.js
var observationsScript []byte

//go:embed assets/loomy.js
var loomyScript []byte

//go:embed assets/routing-groups.js
var routingGroupsScript []byte

//go:embed assets/routing-groups-editor.js
var routingGroupsEditorScript []byte

func appFailure(w http.ResponseWriter, err error) {
	e := routerrequest.ClassifyError(err)
	providerJSON(w, e.Status, map[string]any{"error": e, "requestId": w.Header().Get("X-Request-ID")})
}

func appsAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, token string, redactionCapability ...routerconfig.RedactionCapability) {
	if store == nil {
		appFailure(w, errors.New("store unavailable"))
		return
	}
	switch r.Method {
	case "GET", "HEAD":
		apps, err := store.AppsContext(r.Context())
		if err != nil {
			appFailure(w, err)
			return
		}
		if r.Method == "HEAD" {
			return
		}
		capability := routerconfig.RedactionUnavailable
		if len(redactionCapability) > 0 {
			capability = redactionCapability[0]
		}
		providerJSON(w, 200, map[string]any{"schemaVersion": 1, "apps": apps, "capabilities": map[string]any{"inference": true, "connectionTest": true, "redaction": capability == routerconfig.RedactionAvailable || capability == routerconfig.RedactionExperimental, "redactionState": capability}})
	case "POST", "PUT":
		var in routerconfig.AppEdit
		if !providerInput(w, r, &in) {
			return
		}
		// Compare normalized input, including price metadata, without echoing
		// any accidentally pasted management credential.
		normalized, _ := json.Marshal(in)
		if token != "" && strings.Contains(string(normalized), token) {
			appFailure(w, routerconfig.ErrCredentialMisuse)
			return
		}
		if in.Name == "" || in.CredentialMode == "" {
			if apps, err := store.AppsContext(r.Context()); err == nil {
				for _, a := range apps {
					if a.ID == strings.TrimSpace(in.ID) {
						if in.Name == "" {
							in.Name = a.Name
						}
						if in.CredentialMode == "" {
							in.CredentialMode = a.CredentialMode
						}
						break
					}
				}
			}
		}
		capability := routerconfig.RedactionUnavailable
		if len(redactionCapability) > 0 {
			capability = redactionCapability[0]
		}
		saved, err := store.SaveAppWithRedactionCapabilityContext(r.Context(), in, capability)
		if err != nil {
			appFailure(w, err)
			return
		}
		providerJSON(w, 200, map[string]any{"app": saved})
	case "DELETE":
		var in struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		}
		if !providerInput(w, r, &in) {
			return
		}
		if err := store.DeleteAppContext(r.Context(), in.ID, in.Revision); err != nil {
			appFailure(w, err)
			return
		}
		providerJSON(w, 200, map[string]bool{"deleted": true})
	}
}
