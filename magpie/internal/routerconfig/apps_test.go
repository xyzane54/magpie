package routerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func appDraft(id, provider string) AppEdit {
	mode := StoredCredential
	if id == "loomy" {
		mode = ForwardAuthorization
	}
	in := AppEdit{ID: id, Name: id, CredentialMode: mode}
	if provider != "" {
		in.Cloud = AppCloud{ProviderID: provider, Model: "app-model-" + id}
	}
	return in
}
func TestApplicationRegistryProfilesAndIsolation(t *testing.T) {
	s := providerSetup(t)
	apps, err := s.Apps()
	if err != nil || len(apps) != 6 {
		t.Fatalf("presets: %d %v", len(apps), err)
	}
	for _, p := range apps {
		if !p.Builtin || p.Configured || p.Revision != 0 || p.RedactionEnabled || p.Cloud.ProviderID != "" || p.LocalRuntime != "shared" || p.Path != "/app/"+p.ID+"/v1" {
			t.Fatalf("false preset readiness: %+v", p)
		}
	}
	for _, id := range []string{"api", "app", "admin", "v1", "Codex", "a_b", "a--b", "-a", "a-", "a/b", "a%2fb", strings.Repeat("a", 49)} {
		if _, err := s.SaveApp(appDraft(id, "")); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	in := appDraft("custom-editor", p.ID)
	in.ReferencePrice = &ReferencePrice{Currency: "CNY", Unit: "million-tokens", Input: 1.5, Output: 2.5, Version: "test-v1", Source: "test"}
	saved, err := s.SaveApp(in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Builtin || !saved.Configured {
		t.Fatalf("custom: %+v", saved)
	}
	if _, err := s.SaveApp(in); !errors.Is(err, ErrAppConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	codex, err := s.SaveApp(appDraft("codex", p.ID))
	if err != nil {
		t.Fatal(err)
	}
	target, err := New(s.layout).ResolveApp(in.ID, "Bearer custom-placeholder", "", "management-test")
	if err != nil {
		t.Fatal(err)
	}
	if target.Provider.APIKey != "test-provider-key" || target.Provider.Config.Model != "app-model-custom-editor" || target.Authorization != "" || target.Profile.ReferencePrice.Input != 1.5 {
		t.Fatal("target/credential/price not pinned")
	}
	if target, err := s.ResolveApp("codex", "Bearer any-placeholder", "another-placeholder"); err != nil || target.Profile.ID != "codex" || target.Provider.Config.Model != "app-model-codex" || target.Authorization != "" {
		t.Fatalf("placeholder changed app selection: %+v %v", target.Profile, err)
	}
	if _, err := s.ResolveApp("missing", "", ""); !errors.Is(err, ErrAppNotFound) {
		t.Fatal(err)
	}
	if _, err := s.ResolveApp("claude-code", "", ""); !errors.Is(err, ErrAppUnconfigured) {
		t.Fatal(err)
	}
	if err := s.DeleteCloudProvider(p.ID, p.Revision); err == nil {
		t.Fatal("deleted referenced provider")
	}
	if err := s.DeleteApp("codex", codex.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("codex", p.ID)); !errors.Is(err, ErrAppConflict) {
		t.Fatal("preset reset allowed stale revision")
	}
	if err := s.DeleteApp(in.ID, saved.Revision); err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveApp(in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision <= saved.Revision {
		t.Fatal("custom re-registration reused old revision")
	}
	if err := s.DeleteApp(in.ID, saved.Revision); !errors.Is(err, ErrAppConflict) {
		t.Fatal("old editor deleted re-registered app")
	}
	if err := s.DeleteApp(in.ID, again.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCloudProvider(p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
}
func TestApplicationCredentialModesAndMisuse(t *testing.T) {
	s := providerSetup(t)
	draft := providerDraft()
	headers := map[string]string{"Authorization": "Bearer wrong-header-key", "X-Tenant": "retained"}
	draft.Headers = &headers
	p, err := s.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	loomy, err := s.SaveApp(appDraft("loomy", p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if loomy.CredentialMode != ForwardAuthorization {
		t.Fatal("wrong Loomy mode")
	}
	target, err := s.ResolveApp("loomy", "Bearer dynamic-loomy", "", "management-token")
	if err != nil {
		t.Fatal(err)
	}
	if target.Authorization != "Bearer dynamic-loomy" || target.Provider.APIKey != "" || target.Provider.Headers["Authorization"] != "" || target.Provider.Headers["X-Tenant"] != "retained" {
		t.Fatal("Loomy replaced dynamic authorization")
	}
	for _, auth := range []string{"", "Basic a", "Bearer ", "Bearer a\r\n"} {
		if _, err := s.ResolveApp("loomy", auth, ""); !errors.Is(err, ErrMissingCredential) {
			t.Fatalf("missing/invalid auth: %v", err)
		}
	}
	if _, err := s.ResolveApp("loomy", "Bearer management-token", "", "management-token"); !errors.Is(err, ErrCredentialMisuse) {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("codex", p.ID)); err != nil {
		t.Fatal(err)
	}
	for _, credentials := range [][2]string{{"", ""}, {"Bearer first-placeholder", ""}, {"", "second-placeholder"}, {"Basic ordinary", "another-placeholder"}} {
		target, err := s.ResolveApp("codex", credentials[0], credentials[1], "management-token")
		if err != nil || target.Authorization != "" || target.Provider.APIKey != "test-provider-key" || target.Provider.Config.Model != "app-model-codex" || target.Provider.Headers["Authorization"] != "" {
			t.Fatalf("ordinary credentials changed target: %v", err)
		}
	}
	for _, credentials := range [][2]string{{"Bearer management-token", ""}, {"", "management-token"}} {
		if _, err := s.ResolveApp("codex", credentials[0], credentials[1], "management-token"); !errors.Is(err, ErrCredentialMisuse) {
			t.Fatalf("management token accepted as placeholder: %v", err)
		}
	}
	for _, id := range []string{"claude-code", "astudio", "workbuddy", "dsh", "ordinary-custom"} {
		bad := appDraft(id, p.ID)
		bad.CredentialMode = ForwardAuthorization
		if _, err := s.SaveApp(bad); err == nil {
			t.Fatalf("Loomy policy applied to %s", id)
		}
	}
	// Never use another provider's key when this exact selected provider has none.
	empty := providerDraft()
	empty.APIKey = nil
	empty.Headers = nil
	p2, err := s.SaveCloudProvider(empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("dsh", p2.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveApp("dsh", "Bearer placeholder", ""); !errors.Is(err, ErrMissingCredential) {
		t.Fatal("missing key fell back to another provider")
	}
	// A management token manually placed in persisted advanced options is blocked.
	bad := providerDraft()
	extra := map[string]json.RawMessage{"private": json.RawMessage(`"management-token"`)}
	bad.ExtraBody = &extra
	p3, err := s.SaveCloudProvider(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveApp(appDraft("astudio", p3.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveApp("astudio", "Bearer placeholder", "", "management-token"); !errors.Is(err, ErrCredentialMisuse) {
		t.Fatal("persisted management token could be sent upstream")
	}
}
func TestApplicationUnknownFieldsCorruptionAndConcurrentEditors(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	in := appDraft("codex", p.ID)
	in.ReferencePrice = &ReferencePrice{Currency: "USD", Unit: "million-tokens", Input: 1, Output: 2, Version: "v1", Source: "test"}
	saved, err := s.SaveApp(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(d Document) error {
		var apps Document
		_ = json.Unmarshal(d["apps"], &apps)
		var app Document
		_ = json.Unmarshal(apps["codex"], &app)
		app["future"] = json.RawMessage(`{"keep":true}`)
		app["cloud"] = mergeObject(app["cloud"], map[string]any{"futureCloud": true})
		app["referencePrice"] = mergeObject(app["referencePrice"], map[string]any{"futurePrice": true})
		apps["codex"], _ = json.Marshal(app)
		d["apps"], _ = json.Marshal(apps)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	in.Revision = saved.Revision
	in.Name = "renamed"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.SaveApp(in); errs <- err }()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent saves: %d succeeded", successes)
	}
	after, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"futureCloud", "futurePrice", "future"} {
		if !bytes.Contains(after, []byte(field)) {
			t.Fatal("lost unknown field", field)
		}
	}
	public, _ := s.Apps()
	encoded, _ := json.Marshal(public)
	for _, secret := range []string{"codex-entry-1234567", "test-provider-key", "entryCredentialRef", "sha256", "futureCloud"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("public projection exposed private/unknown field", secret)
		}
	}
	// A corrupt Profile or provider store cannot be overwritten with defaults.
	originalCredentials, _ := os.ReadFile(s.layout.CredentialsFile())
	for _, corrupt := range []string{"{broken", `{"schemaVersion":1,"providers":null,"apps":{}}`} {
		put(t, s.layout.CredentialsFile(), corrupt)
		if _, err := s.SaveApp(appDraft("new-custom", "")); err == nil {
			t.Fatal("overwrote corrupt credential store")
		}
		unchanged(t, s.layout.CredentialsFile(), corrupt)
	}
	put(t, s.layout.CredentialsFile(), string(originalCredentials))
	corrupt := strings.Replace(string(after), `"localRuntime": "shared"`, `"localRuntime": "per-profile"`, 1)
	put(t, s.layout.ConfigFile(), corrupt)
	if _, err := s.Initialize(); err == nil {
		t.Fatal("initialized damaged Profile")
	}
	if _, err := s.SaveApp(in); err == nil {
		t.Fatal("saved over damaged Profile")
	}
	unchanged(t, s.layout.ConfigFile(), corrupt)
}
