package routerconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
)

func TestRedactionBatchCapabilityAndAtomicRevision(t *testing.T) {
	s := providerSetup(t)
	for _, capability := range []RedactionCapability{RedactionUnknown, RedactionUnavailable, "client-claimed-ready", ""} {
		_, err := s.SetRedactionIntentsContext(context.Background(), []RedactionIntentEdit{{ID: "codex", Enabled: true}}, capability)
		if !errors.Is(err, ErrRedactionUnavailable) {
			t.Fatalf("unknown capability enabled intent: %v", err)
		}
	}
	selected := []RedactionIntentEdit{{ID: "codex", Enabled: true}, {ID: "claude-code", Enabled: true}}
	saved, err := s.SetRedactionIntentsContext(context.Background(), selected, RedactionAvailable)
	if err != nil || len(saved) != 2 || !saved[0].RedactionEnabled || !saved[1].RedactionEnabled {
		t.Fatalf("batch enable: %+v %v", saved, err)
	}
	before, _ := os.ReadFile(s.layout.ConfigFile())
	// A stale second app must also roll back the otherwise valid first edit.
	_, err = s.SetRedactionIntentsContext(context.Background(), []RedactionIntentEdit{{ID: "codex", Revision: saved[0].Revision, Enabled: false}, {ID: "claude-code", Enabled: false}}, RedactionUnavailable)
	after, _ := os.ReadFile(s.layout.ConfigFile())
	if !errors.Is(err, ErrAppConflict) || !bytes.Equal(before, after) {
		t.Fatal("partial mutation after stale batch", err)
	}
	// An unavailable runtime does not erase existing intent or prevent a no-op.
	keep, err := s.SetRedactionIntentsContext(context.Background(), []RedactionIntentEdit{{ID: "codex", Revision: saved[0].Revision, Enabled: true}}, RedactionUnavailable)
	if err != nil || keep[0].Revision != saved[0].Revision || !keep[0].RedactionEnabled {
		t.Fatal("existing intent not preserved", err)
	}
	for i := range selected {
		selected[i].Revision, selected[i].Enabled = saved[i].Revision, false
	}
	if _, err := s.SetRedactionIntentsContext(context.Background(), selected, RedactionUnknown); err != nil {
		t.Fatal("unavailable capability prevented disabling", err)
	}
}

func TestRedactionBatchRetainsProfileAndRejectsInvalidSelection(t *testing.T) {
	s := providerSetup(t)
	p, err := s.SaveApp(appDraft("codex", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(d Document) error {
		var apps Document
		_ = json.Unmarshal(d["apps"], &apps)
		apps["codex"] = mergeObject(apps["codex"], map[string]any{"future": map[string]any{"keep": "extension"}})
		d["apps"], _ = json.Marshal(apps)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRedactionIntentsContext(context.Background(), []RedactionIntentEdit{{ID: p.ID, Revision: p.Revision, Enabled: true}}, RedactionAvailable); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Load()
	var decoded struct {
		Codex struct {
			Future map[string]string
		}
	}
	if json.Unmarshal(d["apps"], &decoded) != nil || decoded.Codex.Future["keep"] != "extension" || !bytes.Contains(d["future"], []byte("keep me")) {
		t.Fatal("intent edit discarded unknown fields")
	}
	for _, edits := range [][]RedactionIntentEdit{nil, {{ID: "unknown"}}, {{ID: "codex"}, {ID: "codex"}}, {{ID: "invalid/id"}}, {{ID: "codex", Revision: -1}}} {
		if _, err := s.SetRedactionIntentsContext(context.Background(), edits, RedactionAvailable); err == nil {
			t.Fatal("invalid selection accepted", edits)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SetRedactionIntentsContext(ctx, []RedactionIntentEdit{{ID: "dsh"}}, RedactionUnknown); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestRedactionBatchConcurrentEditors(t *testing.T) {
	s := providerSetup(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := New(s.layout).SetRedactionIntentsContext(context.Background(), []RedactionIntentEdit{{ID: "codex", Enabled: true}}, RedactionAvailable)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAppConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("lost revision protection: success=%d conflict=%d", success, conflict)
	}
}

func TestRedactionExperimentalBatchRequiresServerAuthorization(t *testing.T) {
	s := providerSetup(t)
	ctx := context.Background()
	for _, capability := range []RedactionCapability{RedactionUnknown, RedactionUnavailable, "experimental=true", "trial-ready", ""} {
		if _, err := s.SetRedactionIntentsContext(ctx, []RedactionIntentEdit{{ID: "codex", Enabled: true}}, capability); !errors.Is(err, ErrRedactionUnavailable) {
			t.Fatal("untrusted trial claim enabled redaction intent", err)
		}
	}
	saved, err := s.SetRedactionIntentsContext(ctx, []RedactionIntentEdit{{ID: "codex", Enabled: true}, {ID: "dsh", Enabled: true}}, RedactionExperimental)
	if err != nil || len(saved) != 2 || !saved[0].RedactionEnabled || !saved[1].RedactionEnabled {
		t.Fatal("server-authorized trial could not enable selected intents", err)
	}
	before, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRedactionIntentsContext(ctx, []RedactionIntentEdit{{ID: "codex", Revision: saved[0].Revision, Enabled: false}, {ID: "dsh", Revision: 0, Enabled: false}}, RedactionExperimental); !errors.Is(err, ErrAppConflict) {
		t.Fatal("experimental update lost atomic revision validation", err)
	}
	after, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conflicting trial update partially changed intents", err)
	}
	if _, err := s.SetRedactionIntentsContext(ctx, []RedactionIntentEdit{{ID: "codex", Revision: saved[0].Revision, Enabled: false}}, RedactionUnknown); err != nil {
		t.Fatal("ending trial prevented the user from disabling intent", err)
	}
}

func TestRedactionExperimentalProfileSaveAndReadOnlyCheck(t *testing.T) {
	s := providerSetup(t)
	provider, err := s.SaveCloudProvider(providerDraft())
	if err != nil {
		t.Fatal(err)
	}
	in := appDraft("codex", provider.ID)
	in.RedactionEnabled = true
	ctx := context.Background()
	before, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAppDraftContext(ctx, in, "test-management-token"); err == nil {
		t.Fatal("ordinary read-only check accepted unapproved redaction")
	}
	if _, err := s.SaveApp(in); err == nil {
		t.Fatal("ordinary profile save accepted unapproved redaction")
	}
	for _, capability := range []RedactionCapability{RedactionUnknown, RedactionUnavailable, "client-claimed-experimental", ""} {
		if _, err := s.ResolveAppDraftWithRedactionCapabilityContext(ctx, in, "test-management-token", capability); err == nil {
			t.Fatal("untrusted capability passed read-only trial check")
		}
		if _, err := s.SaveAppWithRedactionCapabilityContext(ctx, in, capability); err == nil {
			t.Fatal("untrusted capability enabled profile redaction")
		}
	}
	target, err := s.ResolveAppDraftWithRedactionCapabilityContext(ctx, in, "test-management-token", RedactionExperimental)
	if err != nil || !target.Profile.RedactionEnabled || target.Provider.Config.Model != in.Cloud.Model {
		t.Fatal("server-authorized experimental check failed", err)
	}
	after, err := os.ReadFile(s.layout.ConfigFile())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only experimental check wrote profile or acceptance state", err)
	}
	saved, err := s.SaveAppWithRedactionCapabilityContext(ctx, in, RedactionExperimental)
	if err != nil || !saved.RedactionEnabled {
		t.Fatal("server-authorized experimental profile could not be saved", err)
	}
	// Ending the startup trial keeps saved intent but grants no new authorization.
	in.Revision = saved.Revision
	retained, err := s.SaveApp(in)
	if err != nil || !retained.RedactionEnabled {
		t.Fatal("ending experimental mode erased existing intent", err)
	}
}
