package routerconfig

import (
	"encoding/json"
	"testing"
)

func boolPtr(v bool) *bool { return &v }
func slicePtr(v ...string) *[]string {
	cp := append([]string(nil), v...)
	return &cp
}

func findApp(t *testing.T, s *Store, id string) AppProfile {
	t.Helper()
	apps, err := s.Apps()
	if err != nil {
		t.Fatalf("Apps() error: %v", err)
	}
	for _, a := range apps {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("app %q not found", id)
	return AppProfile{}
}

func TestAppMultiModelsAndEnabledSchema(t *testing.T) {
	s := providerSetup(t)

	// Create two cloud providers: openaiProv and dsProv
	openaiDraft := providerDraft()
	openaiDraft.Name = "OpenAI"
	openaiProv, err := s.SaveCloudProvider(openaiDraft)
	if err != nil {
		t.Fatalf("save openai provider: %v", err)
	}

	dsDraft := providerDraft()
	dsDraft.Name = "DeepSeek"
	dsProv, err := s.SaveCloudProvider(dsDraft)
	if err != nil {
		t.Fatalf("save deepseek provider: %v", err)
	}

	// 1. Multi-select models + enabled persistence for loomy and workbuddy
	// Mix of group/<id>, local/<model>, and <providerId>/<model>, with whitespace and duplicate to verify normalization.
	loomyEdit := AppEdit{
		ID:             "loomy",
		Name:           "Loomy",
		CredentialMode: StoredCredential,
		Enabled:        boolPtr(true),
		Models:         slicePtr("  group/auto-glm-5 ", "local/spark-x2.5-1.7b", openaiProv.ID+"/gpt-4o", "group/auto-glm-5", ""),
	}
	savedLoomy, err := s.SaveApp(loomyEdit)
	if err != nil {
		t.Fatalf("SaveApp(loomy) failed: %v", err)
	}
	wantLoomyModels := []string{"group/auto-glm-5", "local/spark-x2.5-1.7b", openaiProv.ID + "/gpt-4o"}
	if !savedLoomy.Enabled || !savedLoomy.Configured {
		t.Fatalf("savedLoomy expected Enabled=true, Configured=true; got Enabled=%v, Configured=%v", savedLoomy.Enabled, savedLoomy.Configured)
	}
	if len(savedLoomy.Models) != len(wantLoomyModels) {
		t.Fatalf("savedLoomy.Models = %v, want %v", savedLoomy.Models, wantLoomyModels)
	}
	for i := range wantLoomyModels {
		if savedLoomy.Models[i] != wantLoomyModels[i] {
			t.Fatalf("savedLoomy.Models[%d] = %q, want %q", i, savedLoomy.Models[i], wantLoomyModels[i])
		}
	}
	// Because Models[0] is "group/auto-glm-5" and Cloud was empty, Cloud remains empty.
	if savedLoomy.Cloud.ProviderID != "" || savedLoomy.Cloud.Model != "" {
		t.Fatalf("expected empty Cloud when Models[0] is group/*, got %+v", savedLoomy.Cloud)
	}

	readLoomy := findApp(t, s, "loomy")
	if !readLoomy.Enabled || !readLoomy.Configured || len(readLoomy.Models) != 3 {
		t.Fatalf("reloaded loomy mismatch: %+v", readLoomy)
	}

	// Incremental save omitting Enabled (nil) must preserve Enabled=true
	loomyRename := AppEdit{
		ID:             "loomy",
		Revision:       readLoomy.Revision,
		Name:           "Loomy Agent",
		CredentialMode: StoredCredential,
		Models:         slicePtr("local/spark-x2.5-4b"),
	}
	savedLoomy2, err := s.SaveApp(loomyRename)
	if err != nil {
		t.Fatalf("incremental SaveApp(loomy) failed: %v", err)
	}
	if !savedLoomy2.Enabled || !savedLoomy2.Configured || len(savedLoomy2.Models) != 1 || savedLoomy2.Models[0] != "local/spark-x2.5-4b" {
		t.Fatalf("expected Enabled=true preserved and Models=[local/spark-x2.5-4b], got %+v", savedLoomy2)
	}

	// Explicitly setting Enabled=false and empty Models clears configuration
	loomyDisable := AppEdit{
		ID:             "loomy",
		Revision:       savedLoomy2.Revision,
		Name:           "Loomy Agent",
		CredentialMode: StoredCredential,
		Enabled:        boolPtr(false),
		Models:         slicePtr(),
	}
	savedLoomy3, err := s.SaveApp(loomyDisable)
	if err != nil {
		t.Fatalf("disable SaveApp(loomy) failed: %v", err)
	}
	if savedLoomy3.Enabled || savedLoomy3.Configured || len(savedLoomy3.Models) != 0 {
		t.Fatalf("expected disabled & unconfigured loomy, got %+v", savedLoomy3)
	}

	// 2. When Models[0] is a cloud provider model ("<openaiProv.ID>/gpt-4o") and Cloud is omitted, Cloud auto-syncs to Models[0]
	wbEdit := AppEdit{
		ID:             "workbuddy",
		Name:           "WorkBuddy",
		CredentialMode: StoredCredential,
		Enabled:        boolPtr(true),
		Models:         slicePtr(openaiProv.ID+"/gpt-4o", "group/coding", dsProv.ID+"/deepseek-chat"),
	}
	savedWB, err := s.SaveApp(wbEdit)
	if err != nil {
		t.Fatalf("SaveApp(workbuddy) failed: %v", err)
	}
	if savedWB.Cloud.ProviderID != openaiProv.ID || savedWB.Cloud.Model != "gpt-4o" {
		t.Fatalf("expected Cloud synced to %s/gpt-4o, got %+v", openaiProv.ID, savedWB.Cloud)
	}
	if !savedWB.Enabled || !savedWB.Configured || len(savedWB.Models) != 3 {
		t.Fatalf("unexpected workbuddy state: %+v", savedWB)
	}

	// Verify legacy single-Cloud save (Models == nil) auto-populates Models = ["<dsProv.ID>/deepseek-reasoner"] and preserves Enabled
	wbLegacyEdit := AppEdit{
		ID:             "workbuddy",
		Revision:       savedWB.Revision,
		Name:           "WorkBuddy",
		CredentialMode: StoredCredential,
		Cloud:          AppCloud{ProviderID: dsProv.ID, Model: "deepseek-reasoner"},
	}
	savedWBLegacy, err := s.SaveApp(wbLegacyEdit)
	if err != nil {
		t.Fatalf("legacy SaveApp(workbuddy) failed: %v", err)
	}
	if !savedWBLegacy.Enabled || len(savedWBLegacy.Models) != 1 || savedWBLegacy.Models[0] != dsProv.ID+"/deepseek-reasoner" {
		t.Fatalf("expected Models=[%s/deepseek-reasoner] and Enabled=true, got %+v", dsProv.ID, savedWBLegacy)
	}

	// 3. Legacy config.json read compatibility (no "models" or "enabled" fields in raw JSON)
	if err := s.Update(func(d Document) error {
		var apps map[string]json.RawMessage
		if err := json.Unmarshal(d["apps"], &apps); err != nil {
			return err
		}
		apps["codex"] = json.RawMessage(`{
			"schemaVersion": 1,
			"id": "codex",
			"revision": 5,
			"name": "Codex",
			"cloud": {"providerId": "` + openaiProv.ID + `", "model": "gpt-4.1", "capabilities": null},
			"credentialMode": "stored",
			"localRuntime": "shared",
			"redactionEnabled": false,
			"referencePrice": null
		}`)
		d["apps"], _ = json.Marshal(apps)
		return nil
	}); err != nil {
		t.Fatalf("inject legacy codex JSON failed: %v", err)
	}
	legacyCodex := findApp(t, s, "codex")
	if legacyCodex.Enabled {
		t.Fatalf("expected legacy codex Enabled=false, got true")
	}
	if !legacyCodex.Configured {
		t.Fatalf("expected legacy codex Configured=true, got false")
	}
	if len(legacyCodex.Models) != 1 || legacyCodex.Models[0] != openaiProv.ID+"/gpt-4.1" {
		t.Fatalf("expected legacy codex Models=[%s/gpt-4.1], got %v", openaiProv.ID, legacyCodex.Models)
	}

	// 4. Invalid model target formats and missing provider rejection
	invalidCases := [][]string{
		{"invalid-no-slash"},
		{"group/"},
		{"local/"},
		{"/model-only"},
		{openaiProv.ID + "/"},
		{"group/bad id"},
		{openaiProv.ID + "/bad\nmodel"},
		{"unknown-prov/gpt-4"},
	}
	for _, badModels := range invalidCases {
		_, err := s.SaveApp(AppEdit{
			ID:             "astudio",
			Name:           "astudio",
			CredentialMode: StoredCredential,
			Models:         slicePtr(badModels...),
		})
		if err == nil {
			t.Fatalf("expected SaveApp to reject invalid models %v, but it succeeded", badModels)
		}
	}
}
