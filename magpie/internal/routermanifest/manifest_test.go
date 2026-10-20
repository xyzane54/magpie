package routermanifest

import (
	"strings"
	"testing"
)

func TestFixedCatalogHasOnlyVerifiedExecutionArtifacts(t *testing.T) {
	c := Fixed()
	if c.SchemaVersion != 1 || len(c.Models) != 2 {
		t.Fatal(c)
	}
	for _, m := range c.Models {
		if err := m.Artifact.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := m.Template.Validate(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(m.Artifact.URL, m.Revision) || !strings.Contains(m.Template.URL, m.TemplateRevision) || m.Quantization != "Q4_K_M" || m.Purpose != "public_execution_validation_only" || m.InstallationBytes <= m.Artifact.Size || m.License == "" || m.ContextTokens > m.NativeContextTokens {
			t.Fatal(m)
		}
		for _, r := range c.Runtimes {
			if err := r.Artifact.Validate(); err != nil {
				t.Fatal(err)
			}
			if r.Version != "b10828" || r.Commit != "3ad1ba7336986d98592d3e28cafd1a406715351f" {
				t.Fatal(r)
			}
			before := m.Fingerprint(r)
			changed := m
			changed.Revision = "another"
			if changed.Fingerprint(r) == before {
				t.Fatal("revision not bound to capability")
			}
			changed = m
			changed.Template.SHA256 = strings.Repeat("0", 64)
			if changed.Fingerprint(r) == before {
				t.Fatal("template not bound to capability")
			}
			r.Version = "another"
			if m.Fingerprint(r) == before {
				t.Fatal("runtime not bound to capability")
			}
		}
	}
	first := c.Models[0].Artifact.SHA256
	c.Models[0].Artifact.SHA256 = "changed"
	if Fixed().Models[0].Artifact.SHA256 != first {
		t.Fatal("mutable shared catalog")
	}
}

func TestUnknownOrUnsafeArtifactCannotInstall(t *testing.T) {
	good := Fixed().Models[0].Artifact
	for _, edit := range []func(*Artifact){func(a *Artifact) { a.Size = 0 }, func(a *Artifact) { a.SHA256 = "unknown" }, func(a *Artifact) { a.URL = "" }, func(a *Artifact) { a.URL = "file:///private" }, func(a *Artifact) { a.FileName = "../model" }, func(a *Artifact) { a.Format = "unknown" }} {
		a := good
		edit(&a)
		if a.Validate() == nil {
			t.Fatal(a)
		}
	}
}
