package routerdownload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routermanifest"
)

func assemblySource(t *testing.T, root, name string, entries ...archiveEntry) (routermanifest.Artifact, string) {
	t.Helper()
	a, archive := localArtifact(t, zipBytes(t, entries...))
	a.Format, a.ExpandedBytes = "zip", 4096
	directory := filepath.Join(root, name)
	if err := Extract(context.Background(), a, archive, directory); err != nil {
		t.Fatal(err)
	}
	return a, directory
}

func assemblyFixture(t *testing.T) (Assembly, string) {
	t.Helper()
	root := t.TempDir()
	base, baseDirectory := assemblySource(t, root, "base",
		archiveEntry{"bin/llama-server.exe", []byte("fixed runtime executable"), 0o700},
		archiveEntry{"bin/msvcp140.dll", []byte("original older CRT"), 0o600},
		archiveEntry{"LICENSE", []byte("runtime license"), 0o600})
	overlay, overlayDirectory := assemblySource(t, root, "overlay",
		archiveEntry{"msvcp140.dll", []byte("verified newer CRT"), 0o600},
		archiveEntry{"vcruntime140.dll", []byte("verified C runtime"), 0o600})
	return Assembly{Base: base, Overlay: overlay, BaseDirectory: baseDirectory, OverlayDirectory: overlayDirectory, OverlayPrefix: "bin"}, filepath.Join(root, "execution")
}

func readAssemblyReceipt(t *testing.T, directory string) assemblyReceipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, receiptName))
	if err != nil {
		t.Fatal(err)
	}
	var result assemblyReceipt
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func writeAssemblyReceipt(t *testing.T, directory string, r assemblyReceipt) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, receiptName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssemblyPublishesSeparateTreeAndReusesVerifiedInstallation(t *testing.T) {
	a, destination := assemblyFixture(t)
	for range 2 {
		if err := a.Install(context.Background(), destination); err != nil {
			t.Fatal(err)
		}
		if err := a.Verify(context.Background(), destination); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"LICENSE": "runtime license", "bin/llama-server.exe": "fixed runtime executable", "bin/msvcp140.dll": "verified newer CRT", "bin/vcruntime140.dll": "verified C runtime"}
	r := readAssemblyReceipt(t, destination)
	if r.Version != 2 || r.Identity == "" || r.BaseSHA256 != a.Base.SHA256 || r.OverlaySHA256 != a.Overlay.SHA256 || len(r.Files) != len(want) {
		t.Fatalf("composition receipt does not bind the two source plans: %+v", r)
	}
	for _, file := range r.Files {
		content, ok := want[file.Path]
		if !ok {
			t.Fatalf("unexpected planned output %s", file.Path)
		}
		actual, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file.Path)))
		if err != nil || string(actual) != content || file.Size != int64(len(content)) {
			t.Fatalf("assembled %s differs: %q %v", file.Path, actual, err)
		}
	}
	old, err := os.ReadFile(filepath.Join(a.BaseDirectory, "bin", "msvcp140.dll"))
	if err != nil || string(old) != "original older CRT" {
		t.Fatalf("composition replaced a file in the original extraction: %q %v", old, err)
	}
	if err := VerifyDirectory(context.Background(), a.Base, a.BaseDirectory); err != nil {
		t.Fatalf("base extraction changed: %v", err)
	}
	if err := VerifyDirectory(context.Background(), a.Overlay, a.OverlayDirectory); err != nil {
		t.Fatalf("dependency extraction changed: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".execution.assemble-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("composition left staging trees: %v %v", stages, err)
	}
}

func TestAssemblyRejectsTamperedOutputAndReceipt(t *testing.T) {
	for _, kind := range []string{"extra", "changed", "missing", "bad-receipt", "rewritten-hash", "changed-identity", "changed-version", "removed-plan-entry", "linked-file", "linked-directory"} {
		t.Run(kind, func(t *testing.T) {
			a, destination := assemblyFixture(t)
			if err := a.Install(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			r := readAssemblyReceipt(t, destination)
			target := filepath.Join(destination, "bin", "msvcp140.dll")
			switch kind {
			case "extra":
				if err := os.WriteFile(filepath.Join(destination, "injected.dll"), []byte("unexpected"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed", "rewritten-hash":
				bad := []byte("malicious replacement")
				if err := os.WriteFile(target, bad, 0o600); err != nil {
					t.Fatal(err)
				}
				if kind == "rewritten-hash" {
					pin := selectedPin("unused", "msvcp140.dll", bad)
					for i := range r.Files {
						if r.Files[i].Path == "bin/msvcp140.dll" {
							r.Files[i].Size, r.Files[i].SHA256 = pin.Size, pin.SHA256
						}
					}
					writeAssemblyReceipt(t, destination, r)
				}
			case "missing":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			case "bad-receipt":
				if err := os.WriteFile(filepath.Join(destination, receiptName), []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed-identity":
				r.Identity = strings.Repeat("0", 64)
				writeAssemblyReceipt(t, destination, r)
			case "changed-version":
				r.Version = 1
				writeAssemblyReceipt(t, destination, r)
			case "removed-plan-entry":
				if err := os.Remove(filepath.Join(destination, "LICENSE")); err != nil {
					t.Fatal(err)
				}
				kept := r.Files[:0]
				for _, file := range r.Files {
					if file.Path != "LICENSE" {
						kept = append(kept, file)
					}
				}
				r.Files = kept
				writeAssemblyReceipt(t, destination, r)
			case "linked-file":
				outside := filepath.Join(t.TempDir(), "msvcp140.dll")
				if err := os.Rename(target, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Skipf("fixture symlink unavailable: %v", err)
				}
			case "linked-directory":
				outside := filepath.Join(t.TempDir(), "bin")
				if err := os.Rename(filepath.Join(destination, "bin"), outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(destination, "bin")); err != nil {
					t.Skipf("fixture directory symlink unavailable: %v", err)
				}
			}
			if err := a.Verify(context.Background(), destination); err == nil {
				t.Fatal("tampered executable tree passed verification")
			}
			if err := a.Install(context.Background(), destination); err == nil {
				t.Fatal("existing damaged tree was silently reused or replaced")
			}
			if err := VerifyDirectory(context.Background(), a.Base, a.BaseDirectory); err != nil {
				t.Fatalf("rejecting the assembly changed the original runtime tree: %v", err)
			}
		})
	}
}

func TestAssemblyBindsCurrentSourcePlansAndRejectsCorruptedSources(t *testing.T) {
	for _, kind := range []string{"base-corrupt", "overlay-corrupt", "base-receipt", "overlay-extra", "prefix-changed", "source-plan-changed"} {
		t.Run(kind, func(t *testing.T) {
			a, destination := assemblyFixture(t)
			if err := a.Install(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(destination, receiptName))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "base-corrupt":
				err = os.WriteFile(filepath.Join(a.BaseDirectory, "bin", "llama-server.exe"), []byte("changed source"), 0o700)
			case "overlay-corrupt":
				err = os.WriteFile(filepath.Join(a.OverlayDirectory, "msvcp140.dll"), []byte("changed source"), 0o600)
			case "base-receipt":
				err = os.WriteFile(filepath.Join(a.BaseDirectory, receiptName), []byte("{broken"), 0o600)
			case "overlay-extra":
				err = os.WriteFile(filepath.Join(a.OverlayDirectory, "extra.dll"), []byte("extra"), 0o600)
			case "prefix-changed":
				a.OverlayPrefix = "elsewhere"
			case "source-plan-changed":
				// A locally self-consistent source plan still cannot redefine an
				// existing assembly's complete output receipt.
				data, readErr := os.ReadFile(filepath.Join(a.BaseDirectory, receiptName))
				if readErr != nil {
					t.Fatal(readErr)
				}
				var r receipt
				if err := json.Unmarshal(data, &r); err != nil {
					t.Fatal(err)
				}
				kept := r.Files[:0]
				for _, file := range r.Files {
					if file.Path != "LICENSE" {
						kept = append(kept, file)
					}
				}
				r.Files = kept
				data, _ = json.Marshal(r)
				if err := os.WriteFile(filepath.Join(a.BaseDirectory, receiptName), data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(a.BaseDirectory, "LICENSE")); err != nil {
					t.Fatal(err)
				}
				if err := VerifyDirectory(context.Background(), a.Base, a.BaseDirectory); err != nil {
					t.Fatalf("source-plan fixture must remain locally verified: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Verify(context.Background(), destination); err == nil {
				t.Fatal("assembly ignored a source verification/plan change")
			}
			if err := a.Install(context.Background(), destination); err == nil {
				t.Fatal("changed sources silently replaced an existing assembly")
			}
			after, err := os.ReadFile(filepath.Join(destination, receiptName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed reuse rewrote output receipt: %v", err)
			}
		})
	}
}

func TestAssemblyRejectsInvalidOverlayPlanWithoutPublishing(t *testing.T) {
	for _, kind := range []string{"unsafe-prefix", "nested-overlay", "case-collision", "file-directory-collision", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			a, destination := assemblyFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "unsafe-prefix":
				a.OverlayPrefix = "../escape"
			case "nested-overlay":
				a.Overlay, a.OverlayDirectory = assemblySource(t, t.TempDir(), "nested", archiveEntry{"nested/dll", []byte("not flat"), 0o600})
			case "case-collision":
				a.Overlay, a.OverlayDirectory = assemblySource(t, t.TempDir(), "case", archiveEntry{"MSVCP140.DLL", []byte("ambiguous casing"), 0o600})
			case "file-directory-collision":
				a.OverlayPrefix = "LICENSE"
			case "canceled":
				cancel()
			}
			err := a.Install(ctx, destination)
			if err == nil {
				t.Fatal("invalid composition plan was published")
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation classification changed: %v", err)
			}
			requireMissing(t, destination)
			stages, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".execution.assemble-*"))
			if err != nil || len(stages) != 0 {
				t.Fatalf("failed composition retained partial staging: %v %v", stages, err)
			}
		})
	}
}

func TestAssemblyRefusesDestinationInsideAnInputWithoutChangingIt(t *testing.T) {
	for _, input := range []string{"base", "overlay"} {
		t.Run(input, func(t *testing.T) {
			a, _ := assemblyFixture(t)
			root, artifact := a.BaseDirectory, a.Base
			if input == "overlay" {
				root, artifact = a.OverlayDirectory, a.Overlay
			}
			if err := a.Install(context.Background(), filepath.Join(root, "execution")); err == nil {
				t.Fatal("assembly was published into its own source extraction")
			}
			if err := VerifyDirectory(context.Background(), artifact, root); err != nil {
				t.Fatalf("rejected destination still changed the source extraction: %v", err)
			}
		})
	}
}
