package routerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestApplicationRejectsDecodedCredentialsInExtraBody(t *testing.T) {
	const management = "management-audit-secret-12345"
	for _, id := range []string{"codex", "loomy"} {
		for _, secret := range []struct{ name, value string }{{"management", management}, {"embedded-management", "prefix:" + management + ":suffix"}} {
			for _, encoding := range []string{"plaintext", "unicode"} {
				for _, shape := range []string{"string", "nested-object", "nested-array", "object-key"} {
					t.Run(id+"/"+secret.name+"/"+encoding+"/"+shape, func(t *testing.T) {
						s := providerSetup(t)
						encoded, _ := json.Marshal(secret.value)
						if encoding == "unicode" {
							encoded = bytes.ReplaceAll(encoded, []byte(secret.value[:1]), []byte(`\u00`+map[byte]string{'p': "70", 'm': "6d"}[secret.value[0]]))
						}
						value := string(encoded)
						switch shape {
						case "nested-object":
							value = `{"nested":{"value":` + value + `}}`
						case "nested-array":
							value = `[null,true,9007199254740993,{"nested":[` + value + `]}]`
						case "object-key":
							value = `{` + value + `:true}`
						}
						draft := providerDraft()
						extra := map[string]json.RawMessage{"audit_metadata": json.RawMessage(value)}
						draft.ExtraBody = &extra
						provider, err := s.SaveCloudProvider(draft)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := s.SaveApp(appDraft("codex", provider.ID)); err != nil {
							t.Fatal(err)
						}
						if _, err := s.SaveApp(appDraft("claude-code", provider.ID)); err != nil {
							t.Fatal(err)
						}
						auth := "Bearer placeholder"
						if id == "loomy" {
							if _, err := s.SaveApp(appDraft(id, provider.ID)); err != nil {
								t.Fatal(err)
							}
							auth = "Bearer loomy-dynamic-audit"
						}
						beforeConfig, err := os.ReadFile(s.layout.ConfigFile())
						if err != nil {
							t.Fatal(err)
						}
						beforeCredentials, err := os.ReadFile(s.layout.CredentialsFile())
						if err != nil {
							t.Fatal(err)
						}
						target, err := s.ResolveApp(id, auth, "", management)
						if !errors.Is(err, ErrCredentialMisuse) {
							t.Fatalf("decoded %s credential accepted into %s extra body: %v", secret.name, shape, err)
						}
						if target.Provider.APIKey != "" || target.Authorization != "" {
							t.Fatal("failed resolution returned credentials")
						}
						if strings.Contains(err.Error(), secret.value) {
							t.Fatal("credential appeared in error")
						}
						unchanged(t, s.layout.ConfigFile(), string(beforeConfig))
						unchanged(t, s.layout.CredentialsFile(), string(beforeCredentials))
					})
				}
			}
		}
	}
}

func TestApplicationRejectsJSONEscapedSpecialCharacterCredentials(t *testing.T) {
	s := providerSetup(t)
	management := "management-<>&\"\\-123456"
	encoded, _ := json.Marshal("prefix:" + management + ":suffix")
	draft := providerDraft()
	extra := map[string]json.RawMessage{"metadata": encoded}
	draft.ExtraBody = &extra
	provider, err := s.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("codex", provider.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveApp("codex", "Bearer placeholder", "", management); !errors.Is(err, ErrCredentialMisuse) {
		t.Fatalf("JSON escaped credential was accepted: %v", err)
	}
}

func TestApplicationExtraBodyCheckPreservesNumbersAndLiteralEscapes(t *testing.T) {
	s := providerSetup(t)
	draft := providerDraft()
	// A JSON string containing literal backslash-u text is not the decoded key.
	extra := map[string]json.RawMessage{
		"large":    json.RawMessage(`90071992547409931234567890`),
		"exponent": json.RawMessage(`1e400`),
		"nested":   json.RawMessage(`{"values":[null,false,-0.000001,"ordinary",{"literal":"\\u006danagement-audit-secret-12345"}]}`),
	}
	draft.ExtraBody = &extra
	provider, err := s.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("codex", provider.ID)); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	beforeCredentials, err := os.ReadFile(s.layout.CredentialsFile())
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.ResolveApp("codex", "Bearer placeholder", "", "management-audit-secret-12345")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range extra {
		var compact bytes.Buffer
		if err := json.Compact(&compact, target.Provider.ExtraBody[name]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(compact.Bytes(), want) {
			t.Fatalf("extra body %s changed: got %s want %s", name, target.Provider.ExtraBody[name], want)
		}
	}
	unchanged(t, s.layout.ConfigFile(), string(beforeConfig))
	unchanged(t, s.layout.CredentialsFile(), string(beforeCredentials))
}
