package routerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// AppTarget is a fixed, request-local snapshot. Never encode or log secrets.
type AppTarget struct {
	Profile       AppProfile       `json:"-"`
	Provider      ResolvedProvider `json:"-"`
	Authorization string           `json:"-"`
}

// ResolveApp resolves exactly the named Profile and provider under one lock.
// Stored mode ignores inbound credentials and uses only its bound provider key.
// Explicit forward-authorization mode retains Loomy's session Authorization.
// forbidden must include the current management token; no header is copied by default.
func (s *Store) ResolveApp(id, authorization, apiKey string, forbidden ...string) (AppTarget, error) {
	return s.ResolveAppContext(context.Background(), id, authorization, apiKey, forbidden...)
}

func (s *Store) ResolveAppContext(ctx context.Context, id, authorization, apiKey string, forbidden ...string) (AppTarget, error) {
	f, err := s.snapshot(ctx)
	if err != nil {
		return AppTarget{}, err
	}
	apps, err := readApps(f.document)
	if err != nil {
		return AppTarget{}, err
	}
	p, ok := apps[id]
	if !ok {
		return AppTarget{}, ErrAppNotFound
	}
	target, err := resolveAppTarget(f, p, authorization, apiKey, forbidden...)
	if err == nil {
		err = ctx.Err()
	}
	return target, err
}

// ResolveAppDraft resolves a validated editor snapshot without saving or discovering
// models. The current app and provider revisions are checked under the same lock.
func (s *Store) ResolveAppDraft(in AppEdit, managementToken string) (AppTarget, error) {
	return s.ResolveAppDraftContext(context.Background(), in, managementToken)
}

func (s *Store) ResolveAppDraftContext(ctx context.Context, in AppEdit, managementToken string) (AppTarget, error) {
	return s.resolveAppDraftContext(ctx, in, managementToken, RedactionUnavailable)
}

// ResolveAppDraftWithRedactionCapabilityContext validates a read-only editor
// check with server-owned authorization, matching the trusted save path. HTTP
// callers cannot supply capability or opt into an experimental runtime.
func (s *Store) ResolveAppDraftWithRedactionCapabilityContext(ctx context.Context, in AppEdit, managementToken string, capability RedactionCapability) (AppTarget, error) {
	return s.resolveAppDraftContext(ctx, in, managementToken, capability)
}

func (s *Store) resolveAppDraftContext(ctx context.Context, in AppEdit, managementToken string, capability RedactionCapability) (AppTarget, error) {
	f, err := s.snapshot(ctx)
	if err != nil {
		return AppTarget{}, err
	}
	p, err := validatedAppDraft(f, in, capability)
	if err != nil {
		return AppTarget{}, err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return AppTarget{}, err
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return AppTarget{}, err
	}
	if managementToken != "" && containsCredentialString(decoded, func(v string) bool { return strings.Contains(v, managementToken) }) {
		return AppTarget{}, ErrCredentialMisuse
	}
	target, err := resolveAppTarget(f, p, "", "", managementToken)
	if err == nil {
		err = ctx.Err()
	}
	return target, err
}

// ResolveAppProviderTarget resolves a specific cloud provider and model for a profile at runtime.
func (s *Store) ResolveAppProviderTarget(p AppProfile, providerID, modelID, authorization, apiKey string, forbidden ...string) (AppTarget, error) {
	return s.ResolveAppProviderTargetContext(context.Background(), p, providerID, modelID, authorization, apiKey, forbidden...)
}

func (s *Store) ResolveAppProviderTargetContext(ctx context.Context, p AppProfile, providerID, modelID, authorization, apiKey string, forbidden ...string) (AppTarget, error) {
	f, err := s.snapshot(ctx)
	if err != nil {
		return AppTarget{}, err
	}
	p.Configured = true
	p.Cloud.ProviderID = strings.TrimSpace(providerID)
	p.Cloud.Model = strings.TrimSpace(modelID)
	if p.Cloud.ProviderID == "" || p.Cloud.Model == "" {
		return AppTarget{}, ErrAppUnconfigured
	}
	target, err := resolveAppTarget(f, p, authorization, apiKey, forbidden...)
	if err == nil {
		err = ctx.Err()
	}
	return target, err
}

func resolveAppTarget(f providerFiles, p AppProfile, authorization, apiKey string, forbidden ...string) (AppTarget, error) {
	if !p.Configured || (p.Cloud.ProviderID == "" && len(p.Models) == 0) {
		return AppTarget{}, ErrAppUnconfigured
	}
	containsForbidden := func(value string) bool {
		for _, secret := range forbidden {
			if secret != "" && strings.Contains(value, secret) {
				return true
			}
		}
		return false
	}
	if containsForbidden(authorization) || containsForbidden(apiKey) {
		return AppTarget{}, ErrCredentialMisuse
	}
	if p.Cloud.ProviderID == "" {
		if p.CredentialMode == ForwardAuthorization {
			if !validBearer(authorization) {
				return AppTarget{}, ErrMissingCredential
			}
		} else {
			authorization = ""
		}
		return AppTarget{Profile: p, Authorization: authorization}, nil
	}
	upstream, _, err := f.resolved(p.Cloud.ProviderID)
	if err != nil {
		return AppTarget{}, err
	}
	// Header auth can never replace the credential chosen by this Profile.
	for name := range upstream.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "x-api-key") {
			delete(upstream.Headers, name)
		}
	}
	if p.CredentialMode == ForwardAuthorization {
		if !validBearer(authorization) {
			return AppTarget{}, ErrMissingCredential
		}
		upstream.APIKey = ""
	} else {
		if upstream.APIKey == "" {
			return AppTarget{}, ErrMissingCredential
		}
		authorization = ""
	}
	private, err := json.Marshal([]any{upstream.APIKey, upstream.Headers, upstream.ExtraBody, upstream.ProxyURL, authorization})
	if err != nil {
		return AppTarget{}, errors.New("invalid provider private options")
	}
	// RawMessage retains JSON escapes. Inspect decoded strings, including
	// nested options, without rewriting the target or converting numbers to float64.
	decoder := json.NewDecoder(bytes.NewReader(private))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return AppTarget{}, errors.New("invalid provider private options")
	}
	if containsCredentialString(normalized, containsForbidden) {
		return AppTarget{}, ErrCredentialMisuse
	}
	upstream.Config.Model = p.Cloud.Model
	return AppTarget{p, upstream, authorization}, nil
}

// containsCredentialString checks JSON string values and member names after
// decoding. Escape-looking text inside a string is not decoded a second time.
func containsCredentialString(value any, forbidden func(string) bool) bool {
	switch value := value.(type) {
	case string:
		return forbidden(value)
	case []any:
		for _, item := range value {
			if containsCredentialString(item, forbidden) {
				return true
			}
		}
	case map[string]any:
		for name, item := range value {
			if forbidden(name) || containsCredentialString(item, forbidden) {
				return true
			}
		}
	}
	return false
}

func validBearer(value string) bool {
	return strings.HasPrefix(value, "Bearer ") && len(value) > 7 && len(value) <= 16391 && !strings.ContainsAny(value[7:], " \t\r\n\x00")
}
