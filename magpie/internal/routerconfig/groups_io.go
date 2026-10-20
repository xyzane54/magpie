package routerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yetone/magpie/internal/steady"
	"io/fs"
	"os"
	"strings"
)

// LoadConfig loads a Config from the given file path.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var d Document
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &d); err != nil {
		return Config{}, err
	}
	if rawGroups, ok := d["groups"]; ok && len(bytes.TrimSpace(rawGroups)) > 0 && !bytes.Equal(bytes.TrimSpace(rawGroups), []byte("null")) {
		var groups []Group
		if err := json.Unmarshal(rawGroups, &groups); err != nil {
			return Config{}, fmt.Errorf("groups must be a JSON array: %w", err)
		}
		if _, err := ValidateAndNormalizeGroups(groups); err != nil {
			return Config{}, fmt.Errorf("groups: %w", err)
		}
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ListGroups reads the persisted routing groups from the file at path.
func ListGroups(path string) ([]Group, error) {
	return ListGroupsContext(context.Background(), path)
}

// ListGroupsContext reads the persisted routing groups from the file at path.
func ListGroupsContext(ctx context.Context, path string) ([]Group, error) {
	if s, ok := storeForConfigPath(path); ok {
		if d, err := s.LoadContext(ctx); err == nil {
			return readGroupsFromDoc(d)
		}
	}
	b, err := steady.ReadFileContext(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	d, _, err := ensureStoreOrRawDoc(ctx, path)
	if err != nil {
		return nil, err
	}
	_ = b
	return readGroupsFromDoc(d)
}

// SaveGroups validates, normalizes, and writes the routing group list to path,
// preserving all other fields in the JSON configuration file.
func SaveGroups(path string, groups []Group) ([]Group, error) {
	return SaveGroupsContext(context.Background(), path, groups)
}

// SaveGroupsContext validates, normalizes, and writes the routing group list to path.
func SaveGroupsContext(ctx context.Context, path string, groups []Group) ([]Group, error) {
	normalized, err := ValidateAndNormalizeGroups(groups)
	if err != nil {
		return nil, err
	}
	if s, ok := storeForConfigPath(path); ok {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			if _, initErr := s.InitializeContext(ctx); initErr != nil {
				return nil, initErr
			}
		}
		if res, sErr := s.SaveGroupsContext(ctx, normalized); sErr == nil {
			return res, nil
		}
	}
	d, prev, err := ensureStoreOrRawDoc(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		delete(d, "groups")
	} else {
		raw, err := json.Marshal(normalized)
		if err != nil {
			return nil, err
		}
		d["groups"] = raw
	}
	if err := writeRawOrFullDoc(ctx, path, d, prev); err != nil {
		return nil, err
	}
	return normalized, nil
}

// SaveGroup creates or updates a single routing group in path by ID, preserving all other fields.
func SaveGroup(path string, g Group) (Group, error) {
	return SaveGroupContext(context.Background(), path, g)
}

// SaveGroupContext creates or updates a single routing group in path by ID.
func SaveGroupContext(ctx context.Context, path string, g Group) (Group, error) {
	norm, err := NormalizeGroup(g)
	if err != nil {
		return Group{}, err
	}
	if s, ok := storeForConfigPath(path); ok {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			if _, initErr := s.InitializeContext(ctx); initErr != nil {
				return Group{}, initErr
			}
		}
		if res, sErr := s.SaveGroupContext(ctx, norm); sErr == nil {
			return res, nil
		}
	}
	d, prev, err := ensureStoreOrRawDoc(ctx, path)
	if err != nil {
		return Group{}, err
	}
	existing, err := readGroupsFromDoc(d)
	if err != nil {
		return Group{}, err
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
		return Group{}, err
	}
	raw, err := json.Marshal(validated)
	if err != nil {
		return Group{}, err
	}
	d["groups"] = raw
	if err := writeRawOrFullDoc(ctx, path, d, prev); err != nil {
		return Group{}, err
	}
	for _, item := range validated {
		if item.ID == norm.ID {
			return item, nil
		}
	}
	return norm, nil
}

// DeleteGroup removes a routing group by ID from path (or records a Hidden tombstone for auto groups).
func DeleteGroup(path, id string) (bool, error) {
	return DeleteGroupContext(context.Background(), path, id)
}

// DeleteGroupContext removes a routing group by ID from path.
func DeleteGroupContext(ctx context.Context, path, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, invalidGroup("group id is required")
	}
	if s, ok := storeForConfigPath(path); ok {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			if !strings.HasPrefix(id, "auto-") {
				return false, nil
			}
			if _, initErr := s.InitializeContext(ctx); initErr != nil {
				return false, initErr
			}
		}
		if deleted, sErr := s.DeleteGroupContext(ctx, id); sErr == nil {
			return deleted, nil
		}
	}
	d, prev, err := ensureStoreOrRawDoc(ctx, path)
	if err != nil {
		return false, err
	}
	existing, err := readGroupsFromDoc(d)
	if err != nil {
		return false, err
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
		return false, nil
	}
	if isAutoID {
		if foundIdx >= 0 {
			existing[foundIdx] = Group{ID: id, Hidden: true}
		} else {
			existing = append(existing, Group{ID: id, Hidden: true})
		}
	} else {
		existing = append(existing[:foundIdx], existing[foundIdx+1:]...)
	}
	if len(existing) == 0 {
		delete(d, "groups")
	} else {
		raw, err := json.Marshal(existing)
		if err != nil {
			return false, err
		}
		d["groups"] = raw
	}
	if err := writeRawOrFullDoc(ctx, path, d, prev); err != nil {
		return false, err
	}
	return true, nil
}
