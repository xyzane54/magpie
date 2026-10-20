package routerconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

func resolveEdit(f providerFiles, in ProviderEdit) (ResolvedProvider, map[string]json.RawMessage, error) {
	p := ResolvedProvider{Headers: map[string]string{}, ExtraBody: map[string]json.RawMessage{}}
	fields := map[string]json.RawMessage{}
	var err error
	if in.ID != "" {
		p, fields, err = f.resolved(in.ID)
		if err != nil {
			return p, nil, err
		}
		if p.Config.Revision != in.Revision {
			return p, nil, ErrProviderConflict
		}
	}
	discoveredModels := p.Config.DiscoveredModels
	if in.DiscoveredModels != nil {
		discoveredModels, err = normalizeDiscoveredModels(*in.DiscoveredModels)
		if err != nil {
			return p, nil, err
		}
	}
	if len(in.Name) > 120 || strings.TrimSpace(in.Name) == "" {
		return p, nil, invalidProvider("provider name is required (up to 120 bytes)")
	}
	if len(in.Preset) > 128 {
		return p, nil, invalidProvider("invalid provider preset ID")
	}
	if in.Protocol != "chat" && in.Protocol != "responses" && in.Protocol != "anthropic" {
		return p, nil, invalidProvider("unsupported provider protocol")
	}
	if !validBase(in.BaseURL) || len(in.BaseURL) > 2048 {
		return p, nil, invalidProvider("API base must be an HTTP(S) URL without credentials, query or fragment")
	}
	if in.ProxyMode == "" {
		in.ProxyMode = "system"
	}
	if in.ProxyMode != "system" && in.ProxyMode != "direct" && in.ProxyMode != "custom" {
		return p, nil, invalidProvider("unsupported proxy mode")
	}
	p.Config = CloudProvider{ID: in.ID, Revision: in.Revision, Name: strings.TrimSpace(in.Name), Preset: in.Preset, Protocol: in.Protocol, BaseURL: strings.TrimRight(in.BaseURL, "/"), DiscoveredModels: discoveredModels, ProxyMode: in.ProxyMode}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		p.APIKey = strings.TrimSpace(*in.APIKey)
	}
	if in.ClearKey {
		p.APIKey = ""
	}
	if len(p.APIKey) > 16384 || strings.ContainsAny(p.APIKey, "\r\n\x00") {
		return p, nil, invalidProvider("invalid API key")
	}
	if in.Headers != nil {
		p.Headers = *in.Headers
	}
	cleaned := map[string]string{}
	for key, value := range p.Headers {
		name := http.CanonicalHeaderKey(key)
		if name == "" || strings.IndexFunc(key, func(r rune) bool {
			return r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("!#$%&'*+-.^_`|~", r))
		}) >= 0 || strings.ContainsAny(value, "\r\n\x00") {
			return p, nil, invalidProvider("invalid custom request header")
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-connection", "cookie", "set-cookie", "trailer", "te":
			return p, nil, invalidProvider("transport and browser headers cannot be overridden")
		}
		if _, exists := cleaned[name]; exists {
			return p, nil, invalidProvider("duplicate custom request header")
		}
		cleaned[name] = value
	}
	p.Headers = cleaned
	if in.ExtraBody != nil {
		p.ExtraBody = *in.ExtraBody
	}
	for key, value := range p.ExtraBody {
		switch strings.TrimPrefix(key, "!") {
		case "model", "stream", "messages", "input", "contents", "system", "systemInstruction", "system_instruction", "instructions", "tools", "previous_response_id":
			return p, nil, invalidProvider("extra body cannot override model, stream, conversation or tools")
		}
		if !json.Valid(value) {
			return p, nil, invalidProvider("extra body must be a JSON object")
		}
	}
	options, err := json.Marshal([]any{p.Headers, p.ExtraBody})
	if err != nil || len(options) > 64<<10 {
		return p, nil, invalidProvider("advanced options are too large")
	}
	if in.ProxyURL != nil && strings.TrimSpace(*in.ProxyURL) != "" {
		p.ProxyURL = strings.TrimSpace(*in.ProxyURL)
	}
	if p.Config.ProxyMode == "custom" {
		u, err := url.Parse(p.ProxyURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return p, nil, invalidProvider("custom proxy requires an HTTP(S) or SOCKS5 URL")
		}
	}
	return p, fields, nil
}

// ResolveCloudProvider merges a draft with the same provider's saved secrets.
// It is read-only and used solely by the explicit model-list operation.
func (s *Store) ResolveCloudProvider(in ProviderEdit) (ResolvedProvider, error) {
	return s.ResolveCloudProviderContext(context.Background(), in)
}

func (s *Store) ResolveCloudProviderContext(ctx context.Context, in ProviderEdit) (ResolvedProvider, error) {
	f, err := s.snapshot(ctx)
	if err != nil {
		return ResolvedProvider{}, err
	}
	p, _, err := resolveEdit(f, in)
	if err == nil {
		err = ctx.Err()
	}
	return p, err
}

func (s *Store) SaveCloudProvider(in ProviderEdit) (CloudProvider, error) {
	return s.SaveCloudProviderContext(context.Background(), in)
}

func (s *Store) SaveCloudProviderContext(ctx context.Context, in ProviderEdit) (CloudProvider, error) {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return CloudProvider{}, err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return CloudProvider{}, err
	}
	p, fields, err := resolveEdit(f, in)
	if err != nil {
		return CloudProvider{}, err
	}
	if p.Config.ID == "" {
		p.Config.ID, err = randomID("p_")
		if err != nil {
			return CloudProvider{}, err
		}
	}
	p.Config.Revision++
	ref, err := randomID("cred_")
	if err != nil {
		return CloudProvider{}, err
	}
	secret := providerSecret{p.Config.ID, p.APIKey, p.Headers, p.ExtraBody, p.ProxyURL}
	secretFields := map[string]json.RawMessage{}
	var previousRef string
	if json.Unmarshal(fields["credentialRef"], &previousRef) == nil && previousRef != "" {
		if err := json.Unmarshal(f.secrets[previousRef], &secretFields); err != nil {
			return CloudProvider{}, err
		}
	}
	known, err := json.Marshal(secret)
	if err != nil {
		return CloudProvider{}, err
	}
	var secretUpdates map[string]json.RawMessage
	if err := json.Unmarshal(known, &secretUpdates); err != nil {
		return CloudProvider{}, err
	}
	for key, value := range secretUpdates {
		secretFields[key] = value
	}
	f.secrets[ref], err = json.Marshal(secretFields)
	if err != nil {
		return CloudProvider{}, err
	}
	b, _ := json.Marshal(p.Config)
	var current map[string]json.RawMessage
	_ = json.Unmarshal(b, &current)
	for key, value := range current {
		fields[key] = value
	}
	if len(p.Config.DiscoveredModels) == 0 {
		delete(fields, "discoveredModels")
	}
	// Derived status and the retired provider model are never persisted.
	// Unknown future config fields are kept; app model selections are untouched.
	for _, key := range []string{"hasKey", "headerNames", "hasExtraBody", "hasProxyURL", "model"} {
		delete(fields, key)
	}
	fields["credentialRef"], _ = json.Marshal(ref)
	retainedRefs := activeProviderCredentialRefs(f.providers, "")
	retainedRefs[ref] = true
	f.providers[p.Config.ID], err = json.Marshal(fields)
	if err != nil {
		return CloudProvider{}, err
	}
	pruneOrphanSecrets(&f, retainedRefs)
	if err := s.writeProviders(ctx, f, true); err != nil {
		return CloudProvider{}, err
	}
	resolved, _, err := f.resolved(p.Config.ID)
	return resolved.Config, err
}

func (s *Store) SaveProviderDiscoveredModels(id string, models []string) (CloudProvider, error) {
	return s.SaveProviderDiscoveredModelsContext(context.Background(), id, models)
}

func (s *Store) SaveProviderDiscoveredModelsContext(ctx context.Context, id string, models []string) (CloudProvider, error) {
	if strings.TrimSpace(id) == "" {
		return CloudProvider{}, ErrProviderNotFound
	}
	normalized, err := normalizeDiscoveredModels(models)
	if err != nil {
		return CloudProvider{}, err
	}
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return CloudProvider{}, err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return CloudProvider{}, err
	}
	p, fields, err := f.resolved(id)
	if err != nil {
		return CloudProvider{}, err
	}
	if len(normalized) == 0 {
		delete(fields, "discoveredModels")
	} else {
		fields["discoveredModels"], err = json.Marshal(normalized)
		if err != nil {
			return CloudProvider{}, err
		}
	}
	f.providers[p.Config.ID], err = json.Marshal(fields)
	if err != nil {
		return CloudProvider{}, err
	}
	if err := s.writeProviders(ctx, f, false); err != nil {
		return CloudProvider{}, err
	}
	resolved, _, err := f.resolved(p.Config.ID)
	return resolved.Config, err
}
