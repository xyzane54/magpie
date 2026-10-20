package routerconfig

import (
	"context"
	"encoding/json"
	"errors"
)

// RedactionCapability is server-owned authorization, never client input.
// Available means the runtime, recognition specification and cloud integration
// have all been accepted. Loading or enabling a model alone is insufficient.
// Experimental permits explicitly opted-in trial intent without formal quality
// acceptance; the service owns that opt-in and its current trial readiness.
type RedactionCapability string

const (
	RedactionUnknown      RedactionCapability = "unknown"
	RedactionUnavailable  RedactionCapability = "unavailable"
	RedactionAvailable    RedactionCapability = "available"
	RedactionExperimental RedactionCapability = "experimental"
)

func redactionMayEnable(capability RedactionCapability) bool {
	return capability == RedactionAvailable || capability == RedactionExperimental
}

var ErrRedactionUnavailable = errors.New("redaction_unavailable")

// RedactionIntentEdit changes only the saved intent of an existing Profile.
// Every selected app supplies its current revision, including virtual presets.
type RedactionIntentEdit struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Enabled  bool   `json:"enabled"`
}

// SetRedactionIntentsContext applies a global selection atomically. Unknown or
// unavailable capability may retain or disable intent, but cannot newly enable
// it. Runtime selection/disable never invokes this writer to clear user intent.
func (s *Store) SetRedactionIntentsContext(ctx context.Context, edits []RedactionIntentEdit, capability RedactionCapability) ([]AppProfile, error) {
	if len(edits) == 0 || len(edits) > 1024 {
		return nil, invalidProvider("invalid redaction selection")
	}
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	f, err := s.providerFilesContext(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := readApps(f.document)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(edits))
	for _, edit := range edits {
		if !ValidAppID(edit.ID) || edit.Revision < 0 || seen[edit.ID] {
			return nil, invalidProvider("invalid redaction selection")
		}
		seen[edit.ID] = true
		old, ok := apps[edit.ID]
		if !ok {
			return nil, ErrAppNotFound
		}
		if old.Revision != edit.Revision {
			return nil, ErrAppConflict
		}
		if edit.Enabled && !old.RedactionEnabled && !redactionMayEnable(capability) {
			return nil, ErrRedactionUnavailable
		}
	}
	var raw Document
	if err := json.Unmarshal(f.document["apps"], &raw); err != nil {
		return nil, err
	}
	saved := make([]AppProfile, 0, len(edits))
	changed := false
	for _, edit := range edits {
		p := apps[edit.ID]
		if p.RedactionEnabled != edit.Enabled {
			p.Revision, err = nextAppRevision(f.document, p.Revision)
			if err != nil {
				return nil, err
			}
			p.RedactionEnabled = edit.Enabled
			if err := validateApp(p); err != nil {
				return nil, err
			}
			// Existing records retain all fields verbatim except intent/revision;
			// virtual presets are materialized with their established defaults.
			if raw[p.ID] == nil {
				raw[p.ID] = mergeObject(nil, p)
				var fields Document
				_ = json.Unmarshal(raw[p.ID], &fields)
				delete(fields, "builtin")
				delete(fields, "configured")
				delete(fields, "path")
				raw[p.ID], _ = json.Marshal(fields)
			} else {
				raw[p.ID] = mergeObject(raw[p.ID], struct {
					Revision int  `json:"revision"`
					Enabled  bool `json:"redactionEnabled"`
				}{p.Revision, p.RedactionEnabled})
			}
			changed = true
		}
		saved = append(saved, p)
	}
	if changed {
		f.document["apps"], _ = json.Marshal(raw)
		if err := s.writeProviders(ctx, f, false); err != nil {
			return nil, err
		}
		return saved, nil
	}
	return saved, ctx.Err()
}
