package routerconfig

import (
	"context"
	"encoding/json"
	"strings"
)

// validatedAppDraft validates the same edit for saves and read-only connection checks.
func validatedAppDraft(f providerFiles, in AppEdit, redactionCapability RedactionCapability) (AppProfile, error) {
	apps, err := readApps(f.document)
	if err != nil {
		return AppProfile{}, err
	}
	old, exists := apps[in.ID]
	if !exists {
		if in.Revision != 0 {
			return AppProfile{}, ErrAppNotFound
		}
		old = defaultApp(in.ID, in.Name)
	}
	if in.Revision != old.Revision {
		return AppProfile{}, ErrAppConflict
	}
	p := old
	p.Revision, err = nextAppRevision(f.document, old.Revision)
	if err != nil {
		return AppProfile{}, err
	}
	p.Name = strings.TrimSpace(in.Name)
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}

	if in.Models != nil {
		normModels, err := normalizeAppModels(*in.Models)
		if err != nil {
			return AppProfile{}, err
		}
		p.Models = normModels
		if len(normModels) > 0 {
			provID, modelID, kind, _ := parseAppModelTarget(normModels[0])
			if kind == "cloud" {
				if in.Cloud.ProviderID == "" && in.Cloud.Model == "" {
					p.Cloud = AppCloud{
						ProviderID:   provID,
						Model:        modelID,
						Capabilities: in.Cloud.Capabilities,
					}
					if p.Cloud.Capabilities == nil && p.Cloud.ProviderID == old.Cloud.ProviderID && p.Cloud.Model == old.Cloud.Model {
						p.Cloud.Capabilities = append([]string(nil), old.Cloud.Capabilities...)
					}
				} else {
					p.Cloud = in.Cloud
					if in.Cloud.Capabilities == nil && in.Cloud.ProviderID == old.Cloud.ProviderID && in.Cloud.Model == old.Cloud.Model {
						p.Cloud.Capabilities = append([]string(nil), old.Cloud.Capabilities...)
					}
				}
			} else {
				// First target is group/<id> or local/<model>; clear any stale direct Cloud binding.
				p.Cloud = AppCloud{}
			}
		} else {
			// Explicit empty Models slice.
			if in.Cloud.ProviderID == "" && in.Cloud.Model == "" {
				p.Models = nil
				p.Cloud = AppCloud{}
			} else {
				p.Cloud = in.Cloud
				if in.Cloud.Capabilities == nil && in.Cloud.ProviderID == old.Cloud.ProviderID && in.Cloud.Model == old.Cloud.Model {
					p.Cloud.Capabilities = append([]string(nil), old.Cloud.Capabilities...)
				}
				if p.Cloud.ProviderID != "" && p.Cloud.Model != "" {
					p.Models = []string{p.Cloud.ProviderID + "/" + p.Cloud.Model}
				}
			}
		}
	} else {
		p.Cloud = in.Cloud
		// Older editors omit capability declarations. Retain them only while the
		// exact binding is unchanged; an explicit empty array clears them.
		if in.Cloud.Capabilities == nil && in.Cloud.ProviderID == old.Cloud.ProviderID && in.Cloud.Model == old.Cloud.Model {
			p.Cloud.Capabilities = append([]string(nil), old.Cloud.Capabilities...)
		}
		if p.Cloud.ProviderID != "" && p.Cloud.Model != "" {
			p.Models = []string{p.Cloud.ProviderID + "/" + p.Cloud.Model}
		} else {
			p.Models = nil
		}
	}

	p.CredentialMode = in.CredentialMode
	p.RedactionEnabled = in.RedactionEnabled
	p.ReferencePrice = in.ReferencePrice
	if err := validateApp(p); err != nil {
		return AppProfile{}, err
	}
	if p.Cloud.ProviderID != "" {
		if upstream, _, err := f.resolved(p.Cloud.ProviderID); err != nil {
			return AppProfile{}, err
		} else if in.ExpectedProviderRevision != 0 && upstream.Config.Revision != in.ExpectedProviderRevision {
			return AppProfile{}, ErrProviderConflict
		} else if len(p.Cloud.Capabilities) > 0 && upstream.Config.Protocol != "responses" {
			return AppProfile{}, invalidProvider("enhanced tool capabilities require a native Responses target")
		}
	}
	for _, m := range p.Models {
		if provID, _, kind, ok := parseAppModelTarget(m); ok && kind == "cloud" {
			if _, _, err := f.resolved(provID); err != nil {
				return AppProfile{}, err
			}
		}
	}
	// Existing intent remains readable while a new enable is allowed only when
	// the service supplies accepted capability or explicit experimental trial
	// authorization. Neither state is accepted from the editor's JSON.
	if p.RedactionEnabled && !old.RedactionEnabled && !redactionMayEnable(redactionCapability) {
		return AppProfile{}, invalidProvider("redaction capability is not available")
	}
	p.Configured = p.Cloud.ProviderID != "" || len(p.Models) > 0
	return p, nil
}

func (s *Store) SaveApp(in AppEdit) (AppProfile, error) {
	return s.SaveAppContext(context.Background(), in)
}

func (s *Store) SaveAppContext(ctx context.Context, in AppEdit) (AppProfile, error) {
	return s.saveAppContext(ctx, in, RedactionUnavailable)
}

// SaveAppWithRedactionCapabilityContext is used only by the service after it
// has read the Manager's server-owned acceptance or opted-in trial state. The capability is
// never supplied by an HTTP caller.
func (s *Store) SaveAppWithRedactionCapabilityContext(ctx context.Context, in AppEdit, capability RedactionCapability) (AppProfile, error) {
	return s.saveAppContext(ctx, in, capability)
}

func (s *Store) saveAppContext(ctx context.Context, in AppEdit, capability RedactionCapability) (AppProfile, error) {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return AppProfile{}, err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return AppProfile{}, err
	}
	p, err := validatedAppDraft(f, in, capability)
	if err != nil {
		return AppProfile{}, err
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(f.document["apps"], &raw)
	fields := Document{}
	if b := raw[p.ID]; b != nil {
		_ = json.Unmarshal(b, &fields)
	}
	b := mergeObject(raw[p.ID], p)
	var updates Document
	_ = json.Unmarshal(b, &updates)
	updates["cloud"] = mergeObject(fields["cloud"], p.Cloud)
	if len(p.Models) == 0 {
		delete(updates, "models")
	}
	if p.ReferencePrice != nil {
		updates["referencePrice"] = mergeObject(fields["referencePrice"], p.ReferencePrice)
	}
	// Deprecated entry fields are ignored on read and removed only for this app
	// during its normal save. Private historical records/backups are untouched.
	for _, k := range []string{"builtin", "configured", "path", "hasEntryKey", "entryCredentialRef", "entryKey", "clearEntryKey"} {
		delete(updates, k)
	}
	raw[p.ID], _ = json.Marshal(updates)
	f.document["apps"], _ = json.Marshal(raw)
	if err := s.writeProviders(ctx, f, false); err != nil {
		return AppProfile{}, err
	}
	p.Configured = p.Cloud.ProviderID != "" || len(p.Models) > 0
	return p, nil
}

// DeleteApp removes custom registrations. Presets stay registered; DELETE clears
// their Profile. A revision tombstone prevents stale-editor ABA.
func (s *Store) DeleteApp(id string, revision int) error {
	return s.DeleteAppContext(context.Background(), id, revision)
}

func (s *Store) DeleteAppContext(ctx context.Context, id string, revision int) error {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return err
	}
	apps, err := readApps(f.document)
	if err != nil {
		return err
	}
	p, ok := apps[id]
	if !ok {
		return ErrAppNotFound
	}
	if p.Revision != revision {
		return ErrAppConflict
	}
	var raw Document
	_ = json.Unmarshal(f.document["apps"], &raw)
	next, err := nextAppRevision(f.document, revision)
	if err != nil {
		return err
	}
	if p.Builtin {
		p = defaultApp(id, builtinApps[id])
		p.Revision = next
		var previous Document
		_ = json.Unmarshal(raw[id], &previous)
		b := mergeObject(raw[id], p)
		var fields Document
		_ = json.Unmarshal(b, &fields)
		fields["cloud"] = mergeObject(previous["cloud"], p.Cloud)
		delete(fields, "models")
		delete(fields, "entryCredentialRef")
		for _, k := range []string{"builtin", "configured", "path", "hasEntryKey", "entryKey", "clearEntryKey"} {
			delete(fields, k)
		}
		raw[id], _ = json.Marshal(fields)
	} else {
		delete(raw, id)
	}
	f.document["apps"], _ = json.Marshal(raw)
	return s.writeProviders(ctx, f, false)
}
