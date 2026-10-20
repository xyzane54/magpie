package routerconfig

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGroupCycleDetection(t *testing.T) {
	s, layout := newGroupTestStore(t)
	if _, err := s.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	configPath := layout.ConfigFile()

	// 1. Direct self-reference (A -> A).
	_, err := SaveGroup(configPath, Group{
		ID:      "alpha",
		Name:    "Alpha",
		Members: []string{"group/alpha"},
	})
	if !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("expected ErrGroupCycle for direct self-reference, got %v", err)
	}

	// 2. Two-node cycle (A -> B -> A).
	_, err = SaveGroups(configPath, []Group{
		{ID: "a", Name: "Group A", Members: []string{"group/b"}},
		{ID: "b", Name: "Group B", Members: []string{"group/a"}},
	})
	if !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("expected ErrGroupCycle for 2-hop cycle A->B->A, got %v", err)
	}

	// 3. Three-node cycle (A -> B -> C -> A).
	_, err = SaveGroups(configPath, []Group{
		{ID: "a", Name: "Group A", Members: []string{"openai/gpt-4.1", "group/b"}},
		{ID: "b", Name: "Group B", Members: []string{"group/c"}},
		{ID: "c", Name: "Group C", Members: []string{"group/a"}},
	})
	if !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("expected ErrGroupCycle for 3-hop cycle A->B->C->A, got %v", err)
	}

	// 4. Cycle via Rule target/use (A -> B -> rule(group/a)).
	_, err = SaveGroups(configPath, []Group{
		{ID: "a", Name: "Group A", Members: []string{"group/b"}},
		{
			ID:      "b",
			Name:    "Group B",
			Members: []string{"openai/gpt-4.1"},
			Rules:   []Rule{{Pattern: "complex", Target: "group/a"}},
		},
	})
	if !errors.Is(err, ErrGroupCycle) {
		t.Fatalf("expected ErrGroupCycle for rule cycle, got %v", err)
	}

	// 5. Acyclic nested groups succeed and expand properly.
	validGroups, err := SaveGroups(configPath, []Group{
		{ID: "tier-1", Name: "Tier 1", Members: []string{"p1/m1", "group/tier-2"}},
		{ID: "tier-2", Name: "Tier 2", Members: []string{"p2/m2", "group/tier-3"}},
		{ID: "tier-3", Name: "Tier 3", Members: []string{"p3/m3", "p1/m1"}},
	})
	if err != nil {
		t.Fatalf("expected acyclic nested groups to succeed, got %v", err)
	}
	expanded := ExpandGroupMembers(validGroups[0], validGroups)
	wantExpanded := []string{"p1/m1", "p2/m2", "p3/m3"}
	if !reflect.DeepEqual(expanded, wantExpanded) {
		t.Fatalf("ExpandGroupMembers got %v, want %v", expanded, wantExpanded)
	}
}

func TestEffectiveGroupsAutoMergeAndOverrides(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	providers := []CloudProvider{
		{
			ID:               "anthropic-direct",
			Name:             "Anthropic Direct",
			Preset:           "anthropic",
			DiscoveredModels: []string{"claude-sonnet-4-20250514", "claude-3-5-haiku-20241022"},
		},
		{
			ID:               "openrouter",
			Name:             "OpenRouter",
			Preset:           "openrouter",
			DiscoveredModels: []string{"anthropic/claude-sonnet-4-0", "deepseek/deepseek-r1"},
		},
		{
			ID:     "deepseek-official",
			Name:   "DeepSeek Official",
			Preset: "deepseek",
			// No DiscoveredModels; relies on catalogModels fallback.
		},
	}
	catalogModels := map[string][]string{
		"deepseek": {"DeepSeek-R1", "deepseek-chat"},
	}

	// 1. Pure auto-merge without user groups:
	// - claude-sonnet-4 is served by anthropic-direct and openrouter (>=2 providers) -> auto-claude-sonnet-4
	// - deepseek-r1 is served by openrouter and deepseek-official (>=2 providers) -> auto-deepseek-r1
	// - claude-3-5-haiku and deepseek-chat are only on 1 provider -> NOT auto-merged
	cfg := Config{
		Providers: providers,
	}
	eff := EffectiveGroups(cfg, catalogModels)
	if len(eff) != 2 {
		t.Fatalf("expected 2 auto-merged groups, got %d: %+v", len(eff), eff)
	}

	byID := make(map[string]Group, len(eff))
	for _, g := range eff {
		byID[g.ID] = g
	}

	sonnetGroup, ok := byID["auto-claude-sonnet-4"]
	if !ok {
		t.Fatalf("expected auto-claude-sonnet-4 group, got %+v", eff)
	}
	if !sonnetGroup.Auto {
		t.Fatalf("expected auto-claude-sonnet-4 to have Auto=true")
	}
	if sonnetGroup.Name != "claude-sonnet-4" {
		t.Fatalf("expected MergeName 'claude-sonnet-4', got %q", sonnetGroup.Name)
	}
	wantSonnetMembers := []string{
		"anthropic-direct/claude-sonnet-4-20250514",
		"openrouter/anthropic/claude-sonnet-4-0",
	}
	if !reflect.DeepEqual(sonnetGroup.Members, wantSonnetMembers) {
		t.Fatalf("unexpected sonnet members: got %v, want %v", sonnetGroup.Members, wantSonnetMembers)
	}

	r1Group, ok := byID["auto-deepseek-r1"]
	if !ok {
		t.Fatalf("expected auto-deepseek-r1 group, got %+v", eff)
	}
	wantR1Members := []string{
		"openrouter/deepseek/deepseek-r1",
		"deepseek-official/DeepSeek-R1",
	}
	if !reflect.DeepEqual(r1Group.Members, wantR1Members) {
		t.Fatalf("unexpected r1 members: got %v, want %v", r1Group.Members, wantR1Members)
	}

	// 2. User saves custom override for auto-claude-sonnet-4 and a glob Match group.
	_, err := SaveGroup(configPath, Group{
		ID:      "auto-claude-sonnet-4",
		Name:    "Claude Sonnet 4 Custom",
		Members: wantSonnetMembers,
		Routing: Weighted,
		Weights: map[string]int{
			"anthropic-direct/claude-sonnet-4-20250514": 80,
			"openrouter/anthropic/claude-sonnet-4-0":    20,
		},
	})
	if err != nil {
		t.Fatalf("SaveGroup custom override: %v", err)
	}
	_, err = SaveGroup(configPath, Group{
		ID:      "all-deepseek",
		Name:    "All DeepSeek",
		Members: []string{"openrouter/deepseek/deepseek-r1"},
		Match:   []string{"*deepseek*"},
	})
	if err != nil {
		t.Fatalf("SaveGroup match group: %v", err)
	}

	// 3. User deletes/dismisses auto-deepseek-r1 -> writes Hidden: true tombstone.
	deleted, err := DeleteGroup(configPath, "auto-deepseek-r1")
	if err != nil || !deleted {
		t.Fatalf("DeleteGroup auto-deepseek-r1: deleted=%v, err=%v", deleted, err)
	}

	savedGroups, err := ListGroups(configPath)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	cfg.Groups = savedGroups

	effAfter := EffectiveGroups(cfg, catalogModels)
	afterByID := make(map[string]Group, len(effAfter))
	for _, g := range effAfter {
		if g.Hidden {
			t.Fatalf("Hidden tombstone group %q must not appear in EffectiveGroups", g.ID)
		}
		afterByID[g.ID] = g
	}

	// auto-deepseek-r1 must be suppressed by tombstone.
	if _, exists := afterByID["auto-deepseek-r1"]; exists {
		t.Fatalf("expected auto-deepseek-r1 to be hidden by tombstone, got %+v", effAfter)
	}

	// auto-claude-sonnet-4 must reflect the user's saved customization without duplication.
	override, exists := afterByID["auto-claude-sonnet-4"]
	if !exists {
		t.Fatalf("expected overridden auto-claude-sonnet-4 in EffectiveGroups")
	}
	if override.Name != "Claude Sonnet 4 Custom" || override.Routing != Weighted || override.Auto {
		t.Fatalf("expected user override to take precedence, got %+v", override)
	}

	// all-deepseek should have Matched populated with remaining deepseek models not already in Members.
	dsGroup, exists := afterByID["all-deepseek"]
	if !exists {
		t.Fatalf("expected all-deepseek group in EffectiveGroups")
	}
	wantMatched := []string{"deepseek-official/DeepSeek-R1", "deepseek-official/deepseek-chat"}
	if !reflect.DeepEqual(dsGroup.Matched, wantMatched) {
		t.Fatalf("unexpected Matched for all-deepseek: got %v, want %v", dsGroup.Matched, wantMatched)
	}
}
