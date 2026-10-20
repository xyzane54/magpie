package routerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/steady"
	"io/fs"
	"path/filepath"
	"strings"
)

// SaveGroup creates or updates a single routing group by ID and validates the resulting group set.
func (s *Store) SaveGroup(g Group) (Group, error) {
	return s.SaveGroupContext(context.Background(), g)
}

// SaveGroupContext creates or updates a single routing group by ID and validates the resulting group set.
func (s *Store) SaveGroupContext(ctx context.Context, g Group) (Group, error) {
	norm, err := NormalizeGroup(g)
	if err != nil {
		return Group{}, err
	}
	var saved Group
	err = s.UpdateContext(ctx, func(d Document) error {
		existing, err := readGroupsFromDoc(d)
		if err != nil {
			return err
		}
		replaced := false
		next := make([]Group, 0, len(existing)+1)
		for _, item := range existing {
			if item.ID == norm.ID {
				next = append(next, norm)
				replaced = true
			} else {
				next = append(next, item)
			}
		}
		if !replaced {
			next = append(next, norm)
		}
		validated, err := ValidateAndNormalizeGroups(next)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(validated)
		if err != nil {
			return err
		}
		d["groups"] = raw
		for _, item := range validated {
			if item.ID == norm.ID {
				saved = item
				break
			}
		}
		return nil
	})
	if err != nil {
		return Group{}, err
	}
	return saved, nil
}

// DeleteGroup removes a routing group by ID (or writes a Hidden tombstone for an auto-"auto-*" group).
func (s *Store) DeleteGroup(id string) (bool, error) {
	return s.DeleteGroupContext(context.Background(), id)
}

// DeleteGroupContext removes a routing group by ID (or writes a Hidden tombstone for an auto-"auto-*" group).
func (s *Store) DeleteGroupContext(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, invalidGroup("group id is required")
	}
	var deleted bool
	err := s.UpdateContext(ctx, func(d Document) error {
		existing, err := readGroupsFromDoc(d)
		if err != nil {
			return err
		}
		isAutoID := strings.HasPrefix(id, "auto-")
		foundIdx := -1
		for i, item := range existing {
			if item.ID == id {
				foundIdx = i
				if item.Auto {
					isAutoID = true
				}
				break
			}
		}
		if foundIdx < 0 && !isAutoID {
			deleted = false
			return nil
		}
		if isAutoID {
			// If it was an explicit non-hidden user group with "auto-" prefix that wasn't marked Auto,
			// or an auto group dismissal: write a Hidden tombstone so EffectiveGroups suppresses it,
			// unless the caller is deleting an existing custom group that wasn't auto.
			if foundIdx >= 0 && !existing[foundIdx].Auto && !existing[foundIdx].Hidden {
				// Replace with tombstone so auto-merge won't resurrect it, or remove if needed.
				existing[foundIdx] = Group{ID: id, Hidden: true}
			} else if foundIdx >= 0 && existing[foundIdx].Hidden {
				// Already a tombstone; keep it and report true.
				deleted = true
				return nil
			} else if foundIdx >= 0 {
				existing[foundIdx] = Group{ID: id, Hidden: true}
			} else {
				existing = append(existing, Group{ID: id, Hidden: true})
			}
			deleted = true
		} else {
			existing = append(existing[:foundIdx], existing[foundIdx+1:]...)
			deleted = true
		}
		if len(existing) == 0 {
			delete(d, "groups")
			return nil
		}
		raw, err := json.Marshal(existing)
		if err != nil {
			return err
		}
		d["groups"] = raw
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// LoadConfig loads a typed Config view (including Providers and Groups) from the Store.
func (s *Store) LoadConfig() (Config, error) {
	return s.LoadConfigContext(context.Background())
}

// LoadConfigContext loads a typed Config view (including Providers and Groups) from the Store.
func (s *Store) LoadConfigContext(ctx context.Context) (Config, error) {
	d, err := s.LoadContext(ctx)
	if err != nil {
		return Config{}, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Path-based helpers for working directly with a config.json path.

func storeForConfigPath(path string) (*Store, bool) {
	clean := filepath.Clean(path)
	if filepath.Base(clean) == "config.json" {
		dir := filepath.Dir(clean)
		return New(dirs.Layout{
			Install: dir,
			Config:  dir,
			Data:    dir,
			Models:  filepath.Join(dir, "models"),
			Runtime: filepath.Join(dir, "runtime"),
			Cache:   filepath.Join(dir, "cache"),
			State:   dir,
			Logs:    filepath.Join(dir, "logs"),
		}), true
	}
	return nil, false
}

func ensureStoreOrRawDoc(ctx context.Context, path string) (Document, []byte, error) {
	b, err := steady.ReadFileContext(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			d, derr := defaults()
			return d, nil, derr
		}
		return nil, nil, err
	}
	if d, perr := parse(b); perr == nil {
		return d, b, nil
	}
	// Fallback for minimal test JSON files that omit full routerconfig sections.
	var raw Document
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &raw); err != nil {
		return nil, nil, err
	}
	if raw == nil {
		raw = make(Document)
	}
	if rawGroups, ok := raw["groups"]; ok && len(bytes.TrimSpace(rawGroups)) > 0 && !bytes.Equal(bytes.TrimSpace(rawGroups), []byte("null")) {
		var groups []Group
		if err := json.Unmarshal(rawGroups, &groups); err != nil {
			return nil, nil, fmt.Errorf("groups must be a JSON array: %w", err)
		}
		if _, err := ValidateAndNormalizeGroups(groups); err != nil {
			return nil, nil, fmt.Errorf("groups: %w", err)
		}
	}
	return raw, b, nil
}

func writeRawOrFullDoc(ctx context.Context, path string, d Document, prevBytes []byte) error {
	if _, hasSchema := d["schemaVersion"]; hasSchema {
		b, err := encode(d)
		if err != nil {
			return err
		}
		if len(prevBytes) > 0 {
			if err := steady.WriteFileContext(ctx, path+".bak", prevBytes, 0o600); err != nil {
				return err
			}
		}
		return steady.WriteFileContext(ctx, path, b, 0o600)
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if len(prevBytes) > 0 {
		if err := steady.WriteFileContext(ctx, path+".bak", prevBytes, 0o600); err != nil {
			return err
		}
	}
	return steady.WriteFileContext(ctx, path, append(b, '\n'), 0o600)
}
