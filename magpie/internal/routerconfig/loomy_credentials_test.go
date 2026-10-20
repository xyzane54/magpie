package routerconfig

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestLoomyCredentialDefaultUsesStoredProvider(t *testing.T) {
	s := providerSetup(t)
	apps, err := s.Apps()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range apps {
		if app.ID == "loomy" {
			found = true
			if app.CredentialMode != StoredCredential || app.Configured {
				t.Fatal("unconfigured Loomy must default to stored credentials")
			}
		}
	}
	if !found {
		t.Fatal("missing Loomy preset")
	}
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveApp(AppEdit{ID: "loomy", Name: "Loomy", CredentialMode: StoredCredential, Cloud: AppCloud{ProviderID: p.ID, Model: "loomy-stored-model"}})
	if err != nil {
		t.Fatalf("Loomy stored Profile rejected: %v", err)
	}
	for _, headers := range [][2]string{{"", ""}, {"Bearer fake-session", ""}, {"", "fake-client-key"}, {"Basic ordinary-placeholder", "fake-client-key"}} {
		target, err := s.ResolveApp("loomy", headers[0], headers[1], "fake-management-token")
		if err != nil {
			t.Fatal(err)
		}
		if target.Authorization != "" || target.Provider.APIKey != "test-provider-key" || target.Provider.Config.ID != p.ID || target.Provider.Config.Model != "loomy-stored-model" {
			t.Fatal("host credentials changed Loomy's stored target or credential")
		}
	}
}

func TestLoomyCredentialMissingStoredKeyNeverBorrows(t *testing.T) {
	s := providerSetup(t)
	other, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(AppEdit{ID: "other-agent", Name: "Other", CredentialMode: StoredCredential, Cloud: AppCloud{ProviderID: other.ID, Model: "other-model"}}); err != nil {
		t.Fatal(err)
	}
	empty := providerDraft()
	empty.Name, empty.APIKey, empty.Headers = "No key", nil, nil
	p, err := s.SaveCloudProvider(empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(AppEdit{ID: "loomy", Name: "Loomy", CredentialMode: StoredCredential, Cloud: AppCloud{ProviderID: p.ID, Model: "loomy-model"}}); err != nil {
		t.Fatalf("Loomy stored Profile rejected: %v", err)
	}
	for _, headers := range [][2]string{{"", ""}, {"Bearer fake-session", "fake-client-key"}, {"Bearer test-provider-key", "test-provider-key"}} {
		if _, err := s.ResolveApp("loomy", headers[0], headers[1]); !errors.Is(err, ErrMissingCredential) {
			t.Fatalf("missing bound key borrowed another credential: %v", err)
		}
	}
	if target, err := s.ResolveApp("other-agent", "", ""); err != nil || target.Provider.Config.ID != other.ID || target.Provider.APIKey != "test-provider-key" {
		t.Fatal("Loomy resolution affected another application's credential")
	}
}

func TestLoomyCredentialLegacyForwardModeRemainsExplicit(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(AppEdit{ID: "loomy", Name: "Loomy", CredentialMode: ForwardAuthorization, Cloud: AppCloud{ProviderID: p.ID, Model: "session-model"}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	reopened := New(s.layout)
	if _, err := reopened.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range []string{"", "ordinary-placeholder", "Basic fake-session", "Bearer ", "Bearer fake session"} {
		if _, err := reopened.ResolveApp("loomy", authorization, "test-provider-key"); !errors.Is(err, ErrMissingCredential) {
			t.Fatalf("explicit session mode accepted missing or invalid Bearer: %v", err)
		}
	}
	target, err := reopened.ResolveApp("loomy", "Bearer fake-session", "fake-client-key")
	if err != nil {
		t.Fatal(err)
	}
	if target.Profile.CredentialMode != ForwardAuthorization || target.Authorization != "Bearer fake-session" || target.Provider.APIKey != "" || target.Provider.Config.ID != p.ID {
		t.Fatal("legacy explicit session mode changed or used a stored key")
	}
	after, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reading legacy session mode rewrote its configuration")
	}
}
