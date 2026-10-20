package routerredact

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routermanifest"
)

func TestPrivacyContextBindingChangesModelIdentityWithoutChangingMaterialOrLimits(t *testing.T) {
	old, err := NewExperimentalPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu"})
	if err != nil {
		t.Fatal(err)
	}
	long, err := NewExperimentalPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu", ContextTokens: 65536})
	if err != nil {
		t.Fatal(err)
	}
	model, _ := routermanifest.Fixed().Model("spark-x2.5-1.7B")
	runtime, _ := routermanifest.Fixed().Runtime("windows-amd64-cpu")
	model.ContextTokens = 65536
	if old.RuntimeFingerprint() == long.RuntimeFingerprint() || long.RuntimeFingerprint() != model.Fingerprint(runtime) {
		t.Fatal("privacy binding did not follow serving window")
	}
	if old.CapabilitySuite() != long.CapabilitySuite() || old.Fingerprints().Config != long.Fingerprints().Config || long.Budget() != 30*time.Second {
		t.Fatal("context changed recognition policy, evidence material or budget")
	}
	oldBody, oldErr := old.Build(context.Background(), Extraction{Versions: old.Versions(), Texts: []string{"fixture"}})
	newBody, newErr := long.Build(context.Background(), Extraction{Versions: long.Versions(), Texts: []string{"fixture"}})
	if oldErr != nil || newErr != nil || !bytes.Equal(oldBody, newBody) {
		t.Fatal("context binding changed privacy generation material or limits")
	}
	for _, window := range []int{-1, 4096, 131072, 1048577} {
		if _, err := NewExperimentalPrivacySpecification(ModelBinding{ModelID: "spark-x2.5-1.7B", RuntimeID: "windows-amd64-cpu", ContextTokens: window}); err == nil {
			t.Fatalf("uncontrolled context %d accepted", window)
		}
	}
}
