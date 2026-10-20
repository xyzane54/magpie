package routerservice

import (
	"encoding/json"
	"net/http"
)

// managementSession lets only the Router's own browser page obtain the current
// process token. Browsers cannot send the custom header from another origin
// without a CORS preflight, and this server never grants CORS access. Requiring
// exact Origin and Fetch Metadata also rejects navigation and form requests.
// CLI clients continue to authenticate using the private token file.
func managementSession(w http.ResponseWriter, r *http.Request, address, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != "http://"+address ||
		r.Header.Get("Sec-Fetch-Site") != "same-origin" ||
		r.Header.Get("X-API-Router-UI") != "1" || token == "" {
		http.Error(w, "local management page required", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "token": token})
}
