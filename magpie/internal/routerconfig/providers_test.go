package routerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/steady"
	"os"
	"runtime"
	"strings"
	"testing"
)

func providerDraft() ProviderEdit {
	key := "test-provider-key"
	headers := map[string]string{"X-Tenant": "test-private-tenant"}
	body := map[string]json.RawMessage{"prompt_cache_key": json.RawMessage(`"${sessionId}"`)}
	proxy := "http://user:private-proxy-password@127.0.0.1:1080"
	return ProviderEdit{Name: "My GLM", Preset: "zhipu", Protocol: "chat", BaseURL: "https://example.invalid/v1", Model: "model-a", APIKey: &key, Headers: &headers, ExtraBody: &body, ProxyMode: "custom", ProxyURL: &proxy}
}
func providerSetup(t *testing.T) *Store {
	t.Helper()
	s, p := setup(t)
	put(t, p, fixture)
	return s
}

func TestProviderConfigurationKeepsSecretsSeparateAndStable(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	saved, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Revision != 1 || !saved.HasKey || !saved.HasExtraBody || !saved.HasProxyURL || len(saved.HeaderNames) != 1 {
		t.Fatalf("saved: %+v", saved)
	}
	list, err := New(s.layout).CloudProviders()
	if err != nil || len(list) != 1 {
		t.Fatalf("reload: %+v %v", list, err)
	}
	public, _ := json.Marshal(list)
	config, _ := os.ReadFile(s.layout.ConfigFile())
	for _, secret := range []string{*in.APIKey, (*in.Headers)["X-Tenant"], "private-proxy-password", "${sessionId}"} {
		if bytes.Contains(public, []byte(secret)) || bytes.Contains(config, []byte(secret)) {
			t.Fatal("secret present in management JSON or ordinary config")
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.layout.CredentialsFile())
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode: %v %v", fi, err)
		}
	}
	// Edit without typing the saved secrets again, exactly as the masked editor does.
	edit := ProviderEdit{ID: saved.ID, Revision: saved.Revision, Name: "Renamed", Preset: "zhipu", Protocol: "chat", BaseURL: saved.BaseURL, Model: "model-b", ProxyMode: "custom"}
	next, err := s.SaveCloudProvider(edit)
	if err != nil {
		t.Fatal(err)
	}
	edit.Revision = next.Revision
	resolved, err := s.ResolveCloudProvider(edit)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != *in.APIKey || resolved.ProxyURL != *in.ProxyURL || resolved.Headers["X-Tenant"] != "test-private-tenant" || string(resolved.ExtraBody["prompt_cache_key"]) != `"${sessionId}"` {
		t.Fatal("masked edit lost saved options")
	}
	if _, err := s.SaveCloudProvider(ProviderEdit{ID: saved.ID, Revision: 1}); !errors.Is(err, ErrProviderConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := s.SaveCloudProvider(ProviderEdit{ID: "not-this-provider", Revision: 1}); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("cross-provider fallback: %v", err)
	}
	var old Document
	oldBytes, err := os.ReadFile(s.layout.ConfigFile() + ".bak")
	if err != nil || json.Unmarshal(oldBytes, &old) != nil {
		t.Fatal(err)
	}
	var oldProviders map[string]map[string]json.RawMessage
	_ = json.Unmarshal(old["providers"], &oldProviders)
	var oldRef string
	_ = json.Unmarshal(oldProviders[saved.ID]["credentialRef"], &oldRef)
	f, err := s.providerFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(f.secrets[oldRef], []byte(*in.APIKey)) {
		t.Fatal("backup credential reference was destroyed")
	}
	if !bytes.Contains(config, []byte(`"future"`)) {
		t.Fatal("unknown config field lost")
	}
}

func TestProviderInvalidOrDamagedInputDoesNotOverwrite(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	p, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(s.layout.ConfigFile())
	for _, test := range []struct {
		name   string
		change func(*ProviderEdit)
	}{
		{"protocol", func(p *ProviderEdit) { p.Protocol = "gemini" }},
		{"URL credentials", func(p *ProviderEdit) { p.BaseURL = "https://user:secret@example.invalid/v1" }},
		{"URL query", func(p *ProviderEdit) { p.BaseURL = "https://example.invalid/v1?api-key=secret" }},
		{"empty URL query", func(p *ProviderEdit) { p.BaseURL = "https://example.invalid/v1?" }},
		{"header injection", func(p *ProviderEdit) { h := map[string]string{"X-Client": "ok\r\nSecret: bad"}; p.Headers = &h }},
		{"transport header", func(p *ProviderEdit) { h := map[string]string{"Host": "other.invalid"}; p.Headers = &h }},
		{"duplicate header", func(p *ProviderEdit) {
			h := map[string]string{"Authorization": "a", "authorization": "b"}
			p.Headers = &h
		}},
		{"history override", func(p *ProviderEdit) {
			b := map[string]json.RawMessage{"!messages": json.RawMessage(`[]`)}
			p.ExtraBody = &b
		}},
		{"model override", func(p *ProviderEdit) {
			b := map[string]json.RawMessage{"model": json.RawMessage(`"other"`)}
			p.ExtraBody = &b
		}},
		{"bad proxy", func(p *ProviderEdit) { proxy := "file:///etc/passwd"; p.ProxyURL = &proxy }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := in
			bad.ID = p.ID
			bad.Revision = p.Revision
			test.change(&bad)
			if _, err := s.SaveCloudProvider(bad); err == nil {
				t.Fatal("invalid provider accepted")
			}
			after, _ := os.ReadFile(s.layout.ConfigFile())
			if !bytes.Equal(original, after) {
				t.Fatal("invalid input changed config")
			}
		})
	}
	credentialBefore, _ := os.ReadFile(s.layout.CredentialsFile())
	for _, corrupt := range []string{"{broken", `{"schemaVersion":99,"providers":{}}`, `{"schemaVersion":1,"providers":null}`} {
		put(t, s.layout.CredentialsFile(), corrupt)
		if _, err := s.CloudProviders(); err == nil {
			t.Fatal("damaged credentials reported empty")
		}
		if _, err := s.SaveCloudProvider(in); err == nil {
			t.Fatal("damaged credentials accepted")
		}
		unchanged(t, s.layout.CredentialsFile(), corrupt)
		after, _ := os.ReadFile(s.layout.ConfigFile())
		if !bytes.Equal(original, after) {
			t.Fatal("damaged credentials changed config")
		}
	}
	put(t, s.layout.CredentialsFile(), string(credentialBefore))
	if err := os.Remove(s.layout.CredentialsFile()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCloudProvider(in); err == nil {
		t.Fatal("missing credential store silently replaced")
	}
}

func TestProviderFailedConfigWriteKeepsCurrentAndBackupCredentials(t *testing.T) {
	s := providerSetup(t)
	in := providerDraft()
	p, err := s.SaveCloudProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.layout.ConfigFile())
	sync := steady.Sync
	t.Cleanup(func() { steady.Sync = sync })
	steady.Sync = func(f *os.File) error {
		if strings.Contains(f.Name(), ".config.json.") {
			return errors.New("injected config flush failure")
		}
		return f.Sync()
	}
	in.ID = p.ID
	in.Revision = p.Revision
	replacement := "replacement-key"
	in.APIKey = &replacement
	if _, err := s.SaveCloudProvider(in); err == nil {
		t.Fatal("injected failure reported saved")
	}
	after, _ := os.ReadFile(s.layout.ConfigFile())
	if !bytes.Equal(before, after) {
		t.Fatal("failed save changed current config")
	}
	resolve := ProviderEdit{ID: p.ID, Revision: p.Revision, Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL, Model: p.Model, ProxyMode: p.ProxyMode}
	current, err := s.ResolveCloudProvider(resolve)
	if err != nil || current.APIKey != "test-provider-key" {
		t.Fatalf("current key lost: %v", err)
	}
}

func TestProviderDeleteAndReferences(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(d Document) error {
		app := defaultApp("codex", "Codex")
		app.Revision = 1
		app.Cloud = AppCloud{ProviderID: p.ID, Model: "model-a"}
		d["apps"], _ = json.Marshal(map[string]AppProfile{"codex": app})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCloudProvider(p.ID, p.Revision); err == nil {
		t.Fatal("deleted a referenced provider")
	}
	if err := s.Update(func(d Document) error { d["apps"] = json.RawMessage(`{}`); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCloudProvider(p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	list, err := s.CloudProviders()
	if err != nil || len(list) != 0 {
		t.Fatalf("delete not persisted: %+v %v", list, err)
	}
	if err := s.DeleteCloudProvider(p.ID, p.Revision); !errors.Is(err, ErrProviderNotFound) {
		t.Fatal(err)
	}
	credBytes, err := os.ReadFile(s.layout.CredentialsFile())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(credBytes, []byte("test-provider-key")) {
		t.Fatalf("deleted provider API key remained in credentials.json: %s", credBytes)
	}
}
