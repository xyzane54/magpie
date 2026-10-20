package routerconfig

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestResolveAppDraftCredentialIsolationAndNoWrites(t *testing.T) {
	s := providerSetup(t)
	draft := providerDraft()
	headers := map[string]string{"Authorization": "Bearer saved-override", "x-api-key": "other-override", "X-Private": "safe-value"}
	draft.Headers = &headers
	p, err := s.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := os.ReadFile(s.layout.ConfigFile())
	beforeCreds, _ := os.ReadFile(s.layout.CredentialsFile())
	in := appDraft("codex", p.ID)
	target, err := s.ResolveAppDraft(in, "management-test-token")
	if err != nil {
		t.Fatal(err)
	}
	if target.Provider.Config.ID != p.ID || target.Provider.APIKey == "" || target.Authorization != "" || target.Provider.Config.Model != in.Cloud.Model {
		t.Fatal("draft failed exact credential/model resolution")
	}
	for name := range target.Provider.Headers {
		if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "x-api-key") {
			t.Fatal("saved auth override survived")
		}
	}
	unchanged(t, s.layout.ConfigFile(), string(beforeConfig))
	unchanged(t, s.layout.CredentialsFile(), string(beforeCreds))
}

func TestResolveAppDraftRejectsPrivateManagementCredentials(t *testing.T) {
	const management = "management-draft-123456"
	for _, location := range []string{"apiKey", "headers", "extraBody", "proxyURL", "name", "price", "missingKey"} {
		t.Run(location, func(t *testing.T) {
			s := providerSetup(t)
			draft := providerDraft()
			switch location {
			case "apiKey":
				key := management
				draft.APIKey = &key
			case "headers":
				h := map[string]string{"X-Business": management}
				draft.Headers = &h
			case "extraBody":
				extra := map[string]json.RawMessage{"metadata": json.RawMessage(`{"nested":[{"\u006danagement-draft-123456":true}]}`)}
				draft.ExtraBody = &extra
			case "proxyURL":
				proxy := "http://user:" + management + "@127.0.0.1:1"
				draft.ProxyMode = "custom"
				draft.ProxyURL = &proxy
			case "missingKey":
				draft.APIKey = nil
			}
			p, err := s.SaveCloudProvider(draft)
			if err != nil {
				t.Fatal(err)
			}
			in := appDraft("codex", p.ID)
			if location == "name" {
				in.Name = management
			}
			if location == "price" {
				in.ReferencePrice = &ReferencePrice{Currency: "USD", Unit: "million-tokens", Version: "v1", Source: management}
			}
			beforeConfig, _ := os.ReadFile(s.layout.ConfigFile())
			beforeCreds, _ := os.ReadFile(s.layout.CredentialsFile())
			_, err = s.ResolveAppDraft(in, management)
			want := ErrCredentialMisuse
			if location == "missingKey" {
				want = ErrMissingCredential
			}
			if !errors.Is(err, want) {
				t.Fatalf("private management %s accepted: %v", location, err)
			}
			unchanged(t, s.layout.ConfigFile(), string(beforeConfig))
			unchanged(t, s.layout.CredentialsFile(), string(beforeCreds))
		})
	}
}
