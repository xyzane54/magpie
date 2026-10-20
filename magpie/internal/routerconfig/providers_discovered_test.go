package routerconfig

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestProviderOrphanSecretsPrunedOnRepeatedSaves(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	firstKey := "first-secret-key"
	in.APIKey = &firstKey
	p1, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.Revision = p1.ID, p1.Revision
	secondKey := "second-secret-key"
	in.APIKey = &secondKey
	p2, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.Revision = p2.ID, p2.Revision
	thirdKey := "third-secret-key"
	in.APIKey = &thirdKey
	if _, err := s.SaveCloudProvider(in); err != nil {
		t.Fatal(err)
	}
	f, err := s.providerFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.secrets) != 2 {
		t.Fatalf("expected at most 2 credentialRef entries (current and .bak), got %d", len(f.secrets))
	}
	credBytes, err := os.ReadFile(s.layout.CredentialsFile())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(credBytes, []byte(firstKey)) {
		t.Fatalf("first secret should be pruned after 3 saves: %s", credBytes)
	}
	if !bytes.Contains(credBytes, []byte(secondKey)) || !bytes.Contains(credBytes, []byte(thirdKey)) {
		t.Fatalf("expected .bak (second) and current (third) secrets to remain: %s", credBytes)
	}
}

func TestProviderDiscoveredModelsPersistenceAndValidation(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	rawModels := []string{"  gpt-4o ", "o3-mini", "gpt-4o", "", "   ", "gpt-4.1"}
	in.DiscoveredModels = &rawModels
	saved, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-4o", "o3-mini", "gpt-4.1"}
	if !reflect.DeepEqual(saved.DiscoveredModels, want) {
		t.Fatalf("saved DiscoveredModels = %#v, want %#v", saved.DiscoveredModels, want)
	}

	// Editing provider with DiscoveredModels == nil must preserve cached models.
	editKeep := providerDraft()
	editKeep.ID, editKeep.Revision = saved.ID, saved.Revision
	editKeep.Name = "Renamed Provider"
	editKeep.APIKey = nil
	kept, err := s.SaveCloudProvider(editKeep)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept.DiscoveredModels, want) {
		t.Fatalf("DiscoveredModels lost after nil edit: got %#v, want %#v", kept.DiscoveredModels, want)
	}
	list, err := s.CloudProviders()
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0].DiscoveredModels, want) {
		t.Fatalf("CloudProviders DiscoveredModels = %+v, err = %v", list, err)
	}

	// Explicit empty slice clears cached models.
	empty := []string{"", "  "}
	editClear := editKeep
	editClear.Revision = kept.Revision
	editClear.DiscoveredModels = &empty
	cleared, err := s.SaveCloudProvider(editClear)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.DiscoveredModels) != 0 {
		t.Fatalf("expected cleared DiscoveredModels, got %#v", cleared.DiscoveredModels)
	}

	// Validation: illegal control characters, >256 bytes, >1000 models.
	tooMany := make([]string, 1001)
	for i := range tooMany {
		tooMany[i] = "model-" + strings.Repeat("a", i%5) + string(rune('A'+i%26)) + string(rune('0'+(i/26)%10)) + string(rune('0'+(i/260)%10))
	}
	for _, bad := range [][]string{
		{"valid-model", "bad\nmodel"},
		{"bad\rmodel"},
		{"bad\x00model"},
		{strings.Repeat("m", 257)},
		tooMany,
	} {
		invalidIn := providerDraft()
		modelsCopy := bad
		invalidIn.DiscoveredModels = &modelsCopy
		if _, err := s.SaveCloudProvider(invalidIn); err == nil {
			t.Fatalf("accepted invalid DiscoveredModels: %v", bad[:1])
		} else if _, code := ProviderError(err); code != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid DiscoveredModels, got %d (%v)", code, err)
		}
	}
}

func TestSaveProviderDiscoveredModelsAndLegacyConfigCompatibility(t *testing.T) {
	s := providerSetup(t)
	// Legacy provider saved without discoveredModels field.
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.DiscoveredModels) != 0 {
		t.Fatalf("expected empty DiscoveredModels initially, got %#v", p.DiscoveredModels)
	}

	// Update discoveredModels independently via SaveProviderDiscoveredModelsContext.
	updated, err := s.SaveProviderDiscoveredModelsContext(context.Background(), p.ID, []string{" spark-x2.5 ", "spark-x2.5-4b", "spark-x2.5", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"spark-x2.5", "spark-x2.5-4b"}
	if !reflect.DeepEqual(updated.DiscoveredModels, want) {
		t.Fatalf("SaveProviderDiscoveredModelsContext = %#v, want %#v", updated.DiscoveredModels, want)
	}
	if updated.Revision != p.Revision {
		t.Fatalf("expected Revision %d to stay unchanged on cache update, got %d", p.Revision, updated.Revision)
	}

	// Subsequent SaveCloudProvider without DiscoveredModels keeps the cached models.
	edit := providerDraft()
	edit.ID, edit.Revision = updated.ID, updated.Revision
	edit.Name = "Updated Name After Model Fetch"
	edit.APIKey = nil
	saved, err := s.SaveCloudProvider(edit)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.DiscoveredModels, want) {
		t.Fatalf("SaveCloudProvider dropped DiscoveredModels: got %#v, want %#v", saved.DiscoveredModels, want)
	}

	if _, err := s.SaveProviderDiscoveredModels("p_missing", []string{"m1"}); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("expected ErrProviderNotFound, got %v", err)
	}
}
