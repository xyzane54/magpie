package routerconfig

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/dirs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newGroupTestStore(t *testing.T) (*Store, dirs.Layout) {
	t.Helper()
	root := t.TempDir()
	layout := dirs.Layout{
		Install: filepath.Join(root, "install"),
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Models:  filepath.Join(root, "data", "models"),
		Runtime: filepath.Join(root, "data", "runtime"),
		Cache:   filepath.Join(root, "cache"),
		State:   filepath.Join(root, "state"),
		Logs:    filepath.Join(root, "state", "logs"),
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return New(layout), layout
}

func TestGroupsCRUDAndLosslessPersistence(t *testing.T) {
	s, layout := newGroupTestStore(t)
	if _, err := s.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	configPath := layout.ConfigFile()

	// 1. Old config without "groups" field loads cleanly with empty groups.
	initialGroups, err := ListGroups(configPath)
	if err != nil {
		t.Fatalf("ListGroups on initial config: %v", err)
	}
	if len(initialGroups) != 0 {
		t.Fatalf("expected 0 initial groups, got %d", len(initialGroups))
	}

	// Inject custom top-level fields (accounts, hosts) to verify lossless round-trip.
	if err := s.Update(func(d Document) error {
		d["accounts"] = json.RawMessage(`{"acc-1":{"name":"primary"}}`)
		d["hosts"] = json.RawMessage(`{"workbuddy":{"port":13145}}`)
		return nil
	}); err != nil {
		t.Fatalf("inject custom fields: %v", err)
	}

	// 2. Save a provider first.
	key := "sk-test-12345"
	models := []string{"claude-sonnet-4-20250514", "gpt-4.1"}
	prov, err := s.SaveCloudProvider(ProviderEdit{
		Name:             "OpenAI Main",
		Protocol:         "chat",
		BaseURL:          "https://api.openai.com/v1",
		APIKey:           &key,
		DiscoveredModels: &models,
		ProxyMode:        "direct",
	})
	if err != nil {
		t.Fatalf("SaveCloudProvider: %v", err)
	}

	// 3. Save a group via SaveGroup(path, g) without explicit ID (auto-slug from Name).
	g1, err := SaveGroup(configPath, Group{
		Name:    "Smart Coding Pool",
		Members: []string{"openai-main/gpt-4.1", "anthropic/claude-sonnet-4"},
		Routing: SmartSplit,
		Effort:  EffortAuto,
	})
	if err != nil {
		t.Fatalf("SaveGroup g1: %v", err)
	}
	if g1.ID != "smart-coding-pool" {
		t.Fatalf("expected slug ID 'smart-coding-pool', got %q", g1.ID)
	}

	// 4. Save another group referencing g1.
	g2, err := SaveGroup(configPath, Group{
		ID:      "fallback-chain",
		Name:    "Fallback Chain",
		Members: []string{"group/smart-coding-pool", "openai-main/claude-sonnet-4-20250514"},
		Routing: Ordered,
	})
	if err != nil {
		t.Fatalf("SaveGroup g2: %v", err)
	}
	if g2.ID != "fallback-chain" {
		t.Fatalf("unexpected g2 ID: %q", g2.ID)
	}

	// 5. Update provider via SaveCloudProvider and verify groups are NOT lost.
	updatedProv, err := s.SaveCloudProvider(ProviderEdit{
		ID:        prov.ID,
		Revision:  prov.Revision,
		Name:      "OpenAI Main Updated",
		Protocol:  prov.Protocol,
		BaseURL:   prov.BaseURL,
		ProxyMode: prov.ProxyMode,
	})
	if err != nil {
		t.Fatalf("SaveCloudProvider update: %v", err)
	}
	if len(updatedProv.DiscoveredModels) != 2 {
		t.Fatalf("expected DiscoveredModels preserved, got %v", updatedProv.DiscoveredModels)
	}

	listed, err := ListGroups(configPath)
	if err != nil {
		t.Fatalf("ListGroups after provider update: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 groups preserved after SaveCloudProvider, got %d", len(listed))
	}

	// Verify providers, accounts, and hosts are still intact in config.json.
	doc, err := s.Load()
	if err != nil {
		t.Fatalf("Load doc: %v", err)
	}
	if !strings.Contains(string(doc["accounts"]), `"acc-1"`) || !strings.Contains(string(doc["accounts"]), `"primary"`) {
		t.Fatalf("accounts field corrupted: %s", string(doc["accounts"]))
	}
	if !strings.Contains(string(doc["hosts"]), `"workbuddy"`) || !strings.Contains(string(doc["hosts"]), `13145`) {
		t.Fatalf("hosts field corrupted: %s", string(doc["hosts"]))
	}
	providersList, err := s.CloudProviders()
	if err != nil || len(providersList) != 1 || providersList[0].Name != "OpenAI Main Updated" {
		t.Fatalf("providers corrupted after group saves: %v, err=%v", providersList, err)
	}

	// 6. Delete group g2 and verify only g1 remains.
	deleted, err := DeleteGroup(configPath, "fallback-chain")
	if err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if !deleted {
		t.Fatalf("expected DeleteGroup to return true")
	}
	deletedAgain, err := DeleteGroup(configPath, "fallback-chain")
	if err != nil {
		t.Fatalf("DeleteGroup non-existent: %v", err)
	}
	if deletedAgain {
		t.Fatalf("expected second DeleteGroup to return false")
	}
	listedAfterDel, err := ListGroups(configPath)
	if err != nil {
		t.Fatalf("ListGroups after delete: %v", err)
	}
	if len(listedAfterDel) != 1 || listedAfterDel[0].ID != "smart-coding-pool" {
		t.Fatalf("unexpected groups after delete: %+v", listedAfterDel)
	}
}

func TestGroupNormalizationAndOrphanCleanup(t *testing.T) {
	s, layout := newGroupTestStore(t)
	if _, err := s.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	configPath := layout.ConfigFile()

	raw := Group{
		ID:      "  weighted-pool  ",
		Name:    "", // should default to ID
		Members: []string{" p1/m1 ", "p2/m2", "p1/m1", "", "   "},
		Match:   []string{" claude-* ", "claude-*", ""},
		Matched: []string{"p3/claude-3-7-sonnet"}, // runtime-derived: allows Off/Weights during save, then cleared
		Routing: Weighted,
		Off:     []string{" p2/m2 ", "p2/m2", "orphan/model", "p3/claude-3-7-sonnet"},
		Levels:  []string{" low ", "high", "low"},
		Fast:    []string{" p1/m1 ", "p1/m1"},
		Weights: map[string]int{
			"p1/m1":                70,
			"p2/m2":                30,
			"p3/claude-3-7-sonnet": 20,
			"orphan/model":         50, // not in Members or Matched -> pruned
			"zero/weight":          0,  // <= 0 -> pruned
			"neg/weight":           -5, // <= 0 -> pruned
		},
		Auto: true, // runtime-derived -> cleared on save
	}

	saved, err := SaveGroup(configPath, raw)
	if err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	if saved.ID != "weighted-pool" {
		t.Fatalf("expected ID 'weighted-pool', got %q", saved.ID)
	}
	if saved.Name != "weighted-pool" {
		t.Fatalf("expected Name defaulted to 'weighted-pool', got %q", saved.Name)
	}
	if !reflect.DeepEqual(saved.Members, []string{"p1/m1", "p2/m2"}) {
		t.Fatalf("unexpected normalized Members: %v", saved.Members)
	}
	if !reflect.DeepEqual(saved.Match, []string{"claude-*"}) {
		t.Fatalf("unexpected normalized Match: %v", saved.Match)
	}
	if saved.Matched != nil {
		t.Fatalf("expected Matched cleared to nil on save, got %v", saved.Matched)
	}
	if saved.Auto {
		t.Fatalf("expected Auto cleared to false on save")
	}
	if !reflect.DeepEqual(saved.Off, []string{"p2/m2", "p3/claude-3-7-sonnet"}) {
		t.Fatalf("expected orphan Off pruned, got %v", saved.Off)
	}
	wantWeights := map[string]int{
		"p1/m1":                70,
		"p2/m2":                30,
		"p3/claude-3-7-sonnet": 20,
	}
	if !reflect.DeepEqual(saved.Weights, wantWeights) {
		t.Fatalf("expected orphan/non-positive Weights pruned, got %v, want %v", saved.Weights, wantWeights)
	}
	if !reflect.DeepEqual(saved.Levels, []string{"low", "high"}) {
		t.Fatalf("unexpected normalized Levels: %v", saved.Levels)
	}
	if !reflect.DeepEqual(saved.Fast, []string{"p1/m1"}) {
		t.Fatalf("unexpected normalized Fast: %v", saved.Fast)
	}

	// Verify validation errors for empty ID+Name, overly long ID, and control characters.
	if _, err := NormalizeGroup(Group{}); err == nil {
		t.Fatalf("expected error when both ID and Name are empty")
	}
	if _, err := NormalizeGroup(Group{ID: strings.Repeat("a", 129), Name: "Too Long"}); err == nil {
		t.Fatalf("expected error when ID exceeds 128 characters")
	}
	if _, err := NormalizeGroup(Group{ID: "bad\nid", Name: "Bad"}); err == nil {
		t.Fatalf("expected error when ID contains newline")
	}
	if _, err := NormalizeGroup(Group{ID: "bad space", Name: "Bad"}); err == nil {
		t.Fatalf("expected error when ID contains space")
	}
}
