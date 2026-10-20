package routerconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestProviderModelRetirementPreservesProfilesAndLegacyReads(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	in.Model = ""
	p, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal("provider must save without a model:", err)
	}
	app, err := s.SaveApp(AppEdit{ID: "codex", Name: "Codex", Cloud: AppCloud{ProviderID: p.ID, Model: "profile-model"}, CredentialMode: StoredCredential})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an old provider default, including an obsolete value that is no
	// longer interpreted, and a future sibling field that must survive edits.
	if err := s.Update(func(d Document) error {
		var providers map[string]map[string]json.RawMessage
		if err := json.Unmarshal(d["providers"], &providers); err != nil {
			return err
		}
		providers[p.ID]["model"] = json.RawMessage(`{"obsolete":"not a current model"}`)
		providers[p.ID]["futureProvider"] = json.RawMessage(`{"keep":true}`)
		d["providers"], _ = json.Marshal(providers)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	list, err := New(s.layout).CloudProviders()
	if err != nil || len(list) != 1 || list[0].Model != "" {
		t.Fatalf("legacy provider read: %v %v", list, err)
	}
	public, _ := json.Marshal(list)
	if bytes.Contains(public, []byte(`"model"`)) {
		t.Fatal("provider still exposes a default model")
	}
	afterRead, _ := os.ReadFile(s.layout.ConfigFile())
	if !bytes.Equal(before, afterRead) {
		t.Fatal("legacy read silently migrated config")
	}
	in.ID, in.Revision, in.APIKey, in.Headers, in.ExtraBody, in.ProxyURL = p.ID, p.Revision, nil, nil, nil, nil
	in.Model = "old-client-value-ignored"
	next, err := s.SaveCloudProvider(in)
	if err != nil || next.Revision != p.Revision+1 {
		t.Fatalf("model-free edit: %v %v", next, err)
	}
	f, err := s.providerFiles()
	if err != nil {
		t.Fatal(err)
	}
	resolved, fields, err := f.resolved(p.ID)
	var future struct{ Keep bool }
	if err != nil || fields["model"] != nil || json.Unmarshal(fields["futureProvider"], &future) != nil || !future.Keep || resolved.APIKey != "test-provider-key" {
		t.Fatal("provider retirement lost credentials/future fields or retained a default model")
	}
	backup, _ := os.ReadFile(s.layout.ConfigFile() + ".bak")
	if !bytes.Equal(backup, before) {
		t.Fatal("legacy configuration backup was not preserved")
	}
	target, err := s.ResolveApp("codex", "Bearer placeholder", "")
	if err != nil || target.Profile.Cloud.Model != "profile-model" || target.Profile.Revision != app.Revision || target.Provider.Config.Model != "profile-model" {
		t.Fatalf("provider edit changed the Profile target: %v %v", target, err)
	}
}
