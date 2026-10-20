package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
)

func retiredFixture(t *testing.T, host string) (Engine, Options, map[string][]byte) {
	t.Helper()
	e, o := workBuddySetup(t)
	if host == "loomy" {
		e, o = loomySetup(t)
	}
	o.Model = "deepseek-flash"
	// Both hosts had already registered the old Router model before this
	// layout first connected them. This is the reported duplicate-list case.
	legacy := o
	if host == "workbuddy" {
		legacy.RouterURL = "http://127.0.0.1:58116"
	}
	var input []FilePlan
	for _, spec := range e.Adapter.Contract().Files {
		input = append(input, FilePlan{Name: spec.Name})
	}
	files, err := e.Adapter.Build(legacy, input)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{}
	for _, f := range files {
		put(t, hostPath(o.ConfigRoot, f.Name), f.After)
		original[f.Name] = bytes.Clone(f.After)
	}
	mustApply(t, e, o)
	o.Model = "spark-x2.5"
	return e, o, original
}

func TestConnectRetiresPreviousRouterModelFromBothCatalogs(t *testing.T) {
	for _, host := range []string{"workbuddy", "loomy"} {
		t.Run(host, func(t *testing.T) {
			e, o, original := retiredFixture(t, host)
			for _, selected := range []string{"spark-x2.5", "model.v2", "deepseek-flash", "spark-x2.5"} {
				o.Model = selected
				if _, err := e.Connect(context.Background(), o); err != nil {
					t.Fatalf("connect %s: %v", selected, err)
				}
				file := e.Adapter.Contract().Files[0].Name
				models, err := catalogModels(host, get(t, hostPath(o.ConfigRoot, file)))
				if err != nil {
					t.Fatal(err)
				}
				if len(models) != 1 || models[selected] == nil {
					t.Fatalf("host still advertises an obsolete Router model: %v", models)
				}
			}
			j, _, err := e.readJournal(o.Host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			if j.Version != 2 || len(j.RetiredModels) == 0 {
				t.Fatal("retirement lacks versioned recovery metadata")
			}
			for _, f := range j.Files {
				if !bytes.Equal(f.Before, original[f.Name]) {
					t.Fatal("retirement overwrote the first original backup")
				}
			}
			mustRestore(t, e, o)
			for name, raw := range original {
				if !bytes.Equal(get(t, hostPath(o.ConfigRoot, name)), raw) {
					t.Fatal("offline restore did not reinstate the exact original definitions:", name)
				}
			}
		})
	}
}

func TestRetiredModelsRestoreOriginalSelectionAndPreserveLaterUserEdits(t *testing.T) {
	for _, host := range []string{"workbuddy", "loomy"} {
		t.Run(host, func(t *testing.T) {
			e, o, _ := retiredFixture(t, host)
			if _, err := e.Connect(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			file := "settings.json"
			if host == "loomy" {
				file = "opencode.json"
			}
			path := hostPath(o.ConfigRoot, file)
			raw, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: "later", Value: "preserve this"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			put(t, path, raw)
			// WorkBuddy models also acquire a later unrelated root field.
			if host == "workbuddy" {
				modelsPath := hostPath(o.ConfigRoot, "models.json")
				raw, err := edit.PatchJSON(get(t, modelsPath), []edit.KV{{Path: "later", Value: "keep models extension"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				put(t, modelsPath, raw)
			}
			mustRestore(t, e, o)
			if !bytes.Contains(get(t, path), []byte("preserve this")) {
				t.Fatal("restore discarded a later user setting")
			}
			models, err := catalogModels(host, get(t, hostPath(o.ConfigRoot, e.Adapter.Contract().Files[0].Name)))
			if err != nil || models["deepseek-flash"] == nil || models["spark-x2.5"] != nil {
				t.Fatal("restore left the original selection without its model definition", err)
			}
		})
	}
}

func TestRetirementPreservesUserDefinedAndUnrelatedModels(t *testing.T) {
	e, o, _ := retiredFixture(t, "workbuddy")
	path := hostPath(o.ConfigRoot, "models.json")
	raw, err := workBuddyAppendModel(get(t, path), map[string]any{"id": "user-model", "name": "My provider", "url": "https://example.test/v1", "apiKey": "private-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, raw)
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	models, err := catalogModels(o.Host, get(t, path))
	if err != nil || len(models) != 2 || models["user-model"] == nil || models[o.Model] == nil {
		t.Fatal("retirement removed another provider's model", err)
	}

	e, o, _ = retiredFixture(t, "loomy")
	path = hostPath(o.ConfigRoot, "opencode.json")
	raw, err = edit.PatchJSON(get(t, path), []edit.KV{{Path: "provider.api_router.models.user-model", Value: map[string]any{"name": "user-model", "tool_call": true, "userNote": "keep"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, raw)
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	models, err = catalogModels(o.Host, get(t, path))
	if err != nil || models["user-model"] == nil || models["deepseek-flash"] != nil {
		t.Fatal("retirement discarded a model with user extensions or kept the obsolete generated one", err)
	}
}

func TestRetirementWriteFailureRestoresCatalogAndJournal(t *testing.T) {
	e, o, _ := retiredFixture(t, "workbuddy")
	if _, err := e.connect(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	path := hostPath(o.ConfigRoot, "models.json")
	before := get(t, path)
	recordPath := e.recordPath(o.Host, o.ConfigRoot)
	record := get(t, recordPath)
	failed := false
	e.write = func(path string, raw []byte, mode uint32) error {
		if path == recordPath && !failed {
			failed = true
			return errors.New("retirement journal write failed")
		}
		return steady.WriteFile(path, raw, os.FileMode(mode))
	}
	if err := e.retireModels(context.Background(), o); err == nil || !failed {
		t.Fatal("did not exercise retirement rollback", err)
	}
	if !bytes.Equal(get(t, path), before) || !bytes.Equal(get(t, recordPath), record) {
		t.Fatal("failed retirement changed the catalog or its recovery baseline")
	}
	if _, err := os.Stat(filepath.Join(e.StateDir, portTransactionFile)); !os.IsNotExist(err) {
		t.Fatal("successful rollback left pending recovery")
	}
}

func TestRetiredOriginalModelUserReplacementBlocksRestore(t *testing.T) {
	for _, host := range []string{"workbuddy", "loomy"} {
		t.Run(host, func(t *testing.T) {
			e, o, _ := retiredFixture(t, host)
			if _, err := e.Connect(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			path := hostPath(o.ConfigRoot, e.Adapter.Contract().Files[0].Name)
			var raw []byte
			var err error
			if host == "workbuddy" {
				raw, err = workBuddyAppendModel(get(t, path), map[string]any{"id": "deepseek-flash", "name": "User replacement", "url": "https://example.test/v1"})
			} else {
				raw, err = edit.PatchJSON(get(t, path), []edit.KV{{Path: "provider.api_router.models.deepseek-flash", Value: map[string]any{"name": "User replacement"}}}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			put(t, path, raw)
			if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
				t.Fatal("restore accepted a conflicting user replacement")
			}
			if !bytes.Equal(get(t, path), raw) {
				t.Fatal("restore overwrote the user's replacement model")
			}
		})
	}
}

func TestInvalidRetirementMetadataBlocksRecovery(t *testing.T) {
	for _, kind := range []string{"v1", "duplicate", "future-version"} {
		t.Run(kind, func(t *testing.T) {
			e, o, _ := retiredFixture(t, "loomy")
			if _, err := e.Connect(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			j, _, err := e.readJournal(o.Host, o.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "v1":
				j.Version = 1
			case "duplicate":
				j.RetiredModels = append(j.RetiredModels, j.RetiredModels[0])
			case "future-version":
				j.Version = 99
			}
			j.Checksum = j.checksum()
			raw, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			put(t, e.recordPath(o.Host, o.ConfigRoot), raw)
			if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
				t.Fatal("invalid retirement record accepted", kind)
			}
		})
	}
}
