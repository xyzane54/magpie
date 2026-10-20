package routerconfig

import (
	"errors"
	"math"
	"regexp"
	"strings"
)

var (
	ErrAppNotFound       = errors.New("application is not registered")
	ErrAppConflict       = errors.New("application changed; reload before saving")
	ErrAppUnconfigured   = errors.New("application cloud target is not configured")
	ErrMissingCredential = errors.New("required business or cloud credential is missing")
	ErrCredentialMisuse  = errors.New("management credentials cannot be used upstream")
)

const StoredCredential = "stored"
const ForwardAuthorization = "forward-authorization"

var appID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var builtinApps = map[string]string{"codex": "Codex", "claude-code": "Claude Code", "loomy": "Loomy", "astudio": "astudio", "workbuddy": "WorkBuddy", "dsh": "DSH"}

func ValidAppID(id string) bool {
	if len(id) > 48 || !appID.MatchString(id) {
		return false
	}
	switch id {
	case "api", "app", "v1", "admin", "healthz", "service", "models", "runtime", "providers", "settings", "static":
		return false
	}
	return true
}

// ReferencePrice is an estimate per million tokens, never an upstream tariff.
// nil means unknown; zero is an explicit free price. Version/source travel with
// the request snapshot so later statistics need not reinterpret old usage.
type ReferencePrice struct {
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
	Input    float64 `json:"input"`
	Output   float64 `json:"output"`
	Version  string  `json:"version"`
	Source   string  `json:"source"`
}
type AppCloud struct {
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
	// Capabilities explicitly authorizes enhanced native Responses tools for
	// this exact provider/model binding. It is a declaration, not a probe result.
	Capabilities []string `json:"capabilities"`
}

// AppProfile is independent of Magpie's whole-agent snapshot Profile.
// Builtin, Configured and Path are derived, never persisted.
type AppProfile struct {
	SchemaVersion    int             `json:"schemaVersion"`
	ID               string          `json:"id"`
	Revision         int             `json:"revision"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Models           []string        `json:"models,omitempty"`
	Cloud            AppCloud        `json:"cloud"`
	CredentialMode   string          `json:"credentialMode"`
	LocalRuntime     string          `json:"localRuntime"`
	RedactionEnabled bool            `json:"redactionEnabled"`
	ReferencePrice   *ReferencePrice `json:"referencePrice"`
	Builtin          bool            `json:"builtin"`
	Configured       bool            `json:"configured"`
	Path             string          `json:"path"`
}

type AppEdit struct {
	ExpectedProviderRevision int             `json:"expectedProviderRevision,omitempty"`
	ID                       string          `json:"id"`
	Revision                 int             `json:"revision"`
	Name                     string          `json:"name"`
	Enabled                  *bool           `json:"enabled,omitempty"`
	Models                   *[]string       `json:"models,omitempty"`
	Cloud                    AppCloud        `json:"cloud"`
	CredentialMode           string          `json:"credentialMode"`
	RedactionEnabled         bool            `json:"redactionEnabled"`
	ReferencePrice           *ReferencePrice `json:"referencePrice"`
}

// LocalModelPrefix marks an on-device local model target in AppProfile.Models.
const LocalModelPrefix = "local/"

// parseAppModelTarget parses a single AppProfile.Models entry.
// Returns (prefixOrProviderID, targetIDOrModel, kind, ok), where kind is "group", "local", or "cloud".
func parseAppModelTarget(raw string) (string, string, string, bool) {
	if strings.ContainsAny(raw, " \t\r\n\x00") || len(raw) == 0 || len(raw) > 256 {
		return "", "", "", false
	}
	if gid, ok := GroupIDOf(raw); ok {
		if strings.ContainsAny(gid, " \t\r\n\x00") {
			return "", "", "", false
		}
		return "group", gid, "group", true
	}
	if strings.HasPrefix(raw, LocalModelPrefix) {
		m := strings.TrimPrefix(raw, LocalModelPrefix)
		if m == "" || strings.TrimSpace(m) == "" {
			return "", "", "", false
		}
		return "local", m, "local", true
	}
	idx := strings.IndexByte(raw, '/')
	if idx <= 0 || idx >= len(raw)-1 {
		return "", "", "", false
	}
	provID := raw[:idx]
	modelID := raw[idx+1:]
	if provID == "group" || provID == "local" || strings.TrimSpace(provID) == "" || strings.TrimSpace(modelID) == "" {
		return "", "", "", false
	}
	return provID, modelID, "cloud", true
}

func appModelReferencesProvider(ref, providerID string) bool {
	if providerID == "" || providerID == "group" || providerID == "local" {
		return false
	}
	ref = strings.TrimSpace(ref)
	prefix := providerID + "/"
	return strings.HasPrefix(ref, prefix) && len(strings.TrimSpace(ref[len(prefix):])) > 0
}

func normalizeAppModels(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if strings.ContainsAny(item, "\r\n\x00") {
			return nil, invalidProvider("invalid application model target")
		}
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, _, _, ok := parseAppModelTarget(v); !ok {
			return nil, invalidProvider("invalid application model target: must be group/<id>, local/<model>, or <providerId>/<model>")
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func defaultApp(id, name string) AppProfile {
	mode := StoredCredential
	return AppProfile{SchemaVersion: 1, ID: id, Name: name, CredentialMode: mode, LocalRuntime: "shared", Builtin: builtinApps[id] != "", Path: "/app/" + id + "/v1"}
}

func validateApp(p AppProfile) error {
	if !ValidAppID(p.ID) || p.SchemaVersion != 1 || p.Revision < 1 || strings.TrimSpace(p.Name) == "" || len(p.Name) > 120 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return invalidProvider("invalid application identity or Profile schema")
	}
	if (p.CredentialMode != StoredCredential && !(p.ID == "loomy" && p.CredentialMode == ForwardAuthorization)) || p.LocalRuntime != "shared" {
		return invalidProvider("unsupported application credential mode or local runtime")
	}
	if (p.Cloud.ProviderID == "") != (p.Cloud.Model == "") || len(p.Cloud.Model) > 256 || strings.ContainsAny(p.Cloud.Model, "\r\n\x00") || (p.Cloud.Model != "" && strings.TrimSpace(p.Cloud.Model) == "") {
		return invalidProvider("select both a provider and model, or leave both empty")
	}
	seenModel := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		if _, _, _, ok := parseAppModelTarget(m); !ok || seenModel[m] {
			return invalidProvider("invalid or duplicate application model target")
		}
		seenModel[m] = true
	}
	seen := map[string]bool{}
	for _, capability := range p.Cloud.Capabilities {
		switch capability {
		case "namespace", "custom", "additional_tools", "web_search":
		default:
			return invalidProvider("unknown cloud tool capability")
		}
		if seen[capability] || p.Cloud.ProviderID == "" {
			return invalidProvider("tool capabilities require one configured target and unique names")
		}
		seen[capability] = true
	}
	if v := p.ReferencePrice; v != nil {
		if (v.Currency != "CNY" && v.Currency != "USD" && v.Currency != "EUR") || v.Unit != "million-tokens" || v.Input < 0 || v.Output < 0 || math.IsNaN(v.Input) || math.IsNaN(v.Output) || math.IsInf(v.Input, 0) || math.IsInf(v.Output, 0) || v.Input > 1e9 || v.Output > 1e9 || v.Version == "" || len(v.Version) > 80 || v.Source == "" || len(v.Source) > 120 {
			return invalidProvider("invalid reference price (currency, million-tokens unit, nonnegative input/output, version and source required)")
		}
	}
	return nil
}
