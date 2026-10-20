package routerconfig

import "strings"

// EffectiveGroups computes the runtime effective routing groups by combining user-persisted
// groups in cfg.Groups with cross-provider auto-merged groups (when >= 2 distinct providers
// offer the same canonical model according to sameModel).
//
// Rules:
//   - Models for each provider are collected from p.Model, p.DiscoveredModels, and
//     catalogModels[p.ID] / catalogModels[p.Preset] (deduplicated per provider).
//   - If cfg.NoAutoGroups is false and >= 2 distinct providers serve the same canonical
//     model, an auto group (ID: AutoGroupID(canon), Name: MergeName(canon), Auto: true)
//     is synthesized unless overridden or hidden (Hidden: true) by a saved group in cfg.Groups.
//   - Saved groups with Hidden: true act as tombstones and are omitted from the returned slice.
//   - Groups with Match patterns have their Matched field populated from all available provider/model pairs.
func EffectiveGroups(cfg Config, catalogModels map[string][]string) []Group {
	return resolveEffectiveGroups(cfg.Groups, cfg.Providers, catalogModels, cfg.NoAutoGroups, cfg.GroupOrder)
}

// ResolveGroups is an alias helper that resolves effective groups from explicit slices.
func ResolveGroups(groups []Group, providers []CloudProvider, catalogModels map[string][]string) []Group {
	return resolveEffectiveGroups(groups, providers, catalogModels, false, nil)
}

func resolveEffectiveGroups(savedGroups []Group, providers []CloudProvider, catalogModels map[string][]string, noAutoGroups bool, groupOrder []string) []Group {
	type providerModels struct {
		providerID string
		models     []string
	}
	pModels := make([]providerModels, 0, len(providers))
	var allCandidates []string
	seenGlobalCandidate := make(map[string]bool)

	for _, p := range providers {
		pid := strings.TrimSpace(p.ID)
		if pid == "" {
			continue
		}
		seen := make(map[string]bool)
		var models []string
		addModel := func(raw string) {
			m := strings.TrimSpace(raw)
			if m == "" {
				return
			}
			// If caller passed "providerId/model" into catalogModels, strip the matching provider prefix.
			if strings.HasPrefix(m, pid+"/") {
				m = strings.TrimSpace(strings.TrimPrefix(m, pid+"/"))
				if m == "" {
					return
				}
			}
			if !seen[m] {
				seen[m] = true
				models = append(models, m)
			}
			cand := pid + "/" + m
			if !seenGlobalCandidate[cand] {
				seenGlobalCandidate[cand] = true
				allCandidates = append(allCandidates, cand)
			}
		}

		if p.Model != "" {
			addModel(p.Model)
		}
		for _, m := range p.DiscoveredModels {
			addModel(m)
		}
		if len(catalogModels) > 0 {
			for _, m := range catalogModels[pid] {
				addModel(m)
			}
			if preset := strings.TrimSpace(p.Preset); preset != "" && preset != pid {
				for _, m := range catalogModels[preset] {
					addModel(m)
				}
			}
		}
		if len(models) > 0 {
			pModels = append(pModels, providerModels{providerID: pid, models: models})
		}
	}

	// Index saved groups by ID.
	savedByID := make(map[string]Group, len(savedGroups))
	hiddenIDs := make(map[string]bool)
	for _, g := range savedGroups {
		id := strings.TrimSpace(g.ID)
		if id == "" && g.Name != "" {
			id = GroupSlug(g.Name)
			g.ID = id
		}
		if id == "" {
			continue
		}
		if g.Hidden {
			hiddenIDs[id] = true
		}
		savedByID[id] = g
	}

	// Build auto-merged groups across providers.
	type autoBucket struct {
		canon     string
		firstRaw  string
		members   []string
		seenMem   map[string]bool
		providers map[string]bool
	}
	var bucketOrder []string
	buckets := make(map[string]*autoBucket)

	if !noAutoGroups {
		for _, pm := range pModels {
			for _, m := range pm.models {
				canon := sameModel(m)
				if canon == "" {
					continue
				}
				b, ok := buckets[canon]
				if !ok {
					b = &autoBucket{
						canon:     canon,
						firstRaw:  m,
						seenMem:   make(map[string]bool),
						providers: make(map[string]bool),
					}
					buckets[canon] = b
					bucketOrder = append(bucketOrder, canon)
				}
				ref := pm.providerID + "/" + m
				if !b.seenMem[ref] {
					b.seenMem[ref] = true
					b.members = append(b.members, ref)
				}
				b.providers[pm.providerID] = true
			}
		}
	}

	var result []Group
	addedIDs := make(map[string]bool)

	// 1. Add user-persisted non-hidden groups first in their saved order.
	for _, g := range savedGroups {
		if g.ID == "" && g.Name != "" {
			g.ID = GroupSlug(g.Name)
		}
		if g.ID == "" || g.Hidden {
			continue
		}
		if addedIDs[g.ID] {
			continue
		}
		cp := cloneGroup(g)
		cp.Matched = computeMatchedMembers(cp, allCandidates)
		result = append(result, cp)
		addedIDs[cp.ID] = true
	}

	// 2. Synthesize auto-merged groups where >= 2 distinct providers share a canonical model,
	// unless suppressed by a Hidden tombstone or already overridden by a saved group with the same ID.
	if !noAutoGroups {
		for _, canon := range bucketOrder {
			b := buckets[canon]
			if len(b.providers) < 2 {
				continue
			}
			autoID := AutoGroupID(canon)
			if hiddenIDs[autoID] || addedIDs[autoID] {
				continue
			}
			if _, exists := savedByID[autoID]; exists {
				continue
			}
			ag := Group{
				ID:      autoID,
				Name:    MergeName(b.firstRaw),
				Members: append([]string(nil), b.members...),
				Auto:    true,
			}
			result = append(result, ag)
			addedIDs[autoID] = true
		}
	}

	if len(groupOrder) > 0 && len(result) > 1 {
		orderIdx := make(map[string]int, len(groupOrder))
		for i, id := range groupOrder {
			if _, exists := orderIdx[id]; !exists {
				orderIdx[id] = i
			}
		}
		ordered := make([]Group, 0, len(result))
		used := make(map[int]bool, len(result))
		for _, id := range groupOrder {
			for i, g := range result {
				if !used[i] && g.ID == id {
					ordered = append(ordered, g)
					used[i] = true
					break
				}
			}
		}
		for i, g := range result {
			if !used[i] {
				ordered = append(ordered, g)
			}
		}
		result = ordered
	}

	if result == nil {
		return []Group{}
	}
	return result
}
