package routerconfig

import (
	"fmt"
	"strings"
)

// NormalizeGroup validates and normalizes a single Group before persistence.
// Derived fields (Matched, Auto) are cleared, slices are trimmed and deduplicated,
// and orphan Off/Weights entries not present in Members (or Matched) are pruned.
func NormalizeGroup(g Group) (Group, error) {
	if hasControlChars(g.ID) || hasControlChars(g.Name) {
		return Group{}, invalidGroup("group id or name contains control characters")
	}
	g.ID = strings.TrimSpace(g.ID)
	g.Name = strings.TrimSpace(g.Name)

	if g.ID == "" && g.Name != "" {
		g.ID = GroupSlug(g.Name)
	}
	if !g.Hidden {
		if g.Name == "" && g.ID != "" {
			g.Name = g.ID
		}
		if g.ID == "" || g.Name == "" {
			return Group{}, invalidGroup("group id or name is required")
		}
	}
	if err := validateGroupID(g.ID); err != nil {
		return Group{}, err
	}

	if g.Hidden {
		return Group{
			ID:     g.ID,
			Hidden: true,
		}, nil
	}

	var err error
	members, err := cleanGroupSlice(g.Members, "members")
	if err != nil {
		return Group{}, err
	}
	if members == nil {
		members = []string{}
	}
	matched, err := cleanGroupSlice(g.Matched, "matched")
	if err != nil {
		return Group{}, err
	}
	g.Match, err = cleanGroupSlice(g.Match, "match")
	if err != nil {
		return Group{}, err
	}
	off, err := cleanGroupSlice(g.Off, "off")
	if err != nil {
		return Group{}, err
	}
	g.Levels, err = cleanGroupSlice(g.Levels, "levels")
	if err != nil {
		return Group{}, err
	}
	g.Fast, err = cleanGroupSlice(g.Fast, "fast")
	if err != nil {
		return Group{}, err
	}

	// Check direct self-reference in members.
	for _, m := range members {
		if subID, ok := GroupIDOf(m); ok && subID == g.ID {
			return Group{}, fmt.Errorf("%w: group %q references itself", ErrGroupCycle, g.ID)
		}
	}

	validMember := make(map[string]bool, len(members)+len(matched))
	for _, m := range members {
		validMember[m] = true
		if bare, _, ok := strings.Cut(m, ":"); ok && bare != "" {
			validMember[bare] = true
		}
	}
	for _, m := range matched {
		validMember[m] = true
		if bare, _, ok := strings.Cut(m, ":"); ok && bare != "" {
			validMember[bare] = true
		}
	}

	isValidTarget := func(m string) bool {
		if validMember[m] {
			return true
		}
		if bare, _, ok := strings.Cut(m, ":"); ok && validMember[bare] {
			return true
		}
		for _, pat := range g.Match {
			if GroupMatchesModel(pat, m) {
				return true
			}
		}
		return false
	}

	if len(off) > 0 {
		filteredOff := make([]string, 0, len(off))
		for _, m := range off {
			if isValidTarget(m) {
				filteredOff = append(filteredOff, m)
			}
		}
		if len(filteredOff) > 0 {
			g.Off = filteredOff
		} else {
			g.Off = nil
		}
	} else {
		g.Off = nil
	}

	if len(g.Weights) > 0 {
		cleanedWeights := make(map[string]int, len(g.Weights))
		for k, w := range g.Weights {
			k = strings.TrimSpace(k)
			if k != "" && w > 0 && isValidTarget(k) {
				cleanedWeights[k] = w
			}
		}
		if len(cleanedWeights) > 0 {
			g.Weights = cleanedWeights
		} else {
			g.Weights = nil
		}
	} else {
		g.Weights = nil
	}

	cleanedEfforts := make(map[string]string, len(g.MemberEfforts))
	for _, m := range members {
		if bare, eff, ok := strings.Cut(m, ":"); ok {
			bare = strings.TrimSpace(bare)
			eff = strings.ToLower(strings.TrimSpace(eff))
			if bare != "" && eff != "" {
				cleanedEfforts[bare] = eff
			}
		}
	}
	for k, eff := range g.MemberEfforts {
		k = strings.TrimSpace(k)
		eff = strings.ToLower(strings.TrimSpace(eff))
		if k != "" && eff != "" && isValidTarget(k) {
			cleanedEfforts[k] = eff
		}
	}
	if len(cleanedEfforts) > 0 {
		g.MemberEfforts = cleanedEfforts
	} else {
		g.MemberEfforts = nil
	}

	g.Members = members
	g.Routing = strings.TrimSpace(g.Routing)
	g.Affinity = strings.TrimSpace(g.Affinity)
	if g.Stickiness < 0 {
		g.Stickiness = 0
	}
	g.Pick = strings.TrimSpace(g.Pick)
	g.Classifier = strings.TrimSpace(g.Classifier)
	g.Effort = strings.TrimSpace(g.Effort)
	g.Family = strings.TrimSpace(g.Family)

	if len(g.Rules) > 0 {
		cleanedRules := make([]Rule, 0, len(g.Rules))
		for _, r := range g.Rules {
			r.When = strings.ToLower(strings.TrimSpace(r.When))
			r.Use = strings.TrimSpace(r.Use)
			r.Target = strings.TrimSpace(r.Target)
			if r.Use == "" && r.Target != "" {
				r.Use = r.Target
			}
			r.Pattern = strings.TrimSpace(r.Pattern)
			r.Reason = strings.TrimSpace(r.Reason)
			r.Tokens = strings.TrimSpace(r.Tokens)
			r.Effort = strings.TrimSpace(r.Effort)
			r.Agents = strings.TrimSpace(r.Agents)
			r.Intent = strings.TrimSpace(r.Intent)
			r.Pause = strings.TrimSpace(r.Pause)
			if subID, ok := GroupIDOf(r.Use); ok && subID == g.ID {
				return Group{}, fmt.Errorf("%w: group %q rule references itself", ErrGroupCycle, g.ID)
			}
			if subID, ok := GroupIDOf(r.Target); ok && subID == g.ID {
				return Group{}, fmt.Errorf("%w: group %q rule references itself", ErrGroupCycle, g.ID)
			}
			r.Folders, err = cleanGroupSlice(r.Folders, "rule folders")
			if err != nil {
				return Group{}, err
			}
			r.ExceptFolders, err = cleanGroupSlice(r.ExceptFolders, "rule exceptFolders")
			if err != nil {
				return Group{}, err
			}
			cleanedRules = append(cleanedRules, r)
		}
		g.Rules = cleanedRules
	} else {
		g.Rules = nil
	}

	// Clear runtime-derived fields before persistence.
	g.Matched = nil
	g.Auto = false
	return g, nil
}
