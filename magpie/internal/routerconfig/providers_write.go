package routerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/steady"
	"net/http"
)

func activeProviderCredentialRefs(providers map[string]json.RawMessage, excludeID string) map[string]bool {
	refs := make(map[string]bool, len(providers))
	for id, raw := range providers {
		if id == excludeID {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			continue
		}
		var ref string
		if json.Unmarshal(fields["credentialRef"], &ref) == nil && ref != "" {
			refs[ref] = true
		}
	}
	return refs
}

func pruneOrphanSecrets(f *providerFiles, retainedRefs map[string]bool) {
	for ref := range f.secrets {
		if !retainedRefs[ref] {
			delete(f.secrets, ref)
		}
	}
}

func (s *Store) DeleteCloudProvider(id string, revision int) error {
	return s.DeleteCloudProviderContext(context.Background(), id, revision)
}

func (s *Store) DeleteCloudProviderContext(ctx context.Context, id string, revision int) error {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return err
	}
	p, _, err := f.resolved(id)
	if err != nil {
		return err
	}
	if p.Config.Revision != revision {
		return ErrProviderConflict
	}
	// Do not break a future application Profile that explicitly references this
	// provider. No automatic reassignment or fallback to another key is allowed.
	var apps map[string]json.RawMessage
	if json.Unmarshal(f.document["apps"], &apps) != nil {
		return errors.New("invalid app configuration")
	}
	for _, raw := range apps {
		var app struct {
			ProviderID string `json:"providerId"`
			Cloud      struct {
				ProviderID string `json:"providerId"`
			} `json:"cloud"`
			Models []string `json:"models"`
		}
		if json.Unmarshal(raw, &app) != nil {
			return errors.New("invalid app configuration")
		}
		if app.ProviderID == id || app.Cloud.ProviderID == id {
			return invalidProvider("provider is referenced by an application")
		}
		for _, m := range app.Models {
			if appModelReferencesProvider(m, id) {
				return invalidProvider("provider is referenced by an application")
			}
		}
	}
	groups, err := readGroupsFromDoc(f.document)
	if err != nil {
		return errors.New("invalid group configuration")
	}
	for _, g := range groups {
		if g.Hidden || g.Disabled {
			continue
		}
		for _, member := range g.Members {
			if appModelReferencesProvider(member, id) {
				return invalidProvider("provider is referenced by a routing group")
			}
		}
		for _, rule := range g.Rules {
			if appModelReferencesProvider(rule.Use, id) || appModelReferencesProvider(rule.Target, id) {
				return invalidProvider("provider is referenced by a routing group")
			}
		}
	}
	delete(f.providers, id)
	pruneOrphanSecrets(&f, activeProviderCredentialRefs(f.providers, id))
	return s.writeProviders(ctx, f, true)
}

func (s *Store) writeProviders(ctx context.Context, f providerFiles, credentialsChanged bool) error {
	var err error
	f.document["providers"], err = json.Marshal(f.providers)
	if err != nil {
		return err
	}
	config, err := encode(f.document)
	if err != nil {
		return err
	}
	if credentialsChanged {
		f.credentials["providers"], err = json.Marshal(f.secrets)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(f.credentials, "", "  ")
		if err != nil {
			return err
		}
		if len(f.credentialBytes) > 0 {
			if err := steady.WriteFileContext(ctx, s.layout.CredentialsFile()+".bak", f.credentialBytes, 0o600); err != nil {
				return err
			}
		}
		// Credentials are append-only immutable references. A failed config
		// write leaves an unused reference; the current config and its backup
		// still refer to their original complete credential records.
		if err := steady.WriteFileContext(ctx, s.layout.CredentialsFile(), append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	if err := steady.WriteFileContext(ctx, s.layout.ConfigFile()+".bak", f.configBytes, 0o600); err != nil {
		return err
	}
	return steady.WriteFileContext(ctx, s.layout.ConfigFile(), config, 0o600)
}

// ProviderError is safe to expose: it contains field names, never secret values.
func ProviderError(err error) (string, int) {
	var validation *ProviderValidation
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled", 499
	case errors.Is(err, context.DeadlineExceeded):
		return "configuration operation budget exhausted", http.StatusGatewayTimeout
	case errors.As(err, &validation):
		return validation.Message, http.StatusBadRequest
	case errors.Is(err, ErrProviderNotFound):
		return err.Error(), http.StatusNotFound
	case errors.Is(err, ErrProviderConflict):
		return err.Error(), http.StatusConflict
	default:
		return "provider configuration could not be read or saved", http.StatusServiceUnavailable
	}
}
