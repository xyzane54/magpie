package routerhost

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func (e Engine) failed(j *journal, cause error) error {
	var rollbackErr error
	for _, f := range j.Files {
		s, err := e.readSnapshot(j.Root, f.FilePlan)
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		if equal(s.Before, f.Before) && s.Existed == f.Existed {
			if f.Existed && s.Mode != f.Mode {
				rollbackErr = errors.Join(rollbackErr, os.Chmod(hostPath(j.Root, f.Name), fs.FileMode(f.Mode)))
			}
			continue
		}
		if !equal(s.Before, f.After) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback conflict in %s", f.Name))
			continue
		}
		if s.After = f.Before; !f.Existed {
			s.After = nil
		}
		if err := e.replace(j.Root, s); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		} else if f.Existed {
			rollbackErr = errors.Join(rollbackErr, os.Chmod(hostPath(j.Root, f.Name), fs.FileMode(f.Mode)))
		}
	}
	if rollbackErr == nil {
		j.Status = "restored"
		rollbackErr = e.saveJournal(j)
	}
	return fmt.Errorf("configuration operation failed; recovery record retained; use host restore-preview / restore: %w", errors.Join(cause, rollbackErr))
}

func loomyProviderPaths(models ...string) [][]string {
	paths := [][]string{{"npm"}, {"name"}, {"options", "baseURL"}, {"options", "apiKey"}, {"options", "useSessionAuth"}}
	for _, m := range models {
		paths = append(paths, []string{"models", m, "name"}, []string{"models", m, "tool_call"})
	}
	return paths
}

func loomyManagedPaths(models ...string) [][]string {
	paths := [][]string{{"model"}, {"small_model"}}
	for _, path := range loomyProviderPaths(models...) {
		paths = append(paths, append([]string{"provider", loomyProvider}, path...))
	}
	return paths
}

func loomyModelKey(model string) bool {
	return strings.TrimSpace(model) != "" && !strings.ContainsAny(model, "\\*?#|![]@\"\r\n\x00")
}

func loomyJSONPath(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = strings.ReplaceAll(p, ".", `\.`)
	}
	return strings.Join(parts, ".")
}

func workBuddyModelEntries(doc map[string]any) []map[string]any {
	var out []map[string]any
	models, _ := doc["models"].([]any)
	for _, m := range models {
		out = append(out, m.(map[string]any))
	}
	return out
}

func workBuddyModel(doc map[string]any, id string) (map[string]any, int) {
	for i, m := range workBuddyModelEntries(doc) {
		if m["id"] == id {
			return m, i
		}
	}
	return nil, -1
}

func catalogModels(host string, raw []byte) (map[string]any, error) {
	if host == "workbuddy" {
		doc, err := workBuddyModels(raw)
		if err != nil {
			return nil, err
		}
		models := map[string]any{}
		for _, entry := range workBuddyModelEntries(doc) {
			models[entry["id"].(string)] = entry
		}
		return models, nil
	}
	doc, err := readJSON(raw)
	if err != nil {
		return nil, err
	}
	value, exists := lookup(doc, []string{"provider", loomyProvider, "models"})
	if !exists {
		return map[string]any{}, nil
	}
	models, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid Router model catalog")
	}
	return models, nil
}

func removedModelIDs(host string, before, after []byte) ([]string, error) {
	original, err := catalogModels(host, before)
	if err != nil {
		return nil, err
	}
	current, err := catalogModels(host, after)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id := range original {
		if _, exists := current[id]; !exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func removeRetiredModels(host string, raw []byte, ids []string) ([]byte, error) {
	retired := map[string]bool{}
	for _, id := range ids {
		retired[id] = true
	}
	if host == "workbuddy" {
		doc, err := workBuddyModels(raw)
		if err != nil {
			return nil, err
		}
		models := workBuddyModelEntries(doc)
		for i := len(models) - 1; i >= 0; i-- {
			if retired[models[i]["id"].(string)] {
				raw, err = workBuddyRemoveModel(raw, i)
				if err != nil {
					return nil, err
				}
			}
		}
		return raw, nil
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for _, id := range ordered {
		if !loomyModelKey(id) {
			return nil, errors.New("invalid retired Loomy model identity")
		}
		var err error
		raw, err = deleteJSONField(raw, loomyJSONPath([]string{"provider", loomyProvider, "models", id}))
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func restoreConnectionContent(a Adapter, j *journal, f FilePlan, current []byte) ([]byte, error) {
	if len(j.RetiredModels) == 0 || f.Name != "models.json" && f.Name != "opencode.json" {
		return restoreContent(a, f, current)
	}
	cleaned, err := removeRetiredModels(j.Host, f.After, j.RetiredModels)
	if err != nil {
		return nil, err
	}
	// Preserve the exact original bytes when no later host or user edit exists.
	if equal(current, cleaned) {
		if !f.Existed {
			return nil, nil
		}
		return bytes.Clone(f.Before), nil
	}
	original, err := catalogModels(j.Host, f.Before)
	if err != nil {
		return nil, err
	}
	now, err := catalogModels(j.Host, current)
	if err != nil {
		return nil, err
	}
	for _, id := range j.RetiredModels {
		value, existed := original[id]
		if !existed {
			continue
		}
		if present, exists := now[id]; exists {
			if !equalValue(value, true, present, true) {
				return nil, errors.New("retired model was replaced by a user definition; reconcile before restoring")
			}
			continue
		}
		if j.Host == "workbuddy" {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid original retired model")
			}
			current, err = workBuddyAppendModel(current, entry)
		} else {
			current, err = edit.PatchJSON(current, []edit.KV{{Path: loomyJSONPath([]string{"provider", loomyProvider, "models", id}), Value: value}}, nil)
		}
		if err != nil {
			return nil, err
		}
	}
	return restoreContent(a, f, current)
}
