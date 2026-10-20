package routerconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/steady"
	"net/url"
	"os"
	"sort"
	"strings"
)

var (
	ErrProviderNotFound = errors.New("provider not found")
	ErrProviderConflict = errors.New("provider changed; reload before saving")
)

type ProviderValidation struct{ Message string }

func (e *ProviderValidation) Error() string { return e.Message }
func invalidProvider(s string) error        { return &ProviderValidation{s} }

// CloudProvider is the public configuration. Sensitive options have an immutable
// provider-scoped reference in config.json, and are never returned by management.
// Revision prevents a stale editor from replacing a newer credential/config.
type CloudProvider struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	Preset   string `json:"preset,omitempty"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseURL"`
	// Model is an ephemeral compatibility projection from an application Profile.
	// Providers neither persist nor expose a default model.
	Model            string   `json:"-"`
	DiscoveredModels []string `json:"discoveredModels,omitempty"`
	ProxyMode        string   `json:"proxyMode"`
	HasKey           bool     `json:"hasKey"`
	HeaderNames      []string `json:"headerNames"`
	HasExtraBody     bool     `json:"hasExtraBody"`
	HasProxyURL      bool     `json:"hasProxyURL"`
}

type ProviderEdit struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	Preset   string `json:"preset,omitempty"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseURL"`
	// Model is accepted only for old management clients and ignored. Model
	// selection belongs exclusively to AppEdit.Cloud.Model.
	Model            string    `json:"model,omitempty"`
	DiscoveredModels *[]string `json:"discoveredModels,omitempty"`
	// Omitted, null or blank APIKey/ProxyURL keep the saved secret. An explicit
	// empty Headers/ExtraBody object clears that section; omission keeps it.
	APIKey    *string                     `json:"apiKey,omitempty"`
	ClearKey  bool                        `json:"clearKey,omitempty"`
	Headers   *map[string]string          `json:"headers,omitempty"`
	ExtraBody *map[string]json.RawMessage `json:"extraBody,omitempty"`
	ProxyMode string                      `json:"proxyMode"`
	ProxyURL  *string                     `json:"proxyURL,omitempty"`
}

type providerSecret struct {
	ProviderID string                     `json:"providerId"`
	APIKey     string                     `json:"apiKey"`
	Headers    map[string]string          `json:"headers"`
	ExtraBody  map[string]json.RawMessage `json:"extraBody"`
	ProxyURL   string                     `json:"proxyURL,omitempty"`
}

// ResolvedProvider is only for an explicitly requested upstream operation. It
// must not be JSON encoded, logged, or used as a cross-app credential fallback.
type ResolvedProvider struct {
	Config    CloudProvider              `json:"-"`
	APIKey    string                     `json:"-"`
	Headers   map[string]string          `json:"-"`
	ExtraBody map[string]json.RawMessage `json:"-"`
	ProxyURL  string                     `json:"-"`
}

type providerFiles struct {
	document                     Document
	providers                    map[string]json.RawMessage
	credentials                  Document
	secrets                      map[string]json.RawMessage
	configBytes, credentialBytes []byte
}

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// Read both files strictly. A missing credential store is only an empty store
// while config has no provider references. Corrupt/unreadable files never mean empty.
func (s *Store) providerFiles() (providerFiles, error) {
	return s.snapshot(context.Background())
}

func (s *Store) providerFilesContext(ctx context.Context) (providerFiles, error) {
	var f providerFiles
	var err error
	if err = s.regularFile(); err != nil {
		return f, err
	}
	f.configBytes, err = steady.ReadFileContext(ctx, s.layout.ConfigFile())
	if err != nil {
		return f, err
	}
	f.document, err = parse(f.configBytes)
	if err != nil {
		return f, err
	}
	if err = json.Unmarshal(f.document["providers"], &f.providers); err != nil || f.providers == nil {
		return f, errors.New("invalid provider configuration")
	}
	f.credentials = Document{"schemaVersion": json.RawMessage("1")}
	f.secrets = map[string]json.RawMessage{}
	needsCredentials := len(f.providers) > 0
	fi, err := os.Lstat(s.layout.CredentialsFile())
	if errors.Is(err, os.ErrNotExist) && !needsCredentials {
		return f, nil
	}
	if err != nil {
		return f, errors.New("cannot read provider credentials")
	}
	if !fi.Mode().IsRegular() {
		return f, errors.New("provider credentials must be a regular file")
	}
	f.credentialBytes, err = steady.ReadFileContext(ctx, s.layout.CredentialsFile())
	if err != nil {
		if ctx.Err() != nil {
			return f, ctx.Err()
		}
		return f, errors.New("cannot read provider credentials")
	}
	var version int
	if json.Unmarshal(f.credentialBytes, &f.credentials) != nil || json.Unmarshal(f.credentials["schemaVersion"], &version) != nil || version != SchemaVersion || json.Unmarshal(f.credentials["providers"], &f.secrets) != nil || f.secrets == nil {
		return f, errors.New("invalid provider credential store")
	}
	return f, nil
}

func (f providerFiles) resolved(id string) (ResolvedProvider, map[string]json.RawMessage, error) {
	var p CloudProvider
	var fields map[string]json.RawMessage
	raw, ok := f.providers[id]
	if !ok {
		return ResolvedProvider{}, nil, ErrProviderNotFound
	}
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &p) != nil || p.ID != id || p.Revision < 1 {
		return ResolvedProvider{}, nil, errors.New("invalid provider configuration")
	}
	var ref string
	if json.Unmarshal(fields["credentialRef"], &ref) != nil || ref == "" {
		return ResolvedProvider{}, nil, errors.New("missing provider credential reference")
	}
	var secret providerSecret
	if json.Unmarshal(f.secrets[ref], &secret) != nil || secret.ProviderID != id {
		return ResolvedProvider{}, nil, errors.New("missing or mismatched provider credentials")
	}
	p.HasKey = secret.APIKey != ""
	p.HasExtraBody = len(secret.ExtraBody) > 0
	p.HasProxyURL = secret.ProxyURL != ""
	p.HeaderNames = []string{}
	for name := range secret.Headers {
		p.HeaderNames = append(p.HeaderNames, name)
	}
	sort.Strings(p.HeaderNames)
	return ResolvedProvider{p, secret.APIKey, secret.Headers, secret.ExtraBody, secret.ProxyURL}, fields, nil
}

func (s *Store) CloudProviders() ([]CloudProvider, error) {
	return s.CloudProvidersContext(context.Background())
}

func (s *Store) CloudProvidersContext(ctx context.Context) ([]CloudProvider, error) {
	f, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	providers := []CloudProvider{}
	for id := range f.providers {
		p, _, err := f.resolved(id)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p.Config)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers, ctx.Err()
}

func validBase(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "?#") && u.Opaque == ""
}

func normalizeDiscoveredModels(raw []string) ([]string, error) {
	var out []string
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		if strings.ContainsAny(item, "\r\n\x00") {
			return nil, invalidProvider("invalid discovered model ID")
		}
		m := strings.TrimSpace(item)
		if m == "" {
			continue
		}
		if len(m) > 256 {
			return nil, invalidProvider("invalid discovered model ID")
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) > 1000 {
		return nil, invalidProvider("too many discovered models")
	}
	return out, nil
}
