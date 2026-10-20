package routermanifest

import (
	"strings"
	"testing"
)

func fixedWindowsRuntime(t *testing.T) Runtime {
	t.Helper()
	r, ok := Fixed().Runtime("windows-amd64-cpu")
	if !ok || r.Dependencies == nil {
		t.Fatal("fixed Windows runtime has no pinned native dependencies")
	}
	return r
}

func TestWindowsDependenciesPinOfficialReleaseFiles(t *testing.T) {
	r := fixedWindowsRuntime(t)
	d := r.Dependencies
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Version != "14.50.35719.0" || d.Architecture != "amd64" || len(d.Files) != 6 {
		t.Fatalf("unexpected fixed Windows dependency set: %+v", d)
	}
	if d.Artifact.Size != 3313422 || d.Artifact.SHA256 != "a4ab48822362df6e478eb4ed33bf825a07a6faacd6879c1e3d59929cd38b984b" || d.Artifact.Format != "zip" {
		t.Fatal("dependency archive differs from downloaded and hashed Microsoft payload", d.Artifact)
	}
	const officialURL = "https://download.visualstudio.microsoft.com/download/pr/a424e95b-20f6-4af6-844f-9d9a806080a7/a4ab48822362df6e478eb4ed33bf825a07a6faacd6879c1e3d59929cd38b984b/Microsoft.VC.14.50.18.0.CRT.Redist.X64.base.vsix"
	if d.Artifact.URL != officialURL || d.LicenseURL != "https://learn.microsoft.com/en-us/visualstudio/releases/2026/redistribution" {
		t.Fatal("dependency source/license must remain the reviewed Microsoft sources")
	}
	const prefix = "Contents/VC/Redist/MSVC/14.50.35710/x64/Microsoft.VC145.CRT/"
	want := map[string]struct {
		size int64
		hash string
	}{
		"msvcp140.dll":             {553552, "def46aa6a8f72f27bafac0c43334419486a4d1dcdb6c479a8ef7034b3e1fa4cb"},
		"vcruntime140.dll":         {123472, "184146852727a9db4eea06178716bec3cdbb1015c911f6b0f915b184ad7775b2"},
		"vcruntime140_1.dll":       {47264, "e6bfb3662ab4b1969a73441dbe35c96d51441b6bff8cf1fe7430bd5b246ca605"},
		"msvcp140_1.dll":           {35488, "2dd670f874562fbdca5b022df1943d70a57ba91fde559280e3a1daebe4db2380"},
		"msvcp140_atomic_wait.dll": {48800, "e7963645e0d1db08e300614d4c5fa7194bd8173e9ab7a5558859e6b232ed3241"},
		"concrt140.dll":            {321696, "b2faf3b85b23c840b654e57d5497a0ad31acd02fb01856cad4725a1715d5f78e"},
	}
	seen := make(map[string]bool, len(d.Files))
	var total int64
	for _, file := range d.Files {
		pin, ok := want[file.Name]
		if !ok || seen[file.Name] || file.Size != pin.size || file.SHA256 != pin.hash || file.Version != d.Version || file.Source != prefix+file.Name || strings.Contains(strings.ToLower(file.Source), "debug_nonredist") {
			t.Fatalf("file differs from independently verified official release bytes: %+v", file)
		}
		seen[file.Name] = true
		total += file.Size
	}
	if d.Artifact.ExpandedBytes < total || d.Artifact.ExpandedBytes > 32<<20 {
		t.Fatal("dependency ZIP expansion needs a bounded reservation")
	}
	for _, runtime := range Fixed().Runtimes {
		if runtime.Platform != "windows" && runtime.Dependencies != nil {
			t.Fatalf("Windows prerequisites leaked onto %s", runtime.ID)
		}
	}
}

func TestWindowsDependenciesAreDetachedAndBindCapabilityFingerprint(t *testing.T) {
	r := fixedWindowsRuntime(t)
	m := Fixed().Models[0]
	original := m.Fingerprint(r)
	for _, field := range []string{"dependency-version", "archive-hash", "single-file-hash", "single-file-size", "single-file-source", "removed-dependency"} {
		t.Run(field, func(t *testing.T) {
			changed := r
			copy := *r.Dependencies
			copy.Files = append([]DependencyFile(nil), r.Dependencies.Files...)
			changed.Dependencies = &copy
			switch field {
			case "dependency-version":
				changed.Dependencies.Version = "14.50.35720.0"
			case "archive-hash":
				changed.Dependencies.Artifact.SHA256 = strings.Repeat("0", 64)
			case "single-file-hash":
				changed.Dependencies.Files[0].SHA256 = strings.Repeat("0", 64)
			case "single-file-size":
				changed.Dependencies.Files[0].Size++
			case "single-file-source":
				changed.Dependencies.Files[0].Source = "other/release/msvcp140.dll"
			case "removed-dependency":
				changed.Dependencies = nil
			}
			if m.Fingerprint(changed) == original {
				t.Fatalf("%s did not invalidate capability identity", field)
			}
		})
	}
	r.Dependencies.Version = "mutated"
	r.Dependencies.Artifact.URL = "https://invalid.example/mutated"
	r.Dependencies.Files[0].SHA256 = "mutated"
	r.Dependencies.Files = append(r.Dependencies.Files, DependencyFile{Name: "injected.dll"})
	fresh := fixedWindowsRuntime(t)
	if fresh.Dependencies.Version != "14.50.35719.0" || len(fresh.Dependencies.Files) != 6 || fresh.Dependencies.Files[0].SHA256 == "mutated" || m.Fingerprint(fresh) != original {
		t.Fatal("Fixed exposed shared mutable dependency state")
	}
}

func TestWindowsDependenciesRejectUnsafeOrIncompleteMetadata(t *testing.T) {
	for _, kind := range []string{"missing-id", "dot-id", "parent-id", "nested-id", "reserved-id", "missing-version", "wrong-architecture", "empty-files", "too-many-files", "archive-url", "archive-hash", "archive-size", "archive-format", "expanded-total", "file-hash", "file-size", "oversized-file", "file-version", "file-extension", "nested-name", "reserved-name", "wildcard-name", "receipt-name", "unsafe-source", "dot-source", "reserved-source", "debug-source", "duplicate-name", "duplicate-source"} {
		t.Run(kind, func(t *testing.T) {
			d := *fixedWindowsRuntime(t).Dependencies
			d.Files = append([]DependencyFile(nil), d.Files...)
			switch kind {
			case "missing-id":
				d.ID = ""
			case "dot-id":
				d.ID = "."
			case "parent-id":
				d.ID = ".."
			case "nested-id":
				d.ID = "sub/deps"
			case "reserved-id":
				d.ID = "NUL"
			case "missing-version":
				d.Version = ""
			case "wrong-architecture":
				d.Architecture = "arm64"
			case "empty-files":
				d.Files = nil
			case "too-many-files":
				for len(d.Files) <= 32 {
					d.Files = append(d.Files, d.Files[0])
				}
			case "archive-url":
				d.Artifact.URL = "file:///private"
			case "archive-hash":
				d.Artifact.SHA256 = "unknown"
			case "archive-size":
				d.Artifact.Size = 0
			case "archive-format":
				d.Artifact.Format = "tar.gz"
			case "expanded-total":
				d.Artifact.ExpandedBytes = d.Files[0].Size
			case "file-hash":
				d.Files[0].SHA256 = "unknown"
			case "file-size":
				d.Files[0].Size = 0
			case "oversized-file":
				d.Files[0].Size = (20 << 20) + 1
			case "file-version":
				d.Files[0].Version = "14.32.31326.0"
			case "file-extension":
				d.Files[0].Name = "unreviewed.exe"
			case "nested-name":
				d.Files[0].Name = "sub/msvcp140.dll"
			case "reserved-name":
				d.Files[0].Name = "NUL.dll"
			case "wildcard-name":
				d.Files[0].Name = "x?.dll"
			case "receipt-name":
				d.Files[0].Name = ".router-verified.json"
			case "unsafe-source":
				d.Files[0].Source = "../msvcp140.dll"
			case "dot-source":
				d.Files[0].Source = "."
			case "reserved-source":
				d.Files[0].Source = "release/NUL.dll"
			case "debug-source":
				d.Files[0].Source = "Contents/VC/Redist/debug_nonredist/x64/msvcp140d.dll"
			case "duplicate-name":
				d.Files[1].Name = strings.ToUpper(d.Files[0].Name)
			case "duplicate-source":
				d.Files[1].Source = d.Files[0].Source
			}
			if err := d.Validate(); err == nil {
				t.Fatalf("unsafe/incomplete dependency metadata accepted: %s", kind)
			}
		})
	}
}
