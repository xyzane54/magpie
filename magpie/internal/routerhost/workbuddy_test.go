package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/steady"
)

// These structures come from WorkBuddy 5.3.8's installed custom-model loader;
// all credentials and model names in this file are synthetic test values.
func workBuddySetup(t *testing.T) (Engine, Options) {
	t.Helper()
	e, o := setup(t, "workbuddy")
	e.Adapter = WorkBuddyAdapter{}
	o.Model = "target-model"
	return e, o
}

func workBuddyTestModels(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("WorkBuddy models.json is not strict JSON: %v", err)
	}
	switch value := value.(type) {
	case map[string]any:
		return value
	case []any:
		return map[string]any{"models": value}
	default:
		t.Fatal("WorkBuddy models.json has unsupported root")
		return nil
	}
}

func workBuddyTarget(t *testing.T, raw []byte, model string) map[string]any {
	t.Helper()
	doc := workBuddyTestModels(t, raw)
	return workBuddyDocTarget(t, doc, model)
}

func workBuddyDocTarget(t *testing.T, doc map[string]any, model string) map[string]any {
	t.Helper()
	models, ok := doc["models"].([]any)
	if !ok {
		t.Fatal("models is not an array")
	}
	for _, value := range models {
		m, ok := value.(map[string]any)
		if ok && m["id"] == model {
			return m
		}
	}
	return nil
}

func workBuddyWriteJSON(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, append(raw, '\n'))
}

func workBuddyAssertTarget(t *testing.T, e Engine, o Options) {
	t.Helper()
	m := workBuddyTarget(t, get(t, hostPath(o.ConfigRoot, "models.json")), o.Model)
	if m == nil || m["url"] != o.RouterURL+"/app/workbuddy/v1" || m["apiKey"] != PlaceholderAPIKey || m["supportsToolCall"] != true || m["useCustomProtocol"] != false {
		t.Fatal("WorkBuddy target did not select Router, public placeholder, or tool-capable Chat Completions")
	}
	settings, err := readJSON(get(t, hostPath(o.ConfigRoot, "settings.json")))
	if err != nil || settings["model"] != "custom-local:"+o.Model {
		t.Fatal("WorkBuddy did not explicitly select the custom model")
	}
}

func TestWorkBuddyPreviewApplyAndExactOfflineRestore(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[existing], func(t *testing.T) {
			e, o := workBuddySetup(t)
			models := []byte("{\"models\":[{\"id\":\"other-model\",\"url\":\"https://fixture.invalid/v1\",\"apiKey\":\"original-fixture-key\"}],\"availableModels\":[\"other-model\"],\"unknown\":3}\n")
			settings := []byte("{\r\n // original user comment\r\n \"model\":\"old-model\", \"unknown\":true\r\n}\r\n")
			if existing {
				put(t, hostPath(o.ConfigRoot, "models.json"), models)
				put(t, hostPath(o.ConfigRoot, "settings.json"), settings)
			}
			checks := 0
			e.Checker = checkerFunc(func(context.Context, Options) error { checks++; return nil })
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			if checks != 0 {
				t.Fatal("preview called a network checker")
			}
			if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
				t.Fatal("preview created recovery state")
			}
			if !existing {
				if _, err := os.Stat(o.ConfigRoot); !os.IsNotExist(err) {
					t.Fatal("preview created host configuration directory")
				}
			} else if !bytes.Equal(models, get(t, hostPath(o.ConfigRoot, "models.json"))) || !bytes.Equal(settings, get(t, hostPath(o.ConfigRoot, "settings.json"))) {
				t.Fatal("preview changed existing files")
			}
			summary, err := json.Marshal(p.Summary())
			if err != nil || bytes.Contains(summary, []byte("original-fixture-key")) || bytes.Contains(summary, []byte(PlaceholderAPIKey)) {
				t.Fatal("preview exposed configuration credentials")
			}
			if err := e.Apply(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			workBuddyAssertTarget(t, e, o)
			firstBackup := get(t, e.recordPath(o.Host, o.ConfigRoot))
			firstModels := get(t, hostPath(o.ConfigRoot, "models.json"))
			firstSettings := get(t, hostPath(o.ConfigRoot, "settings.json"))
			mustApply(t, e, o)
			if !bytes.Equal(firstBackup, get(t, e.recordPath(o.Host, o.ConfigRoot))) || !bytes.Equal(firstModels, get(t, hostPath(o.ConfigRoot, "models.json"))) || !bytes.Equal(firstSettings, get(t, hostPath(o.ConfigRoot, "settings.json"))) {
				t.Fatal("idempotent apply changed bytes or overwrote the first backup")
			}
			// The new Engine has no checker and an origin on which no service runs.
			e = Engine{StateDir: e.StateDir, Adapter: WorkBuddyAdapter{}}
			mustRestore(t, e, o)
			mustRestore(t, e, o)
			for filename, original := range map[string][]byte{"models.json": models, "settings.json": settings} {
				path := hostPath(o.ConfigRoot, filename)
				if existing {
					if !bytes.Equal(get(t, path), original) {
						t.Fatalf("offline restore did not restore exact original %s", filename)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("offline restore retained untouched tool-created %s", filename)
				}
			}
		})
	}
}

func TestWorkBuddyRejectsDamagedAmbiguousAndUnsafeConfiguration(t *testing.T) {
	tests := []struct{ name, file, raw string }{
		{"models-empty", "models.json", ""},
		{"models-malformed", "models.json", `{"models":[`},
		{"models-comment", "models.json", "{\n// host JSON.parse cannot read comments\n\"models\":[]}"},
		{"models-bom", "models.json", "\xef\xbb\xbf{\"models\":[]}"},
		{"models-root-null", "models.json", `null`},
		{"models-null", "models.json", `{"models":null}`},
		{"models-scalar", "models.json", `{"models":false}`},
		{"models-scalar-record", "models.json", `{"models":[3]}`},
		{"models-missing-id", "models.json", `{"models":[{"url":"https://fixture.invalid"}]}`},
		{"models-empty-id", "models.json", `{"models":[{"id":""}]}`},
		{"models-duplicate-id", "models.json", `{"models":[{"id":"target-model"},{"id":"target-model"}]}`},
		{"models-duplicate-key", "models.json", `{"models":[],"models":[]}`},
		{"models-nested-duplicate", "models.json", `{"models":[{"id":"target-model","url":"fixture-private-a","url":"fixture-private-b"}]}`},
		{"models-target-url-type", "models.json", `{"models":[{"id":"target-model","url":{}}]}`},
		{"models-target-key-type", "models.json", `{"models":[{"id":"target-model","apiKey":3}]}`},
		{"models-target-tool-type", "models.json", `{"models":[{"id":"target-model","supportsToolCall":"true"}]}`},
		{"models-target-protocol-type", "models.json", `{"models":[{"id":"target-model","useCustomProtocol":"false"}]}`},
		{"settings-empty", "settings.json", ""},
		{"settings-malformed", "settings.json", `{"model":`},
		{"settings-root-array", "settings.json", `[]`},
		{"settings-duplicate", "settings.json", `{"model":"fixture-private-a","model":"fixture-private-b"}`},
		{"settings-model-type", "settings.json", `{"model":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, o := workBuddySetup(t)
			path := hostPath(o.ConfigRoot, tc.file)
			original := []byte(tc.raw)
			put(t, path, original)
			if _, err := e.Preview(o); err == nil || strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("unsafe configuration accepted or parse error exposed source values")
			}
			if !bytes.Equal(get(t, path), original) {
				t.Fatal("rejected preview changed source")
			}
			if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
				t.Fatal("rejected preview created recovery state")
			}
		})
	}
}

func TestWorkBuddyRestorePreservesOtherProvidersAndLaterChanges(t *testing.T) {
	e, o := workBuddySetup(t)
	modelsPath := hostPath(o.ConfigRoot, "models.json")
	settingsPath := hostPath(o.ConfigRoot, "settings.json")
	put(t, modelsPath, []byte(`{"models":[{"id":"target-model","name":"User label","url":"https://original.fixture.invalid/v1","apiKey":"original-fixture-key","supportsToolCall":false,"useCustomProtocol":true,"unknown":{"keep":true}},{"id":"other-model","url":"https://other.fixture.invalid/v1","apiKey":"other-fixture-key"}],"availableModels":["other-model"],"topUnknown":{"keep":1}}`))
	put(t, settingsPath, []byte("\xef\xbb\xbf{\r\n // original settings comment\r\n \"model\":\"old-model\",\r\n \"unknown\":true\r\n}\r\n"))
	mustApply(t, e, o)
	workBuddyAssertTarget(t, e, o)
	if raw := get(t, settingsPath); !bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || !bytes.Contains(raw, []byte("// original settings comment\r\n")) || !bytes.Contains(raw, []byte("\r\n")) {
		t.Fatal("settings edit lost BOM, CRLF or comments")
	}
	doc := workBuddyTestModels(t, get(t, modelsPath))
	models := doc["models"].([]any)
	target := models[0].(map[string]any)
	if target["name"] != "User label" || target["unknown"] == nil || models[1].(map[string]any)["apiKey"] != "other-fixture-key" {
		t.Fatal("apply changed unknown fields, user label or another Provider")
	}
	target["later"] = "preserve target preference"
	models[1].(map[string]any)["apiKey"] = "user-updated-other-fixture-key"
	// A reorder is unrelated to the managed target's identity.
	doc["models"] = []any{models[1], models[0], map[string]any{"id": "later-model", "url": "https://later.fixture.invalid"}}
	doc["laterGlobal"] = true
	workBuddyWriteJSON(t, modelsPath, doc)
	settings := get(t, settingsPath)
	settings = bytes.Replace(settings, []byte("\"unknown\":true"), []byte("\"unknown\":false,\r\n // later comment\r\n \"laterPreference\":3"), 1)
	put(t, settingsPath, settings)
	e.Checker = nil
	mustRestore(t, e, o)
	restored := workBuddyTestModels(t, get(t, modelsPath))
	target = workBuddyTarget(t, get(t, modelsPath), o.Model)
	if target["url"] != "https://original.fixture.invalid/v1" || target["apiKey"] != "original-fixture-key" || target["supportsToolCall"] != false || target["useCustomProtocol"] != true || target["later"] != "preserve target preference" || restored["laterGlobal"] != true {
		t.Fatal("restore lost original owned fields or unrelated later changes")
	}
	if workBuddyTarget(t, get(t, modelsPath), "other-model")["apiKey"] != "user-updated-other-fixture-key" || workBuddyTarget(t, get(t, modelsPath), "later-model") == nil {
		t.Fatal("restore changed another Provider or removed a later Provider")
	}
	restoredSettings, err := readJSON(get(t, settingsPath))
	if err != nil || restoredSettings["model"] != "old-model" || restoredSettings["unknown"] != false || restoredSettings["laterPreference"] != json.Number("3") || !bytes.Contains(get(t, settingsPath), []byte("// later comment")) {
		t.Fatal("restore lost original model or unrelated settings changes")
	}
}

func TestWorkBuddyCreatedModelRestorationKeepsLaterUnknownFields(t *testing.T) {
	e, o := workBuddySetup(t)
	modelsPath := hostPath(o.ConfigRoot, "models.json")
	mustApply(t, e, o)
	doc := workBuddyTestModels(t, get(t, modelsPath))
	target := workBuddyDocTarget(t, doc, o.Model)
	target["userAfter"] = "keep"
	doc["laterGlobal"] = true
	workBuddyWriteJSON(t, modelsPath, doc)
	mustRestore(t, e, o)
	target = workBuddyTarget(t, get(t, modelsPath), o.Model)
	if target == nil || target["id"] != o.Model || target["userAfter"] != "keep" {
		t.Fatal("restore removed user additions to a tool-created model")
	}
	for _, key := range []string{"url", "apiKey", "supportsToolCall", "useCustomProtocol"} {
		if _, exists := target[key]; exists {
			t.Fatalf("restore retained tool-created connection field %s", key)
		}
	}
}

func TestWorkBuddyManagedFieldConflictsRejectCompleteRestore(t *testing.T) {
	for _, field := range []string{"url", "apiKey", "supportsToolCall", "useCustomProtocol", "settings.model"} {
		t.Run(field, func(t *testing.T) {
			e, o := workBuddySetup(t)
			mustApply(t, e, o)
			modelsPath := hostPath(o.ConfigRoot, "models.json")
			settingsPath := hostPath(o.ConfigRoot, "settings.json")
			if field == "settings.model" {
				put(t, settingsPath, []byte(`{"model":"user-fixture-choice"}`))
			} else {
				doc := workBuddyTestModels(t, get(t, modelsPath))
				target := workBuddyDocTarget(t, doc, o.Model)
				value := any("user-fixture-choice")
				if field == "supportsToolCall" {
					value = false
				} else if field == "useCustomProtocol" {
					value = true
				}
				target[field] = value
				workBuddyWriteJSON(t, modelsPath, doc)
			}
			beforeModels, beforeSettings := get(t, modelsPath), get(t, settingsPath)
			if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil || strings.Contains(err.Error(), "user-fixture-choice") {
				t.Fatal("managed-field conflict accepted or disclosed field value")
			}
			if !bytes.Equal(beforeModels, get(t, modelsPath)) || !bytes.Equal(beforeSettings, get(t, settingsPath)) {
				t.Fatal("conflicting restore changed a file")
			}
		})
	}
}

func TestWorkBuddyStalePlansAndCorruptBackupsNeverWrite(t *testing.T) {
	e, o := workBuddySetup(t)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := hostPath(o.ConfigRoot, "settings.json")
	user := []byte(`{"model":"user-after-preview"}`)
	put(t, settingsPath, user)
	if err := e.Apply(context.Background(), p); err == nil || !bytes.Equal(user, get(t, settingsPath)) {
		t.Fatal("stale apply overwrote user's later file")
	}
	mustApply(t, e, o)
	r, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	modelsPath := hostPath(o.ConfigRoot, "models.json")
	models, settings := get(t, modelsPath), get(t, settingsPath)
	corrupt := []byte(`{"Version":1,"Checksum":"corrupt-fixture"}`)
	put(t, e.recordPath(o.Host, o.ConfigRoot), corrupt)
	if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
		t.Fatal("corrupt backup accepted by restore-preview")
	}
	if err := e.Restore(r); err == nil {
		t.Fatal("stale restore accepted changed backup")
	}
	if _, err := e.Preview(o); err == nil {
		t.Fatal("corrupt backup accepted by apply-preview")
	}
	if !bytes.Equal(models, get(t, modelsPath)) || !bytes.Equal(settings, get(t, settingsPath)) || !bytes.Equal(corrupt, get(t, e.recordPath(o.Host, o.ConfigRoot))) {
		t.Fatal("rejected backup changed configuration or destroyed evidence")
	}
}

func TestWorkBuddyUnverifiedBackupBlocksBothHostFiles(t *testing.T) {
	e, o := workBuddySetup(t)
	modelsPath := hostPath(o.ConfigRoot, "models.json")
	settingsPath := hostPath(o.ConfigRoot, "settings.json")
	models, settings := []byte(`{"models":[]}`), []byte(`{"model":"original"}`)
	put(t, modelsPath, models)
	put(t, settingsPath, settings)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.write = func(path string, data []byte, mode uint32) error {
		if filepath.Dir(path) == e.StateDir {
			data = data[:len(data)/2]
		}
		return steady.WriteFile(path, data, fs.FileMode(mode))
	}
	if err := e.Apply(context.Background(), p); err == nil {
		t.Fatal("apply accepted unverified partial backup")
	}
	if !bytes.Equal(models, get(t, modelsPath)) || !bytes.Equal(settings, get(t, settingsPath)) {
		t.Fatal("unverified backup allowed configuration writes")
	}
}

func TestWorkBuddyPartialWriteAndRollbackFailureRecoversOffline(t *testing.T) {
	e, o := workBuddySetup(t)
	modelsPath := hostPath(o.ConfigRoot, "models.json")
	settingsPath := hostPath(o.ConfigRoot, "settings.json")
	models, settings := []byte(`{"models":[],"unknown":true}`), []byte(`{"model":"original","unknown":3}`)
	put(t, modelsPath, models)
	put(t, settingsPath, settings)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	modelsWrites := 0
	e.write = func(path string, data []byte, mode uint32) error {
		switch path {
		case settingsPath:
			return errors.New("fixture second-file write failure")
		case modelsPath:
			modelsWrites++
			if modelsWrites > 1 {
				return errors.New("fixture rollback failure")
			}
		}
		return steady.WriteFile(path, data, fs.FileMode(mode))
	}
	if err := e.Apply(context.Background(), p); err == nil {
		t.Fatal("partial write failure reported success")
	}
	j, _, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil || j == nil || j.Status != "prepared" {
		t.Fatal("partial operation lost recoverable pending journal")
	}
	if bytes.Equal(models, get(t, modelsPath)) || !bytes.Equal(settings, get(t, settingsPath)) {
		t.Fatal("fixture did not leave exactly the first file applied")
	}
	// Preserve edits made after the failed apply while recovering without HTTP.
	doc := workBuddyTestModels(t, get(t, modelsPath))
	doc["laterUser"] = true
	workBuddyWriteJSON(t, modelsPath, doc)
	e = Engine{StateDir: e.StateDir, Adapter: WorkBuddyAdapter{}}
	mustRestore(t, e, o)
	if workBuddyTarget(t, get(t, modelsPath), o.Model) != nil || workBuddyTestModels(t, get(t, modelsPath))["laterUser"] != true || !bytes.Equal(settings, get(t, settingsPath)) {
		t.Fatal("offline recovery lost unrelated edits or retained partial Router target")
	}
}

func TestWorkBuddyDesktopArrayShapePreservesReorderedModels(t *testing.T) {
	for _, existingTarget := range []bool{false, true} {
		for _, restoredIndex := range []int{0, 1, 2} {
			label := map[bool]string{false: "added", true: "existing"}[existingTarget] + "/" + string(rune('0'+restoredIndex))
			t.Run(label, func(t *testing.T) {
				e, o := workBuddySetup(t)
				path := hostPath(o.ConfigRoot, "models.json")
				entries := []any{
					map[string]any{"id": "other-a", "apiKey": "fixture-a", "unknown": true},
					map[string]any{"id": "other-b", "apiKey": "fixture-b"},
				}
				if existingTarget {
					entries = append(entries, map[string]any{"id": o.Model, "url": "https://original.fixture.invalid", "apiKey": "original-fixture", "unknown": "keep"})
				}
				raw, err := json.MarshalIndent(entries, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				put(t, path, raw)
				mustApply(t, e, o)
				if !bytes.HasPrefix(bytes.TrimSpace(get(t, path)), []byte("[")) {
					t.Fatal("apply changed installed Desktop array format to object")
				}
				workBuddyAssertTarget(t, e, o)
				doc := workBuddyTestModels(t, get(t, path))
				target := workBuddyDocTarget(t, doc, o.Model)
				otherA := workBuddyDocTarget(t, doc, "other-a")
				otherB := workBuddyDocTarget(t, doc, "other-b")
				otherA["laterPreference"] = "keep"
				var reordered []any
				switch restoredIndex {
				case 0:
					reordered = []any{target, otherA, otherB}
				case 1:
					reordered = []any{otherA, target, otherB}
				case 2:
					reordered = []any{otherA, otherB, target}
				}
				raw, err = json.MarshalIndent(reordered, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				put(t, path, raw)
				mustRestore(t, e, o)
				if !bytes.HasPrefix(bytes.TrimSpace(get(t, path)), []byte("[")) {
					t.Fatal("restore changed Desktop array format to object")
				}
				target = workBuddyTarget(t, get(t, path), o.Model)
				if existingTarget {
					if target == nil || target["url"] != "https://original.fixture.invalid" || target["apiKey"] != "original-fixture" || target["unknown"] != "keep" {
						t.Fatal("reordered array target did not restore original fields")
					}
				} else if target != nil {
					t.Fatal("restore did not remove tool-created array target")
				}
				if workBuddyTarget(t, get(t, path), "other-a")["laterPreference"] != "keep" || workBuddyTarget(t, get(t, path), "other-b")["apiKey"] != "fixture-b" {
					t.Fatal("array restore lost another Provider or its later preference")
				}
			})
		}
	}
}

func TestWorkBuddyIdentityAndLocalEndpointContract(t *testing.T) {
	for _, mutate := range []func(*Options){
		func(o *Options) { o.Host = "other-app" },
		func(o *Options) { o.Model = "custom-local:target-model" },
		func(o *Options) { o.RouterURL = "http://127.0.0.1:13145/app/workbuddy/v1" },
		func(o *Options) { o.RouterURL = "http://127.0.0.1:13145/app/other/v1" },
		func(o *Options) { o.RouterURL = "http://localhost:13145" },
		func(o *Options) { o.RouterURL = "http://fixture.invalid" },
	} {
		e, o := workBuddySetup(t)
		mutate(&o)
		if _, err := e.Preview(o); err == nil {
			t.Fatal("mismatched host, prefixed model, or unsafe Router origin accepted")
		}
		if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
			t.Fatal("unsafe options wrote recovery state")
		}
	}
	e, o := workBuddySetup(t)
	o.Model = `model.with.dots/and"quote`
	mustApply(t, e, o)
	workBuddyAssertTarget(t, e, o)
	mustRestore(t, e, o)
}

func TestWorkBuddyCanceledApplyAndUnreadableSourceDoNotWrite(t *testing.T) {
	e, o := workBuddySetup(t)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Apply(ctx, p); err == nil {
		t.Fatal("canceled apply reported success")
	}
	for _, dir := range []string{o.ConfigRoot, e.StateDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("canceled apply created operation state")
		}
	}
	for _, filename := range []string{"models.json", "settings.json"} {
		t.Run(filename, func(t *testing.T) {
			e, o := workBuddySetup(t)
			path := hostPath(o.ConfigRoot, filename)
			original := []byte(`{}`)
			put(t, path, original)
			e.read = func(at string) ([]byte, error) {
				if at == path {
					return nil, fs.ErrPermission
				}
				return os.ReadFile(at)
			}
			if _, err := e.Preview(o); err == nil || !bytes.Equal(original, get(t, path)) {
				t.Fatal("unreadable source became an empty configuration")
			}
			if _, err := os.Stat(e.StateDir); !os.IsNotExist(err) {
				t.Fatal("unreadable source created recovery state")
			}
		})
	}
}

func TestWorkBuddyRenamedManagedModelRequiresReconciliation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "created-target", true: "existing-target"}[existing], func(t *testing.T) {
			e, o := workBuddySetup(t)
			path := hostPath(o.ConfigRoot, "models.json")
			if existing {
				put(t, path, []byte(`{"models":[{"id":"target-model","url":"https://original.fixture.invalid","apiKey":"fixture-original"}]}`))
			}
			mustApply(t, e, o)
			doc := workBuddyTestModels(t, get(t, path))
			workBuddyDocTarget(t, doc, o.Model)["id"] = "user-renamed-model"
			workBuddyWriteJSON(t, path, doc)
			beforeModels := get(t, path)
			beforeSettings := get(t, hostPath(o.ConfigRoot, "settings.json"))
			if _, err := e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
				t.Fatal("renamed managed model reported complete recovery despite retaining Router connection")
			}
			if !bytes.Equal(beforeModels, get(t, path)) || !bytes.Equal(beforeSettings, get(t, hostPath(o.ConfigRoot, "settings.json"))) {
				t.Fatal("identity conflict changed host configuration")
			}
		})
	}
}

func TestWorkBuddyManuallyRemovedCreatedModelIsAlreadyRestored(t *testing.T) {
	e, o := workBuddySetup(t)
	put(t, hostPath(o.ConfigRoot, "models.json"), []byte(`{"models":[],"unknown":true}`))
	mustApply(t, e, o)
	put(t, hostPath(o.ConfigRoot, "models.json"), []byte(`{"models":[{"id":"later-model","url":"https://later.fixture.invalid"}],"unknown":true,"later":true}`))
	e.Checker = nil
	mustRestore(t, e, o)
	if workBuddyTarget(t, get(t, hostPath(o.ConfigRoot, "models.json")), o.Model) != nil || workBuddyTarget(t, get(t, hostPath(o.ConfigRoot, "models.json")), "later-model") == nil {
		t.Fatal("restore recreated removed target or removed unrelated later Provider")
	}
}

func TestWorkBuddyOpaqueNamesAndOtherProviderValuesRemainUntouched(t *testing.T) {
	e, o := workBuddySetup(t)
	path := hostPath(o.ConfigRoot, "models.json")
	put(t, path, []byte(`{"models":[{"id":"target-model","name":{"opaque":"target name"},"url":"https://original.fixture.invalid","apiKey":"fixture-original","extension":[1,null]},{"id":"other-model","name":null,"url":{"opaque":"endpoint"},"apiKey":{"command":"fixture-secret-command"},"supportsToolCall":"host extension","useCustomProtocol":3}],"availableModels":{"opaque":true}}`))
	mustApply(t, e, o)
	doc := workBuddyTestModels(t, get(t, path))
	target := workBuddyDocTarget(t, doc, o.Model)
	if target["name"].(map[string]any)["opaque"] != "target name" || workBuddyDocTarget(t, doc, "other-model")["apiKey"].(map[string]any)["command"] != "fixture-secret-command" {
		t.Fatal("apply rewrote opaque names or another Provider's values")
	}
	// Neither a target's unowned name nor another Provider is restoration policy.
	target["name"] = []any{"later", "name"}
	workBuddyDocTarget(t, doc, "other-model")["apiKey"] = false
	workBuddyWriteJSON(t, path, doc)
	mustRestore(t, e, o)
	doc = workBuddyTestModels(t, get(t, path))
	target = workBuddyDocTarget(t, doc, o.Model)
	if target["url"] != "https://original.fixture.invalid" || target["apiKey"] != "fixture-original" || len(target["name"].([]any)) != 2 || workBuddyDocTarget(t, doc, "other-model")["apiKey"] != false || doc["availableModels"].(map[string]any)["opaque"] != true {
		t.Fatal("restore lost original fields or changed an unowned value")
	}
}
