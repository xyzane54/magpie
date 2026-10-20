package routerhost

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

func TestLoomyReconnectAfterRestoringHostTimeoutDefaults(t *testing.T) {
	e, o := loomySetup(t)
	path := filepath.Join(o.ConfigRoot, "opencode.json")
	put(t, path, []byte(`{"model":"original/model","keep":"original"}`))
	mustApply(t, e, o)
	current, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: "provider.api_router.options.timeout", Value: 1200000}, {Path: "provider.api_router.options.chunkTimeout", Value: 1200000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, current)
	mustRestore(t, e, o)
	restored := get(t, path)
	p, err := e.Preview(o)
	if err != nil {
		t.Fatalf("host defaults retained by restore prevent reconnection: %v", err)
	}
	if err = e.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	mustApply(t, e, o)
	mustRestore(t, e, o)
	if !bytes.Equal(restored, get(t, path)) {
		t.Fatal("reconnection/restore changed preserved host defaults or original settings")
	}
}

func TestLoomyDefaultsOnlyProviderDoesNotAdoptUnknownValues(t *testing.T) {
	for _, provider := range []string{
		`{"options":{}}`,
		`{"options":{"timeout":1200001}}`,
		`{"options":{"timeout":1200000.0}}`,
		`{"options":{"timeout":1200000,"useSessionAuth":false}}`,
		`{"options":{"timeout":1200000,"chunkTimeout":null}}`,
		`{"options":{"timeout":"1200000"}}`,
		`{"options":{"timeout":1200000,"apiKey":"user-key"}}`,
		`{"options":{"timeout":1200000,"baseURL":"https://user.invalid"}}`,
		`{"options":{"timeout":1200000,"headers":{}}}`,
		`{"options":{"timeout":1200000},"name":"user provider"}`,
		`{"options":{"timeout":1200000},"models":{}}`,
	} {
		e, o := loomySetup(t)
		put(t, filepath.Join(o.ConfigRoot, "opencode.json"), []byte(`{"provider":{"api_router":`+provider+`}}`))
		if _, err := e.Preview(o); err == nil {
			t.Fatal("adopted unverified provider", provider)
		}
	}
}

func TestLoomyRuntimeSmallModelRewriteCompatibility(t *testing.T) {
	e, o := loomySetup(t)
	path := filepath.Join(o.ConfigRoot, "opencode.json")
	put(t, path, []byte(`{"model":"imodel/default","small_model":"imodel/default","provider":{"imodel":{"name":"iModel"}}}`))
	o.Model = ""
	o.Models = []string{"spark-x2.5"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("first Connect failed: %v", err)
	}
	o.Models = []string{"group/111"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("second Connect with model retirement failed: %v", err)
	}
	rewriteSmallModel := func() {
		t.Helper()
		updated, err := edit.PatchJSON(get(t, path), []edit.KV{
			{Path: "small_model", Value: "imodel/qwen3.8-flash"},
			{Path: "provider.imodel.models.spark-x2\\.6", Value: map[string]any{"name": "Spark X2.6"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		put(t, path, updated)
	}
	rewriteSmallModel()
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("Connect with same model after Loomy runtime small_model rewrite failed: %v", err)
	}
	rewriteSmallModel()
	o.Models = []string{"group/222"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("Connect switching model after Loomy runtime small_model rewrite failed: %v", err)
	}
	rewriteSmallModel()
	if _, err := e.Preview(o); err != nil {
		t.Fatalf("Preview after Loomy runtime small_model rewrite failed: %v", err)
	}
	rp, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatalf("PreviewRestore after Loomy runtime small_model rewrite failed: %v", err)
	}
	if err := e.Restore(rp); err != nil {
		t.Fatalf("Restore after Loomy runtime small_model rewrite failed: %v", err)
	}
	doc, err := readJSON(get(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "imodel/default" || doc["small_model"] != "imodel/qwen3.8-flash" {
		t.Fatalf("unexpected restored selectors: model=%v small_model=%v", doc["model"], doc["small_model"])
	}
}

func TestLoomyRuntimeMetadataDisplayLabelsAndModelSwitching(t *testing.T) {
	e, o := loomySetup(t)
	path := filepath.Join(o.ConfigRoot, "opencode.json")
	put(t, path, []byte(`{"model":"imodel/default","small_model":"imodel/qwen3.8-flash","provider":{"api_router":{"options":{"timeout":1200000,"chunkTimeout":1200000},"models":{"spark-x2.5":{"limit":{"context":262144,"output":32768}}},"localNote":"local opencode.json may retain model/provider references from local runtime state"}}}`))
	o.Model = ""
	o.Models = []string{"group/111", "spark-x2.5"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("Connect on Loomy runtime stub failed: %v", err)
	}
	updated, err := edit.PatchJSON(get(t, path), []edit.KV{
		{Path: "model", Value: "api_router/spark-x2.5"},
		{Path: "provider.api_router.models.group/111.name", Value: "111 · 路由组"},
		{Path: "provider.api_router.models.spark-x2\\.5.name", Value: "spark-x2.5 · 讯飞星辰 MaaS"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, updated)
	o.Models = []string{"group/111"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("Reconnect after Loomy display label and model switch failed: %v", err)
	}
	rp, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatalf("PreviewRestore after Loomy display label and model switch failed: %v", err)
	}
	if err := e.Restore(rp); err != nil {
		t.Fatalf("Restore after Loomy display label and model switch failed: %v", err)
	}
}

func TestWorkBuddyRestoreAfterSwitchingCustomLocalModel(t *testing.T) {
	e, o := workBuddySetup(t)
	settingsPath := hostPath(o.ConfigRoot, "settings.json")
	put(t, settingsPath, []byte(`{"model":"builtin-model"}`))
	o.Model = ""
	o.Models = []string{"group/111", "spark-x2.5"}
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatalf("WorkBuddy Connect failed: %v", err)
	}
	put(t, settingsPath, []byte(`{"model":"custom-local:spark-x2.5"}`))
	rp, err := e.PreviewRestore(o.Host, o.ConfigRoot)
	if err != nil {
		t.Fatalf("WorkBuddy PreviewRestore after custom-local switch failed: %v", err)
	}
	if err := e.Restore(rp); err != nil {
		t.Fatalf("WorkBuddy Restore after custom-local switch failed: %v", err)
	}
	doc, err := readJSON(get(t, settingsPath))
	if err != nil || doc["model"] != "builtin-model" {
		t.Fatalf("unexpected restored WorkBuddy model: %v (err=%v)", doc["model"], err)
	}
}

