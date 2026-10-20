package routerruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
)

func TestConfigSettingsRoundTripPreservesSiblingRuntimeAndAppFields(t *testing.T) {
	root := t.TempDir()
	layout := dirs.Layout{Install: filepath.Join(root, "install"), Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Models: filepath.Join(root, "data", "models"), Runtime: filepath.Join(root, "data", "runtime"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "state", "logs")}
	store := routerconfig.New(layout)
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(d routerconfig.Document) error {
		d["runtime"] = json.RawMessage(`{"enabled":false,"selected":"spark-x2.5-1.7B","future":{"retained":true}}`)
		d["redaction"] = json.RawMessage(`{"enabled":true,"future":{"keep":1}}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	intents := ConfigIntentStore{Store: store}
	i, err := intents.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i.Settings = routerconfig.LongContextRuntimeSettings()
	i.Enabled = true
	if err := intents.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(d routerconfig.Document) error {
		var runtime map[string]json.RawMessage
		_ = json.Unmarshal(d["runtime"], &runtime)
		var settings map[string]json.RawMessage
		_ = json.Unmarshal(runtime["settings"], &settings)
		settings["future"] = json.RawMessage(`{"retained":true}`)
		runtime["settings"], _ = json.Marshal(settings)
		d["runtime"], _ = json.Marshal(runtime)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := intents.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	loaded, err := intents.Load(context.Background())
	if err != nil || loaded != i {
		t.Fatalf("settings roundtrip failed: %+v %v", loaded, err)
	}
	d, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var runtime map[string]json.RawMessage
	if err := json.Unmarshal(d["runtime"], &runtime); err != nil {
		t.Fatal(err)
	}
	var future struct {
		Retained bool `json:"retained"`
	}
	var redaction struct {
		Enabled bool `json:"enabled"`
		Future  struct {
			Keep int `json:"keep"`
		} `json:"future"`
	}
	if json.Unmarshal(runtime["future"], &future) != nil || !future.Retained || json.Unmarshal(d["redaction"], &redaction) != nil || !redaction.Enabled || redaction.Future.Keep != 1 {
		t.Fatal("runtime save overwrote sibling fields")
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(runtime["settings"], &settings) != nil || json.Unmarshal(settings["future"], &future) != nil || !future.Retained {
		t.Fatal("runtime save overwrote unknown nested settings")
	}
	i.Settings = routerconfig.RuntimeSettings{}
	if err := intents.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	d, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	runtime = nil
	if err := json.Unmarshal(d["runtime"], &runtime); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime["settings"]; ok {
		t.Fatal("legacy settings were not omitted")
	}
}
