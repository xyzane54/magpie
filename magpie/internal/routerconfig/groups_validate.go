package routerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DetectGroupCycle checks for direct or indirect cycles among nested "group/<id>" references.
func DetectGroupCycle(groups []Group) error {
	byID := make(map[string]Group, len(groups))
	for _, g := range groups {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		byID[id] = g
	}

	edgesOf := func(g Group) []string {
		var refs []string
		for _, m := range g.Members {
			if subID, ok := GroupIDOf(m); ok {
				refs = append(refs, subID)
			}
		}
		for _, r := range g.Rules {
			if subID, ok := GroupIDOf(r.Use); ok {
				refs = append(refs, subID)
			}
			if subID, ok := GroupIDOf(r.Target); ok {
				refs = append(refs, subID)
			}
		}
		return refs
	}

	const (
		stateUnvisited = 0
		stateVisiting  = 1
		stateVisited   = 2
	)
	state := make(map[string]int, len(byID))

	var dfs func(id string) error
	dfs = func(id string) error {
		state[id] = stateVisiting
		g, exists := byID[id]
		if exists {
			for _, nextID := range edgesOf(g) {
				if nextID == id {
					return fmt.Errorf("%w: %s -> %s", ErrGroupCycle, id, nextID)
				}
				switch state[nextID] {
				case stateVisiting:
					return fmt.Errorf("%w: %s -> %s", ErrGroupCycle, id, nextID)
				case stateUnvisited:
					if _, ok := byID[nextID]; ok {
						if err := dfs(nextID); err != nil {
							return err
						}
					}
				}
			}
		}
		state[id] = stateVisited
		return nil
	}

	for _, g := range groups {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		if state[id] == stateUnvisited {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateAndNormalizeGroups normalizes all groups, disambiguates or rejects duplicate IDs,
// and ensures there are no nested group cycles.
func ValidateAndNormalizeGroups(groups []Group) ([]Group, error) {
	if len(groups) == 0 {
		return []Group{}, nil
	}
	out := make([]Group, 0, len(groups))
	seenID := make(map[string]int, len(groups))
	for i, raw := range groups {
		hadExplicitID := strings.TrimSpace(raw.ID) != ""
		g, err := NormalizeGroup(raw)
		if err != nil {
			return nil, err
		}
		if prevIdx, exists := seenID[g.ID]; exists {
			if !hadExplicitID {
				base := g.ID
				for seq := 2; ; seq++ {
					cand := fmt.Sprintf("%s-%d", base, seq)
					if len(cand) <= maxGroupIDLen {
						if _, taken := seenID[cand]; !taken {
							g.ID = cand
							break
						}
					} else {
						return nil, invalidGroup(fmt.Sprintf("duplicate group id %q", base))
					}
				}
			} else {
				// Replace earlier entry if same explicit ID is overwritten in batch, or keep latest.
				out[prevIdx] = g
				continue
			}
		}
		seenID[g.ID] = len(out)
		_ = i
		out = append(out, g)
	}
	if err := DetectGroupCycle(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateGroups checks whether a slice of groups is valid and cycle-free.
func ValidateGroups(groups []Group) error {
	_, err := ValidateAndNormalizeGroups(groups)
	return err
}

// ExpandGroupMembers safely expands a group's members (including nested "group/<id>" references)
// with cycle protection and a depth cap of MaxGroupNest.
func ExpandGroupMembers(g Group, all []Group) []string {
	byID := make(map[string]Group, len(all))
	for _, item := range all {
		if item.ID != "" && !item.Hidden {
			byID[item.ID] = item
		}
	}
	if g.ID != "" {
		byID[g.ID] = g
	}

	seen := make(map[string]bool)
	visitedGroups := make(map[string]bool)
	var result []string

	var walk func(cur Group, depth int)
	walk = func(cur Group, depth int) {
		if depth > MaxGroupNest {
			return
		}
		if cur.ID != "" {
			if visitedGroups[cur.ID] {
				return
			}
			visitedGroups[cur.ID] = true
			defer func() { visitedGroups[cur.ID] = false }()
		}
		allMembers := make([]string, 0, len(cur.Members)+len(cur.Matched))
		allMembers = append(allMembers, cur.Members...)
		for _, m := range cur.Matched {
			allMembers = append(allMembers, m)
		}
		for _, m := range allMembers {
			if subID, ok := GroupIDOf(m); ok {
				if sub, exists := byID[subID]; exists && !sub.Disabled {
					walk(sub, depth+1)
				}
				continue
			}
			if !seen[m] {
				seen[m] = true
				result = append(result, m)
			}
		}
	}
	walk(g, 0)
	return result
}

func readGroupsFromDoc(d Document) ([]Group, error) {
	raw, ok := d["groups"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var groups []Group
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("invalid groups: %w", err)
	}
	return groups, nil
}

// ListGroups returns the persisted routing groups from the Store.
func (s *Store) ListGroups() ([]Group, error) {
	return s.ListGroupsContext(context.Background())
}

// ListGroupsContext returns the persisted routing groups from the Store.
func (s *Store) ListGroupsContext(ctx context.Context) ([]Group, error) {
	d, err := s.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	return readGroupsFromDoc(d)
}

// SaveGroups validates, normalizes, and persists the full routing group list into config.json.
func (s *Store) SaveGroups(groups []Group) ([]Group, error) {
	return s.SaveGroupsContext(context.Background(), groups)
}

// SaveGroupsContext validates, normalizes, and persists the full routing group list into config.json.
func (s *Store) SaveGroupsContext(ctx context.Context, groups []Group) ([]Group, error) {
	normalized, err := ValidateAndNormalizeGroups(groups)
	if err != nil {
		return nil, err
	}
	err = s.UpdateContext(ctx, func(d Document) error {
		if len(normalized) == 0 {
			delete(d, "groups")
			return nil
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		d["groups"] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	return normalized, nil
}
