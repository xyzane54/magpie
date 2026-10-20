package routerconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func addLegacyEntryFields(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	if err := s.Update(func(d Document) error {
		var apps Document
		_ = json.Unmarshal(d["apps"], &apps)
		for _, id := range ids {
			apps[id] = mergeObject(apps[id], map[string]any{"entryCredentialRef": "entry_old", "hasEntryKey": true, "entryKey": "obsolete", "clearEntryKey": false, "extension": id})
		}
		d["apps"], _ = json.Marshal(apps)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyEntryReferencesDoNotRequireHashesOrChangeFilesOnRead(t *testing.T) {
	for _, legacy := range []string{`{"entry_old":{"appId":"codex","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`, `{}`, `null`, `{"entry_old":{"sha256":false}}`} {
		t.Run(legacy, func(t *testing.T) {
			s := providerSetup(t)
			provider, err := s.SaveCloudProvider(providerDraft())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"codex", "claude-code"} {
				if _, err := s.SaveApp(appDraft(id, provider.ID)); err != nil {
					t.Fatal(err)
				}
			}
			addLegacyEntryFields(t, s, "codex", "claude-code")
			var credentials Document
			originalCredentials, err := os.ReadFile(s.layout.CredentialsFile())
			if err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(originalCredentials, &credentials)
			credentials["apps"] = json.RawMessage(legacy)
			credentials["extension"] = json.RawMessage(`{"keep":true}`)
			credentialBytes, _ := json.Marshal(credentials)
			put(t, s.layout.CredentialsFile(), string(credentialBytes))
			put(t, s.layout.CredentialsFile()+".bak", "keep-private-backup")
			before, _ := os.ReadFile(s.layout.ConfigFile())
			backup, _ := os.ReadFile(s.layout.ConfigFile() + ".bak")
			reopened := New(s.layout)
			apps, err := reopened.Apps()
			if err != nil {
				t.Fatal(err)
			}
			target, err := reopened.ResolveApp("codex", "", "", "management-test")
			if err != nil || target.Provider.APIKey != "test-provider-key" || target.Profile.Path != "/app/codex/v1" {
				t.Fatalf("legacy entry prevented exact target resolution: %v", err)
			}
			encoded, _ := json.Marshal(apps)
			for _, removed := range []string{"entryCredentialRef", "hasEntryKey", "entryKey", "clearEntryKey"} {
				if bytes.Contains(encoded, []byte(removed)) {
					t.Fatalf("legacy field exposed by Apps: %s", removed)
				}
			}
			unchanged(t, s.layout.ConfigFile(), string(before))
			unchanged(t, s.layout.ConfigFile()+".bak", string(backup))
			edit := appDraft("codex", provider.ID)
			edit.Revision = target.Profile.Revision
			if _, err := reopened.SaveApp(edit); err != nil {
				t.Fatal(err)
			}
			d, err := reopened.Load()
			if err != nil {
				t.Fatal(err)
			}
			var savedApps map[string]Document
			_ = json.Unmarshal(d["apps"], &savedApps)
			for _, field := range []string{"entryCredentialRef", "hasEntryKey", "entryKey", "clearEntryKey"} {
				if savedApps["codex"][field] != nil || savedApps["claude-code"][field] == nil {
					t.Fatalf("save did not limit legacy cleanup to edited app: %s", field)
				}
			}
			if string(savedApps["codex"]["extension"]) != `"codex"` {
				t.Fatal("lost unrelated app data")
			}
			unchanged(t, s.layout.ConfigFile()+".bak", string(before))
			unchanged(t, s.layout.CredentialsFile(), string(credentialBytes))
			unchanged(t, s.layout.CredentialsFile()+".bak", "keep-private-backup")
		})
	}
}

func TestLegacyAppOnlyReferenceDoesNotRequireCredentialFile(t *testing.T) {
	s := providerSetup(t)
	app, err := s.SaveApp(appDraft("codex", ""))
	if err != nil {
		t.Fatal(err)
	}
	addLegacyEntryFields(t, s, "codex")
	edit := appDraft("codex", "")
	edit.Revision = app.Revision
	if _, err := s.SaveApp(edit); err != nil {
		t.Fatalf("unused legacy reference required credentials: %v", err)
	}
	if _, err := os.Stat(s.layout.CredentialsFile()); !os.IsNotExist(err) {
		t.Fatal("created unnecessary credential file")
	}
}
