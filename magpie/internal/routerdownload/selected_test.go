package routerdownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func selectedPin(source, name string, data []byte) SelectedFile {
	hash := sha256.Sum256(data)
	return SelectedFile{Source: source, Name: name, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
}

func TestExtractSelectedPublishesOnlyPinnedFlatFiles(t *testing.T) {
	first, second := []byte("release CRT bytes"), []byte("second release DLL")
	b := zipBytes(t,
		archiveEntry{"Contents/VC/Redist/x64/", nil, os.ModeDir | 0o700},
		archiveEntry{"Contents/VC/Redist/x64/msvcp140.dll", first, 0o600},
		archiveEntry{"Contents/VC/Redist/x64/vcruntime140.dll", second, 0o600},
		archiveEntry{"Contents/VC/Redist/debug_nonredist/msvcp140d.dll", []byte("debug DLL must remain in the archive"), 0o600},
		archiveEntry{"manifest.json", []byte("source package metadata"), 0o600})
	a, archive := localArtifact(t, b)
	a.Format, a.ExpandedBytes = "zip", 1024
	files := []SelectedFile{selectedPin("Contents/VC/Redist/x64/msvcp140.dll", "msvcp140.dll", first), selectedPin("Contents/VC/Redist/x64/vcruntime140.dll", "vcruntime140.dll", second)}
	dest := filepath.Join(t.TempDir(), "private-crt")
	for range 2 {
		if err := ExtractSelected(context.Background(), a, archive, dest, files); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyDirectory(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 3 {
		t.Fatalf("selected tree contains unexpected entries: %v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "msvcp140.dll" && entry.Name() != "vcruntime140.dll" && entry.Name() != receiptName) {
			t.Fatalf("unselected archive member reached disk: %s", entry.Name())
		}
	}
	data, err := os.ReadFile(filepath.Join(dest, receiptName))
	if err != nil {
		t.Fatal(err)
	}
	var r receipt
	if err := json.Unmarshal(data, &r); err != nil || r.Version != 1 || len(r.Files) != 2 || r.ArtifactSHA256 != a.SHA256 || r.ArtifactSize != a.Size {
		t.Fatalf("selected receipt does not bind exactly the pinned files/archive: %+v %v", r, err)
	}
	for _, pin := range files {
		actual, err := os.ReadFile(filepath.Join(dest, pin.Name))
		if err != nil || int64(len(actual)) != pin.Size {
			t.Fatalf("selected file differs: %s %v", pin.Name, err)
		}
	}
}

func TestExtractSelectedRejectsUnsafeUnselectedMembers(t *testing.T) {
	for _, kind := range []string{"traversal", "absolute", "backslash", "ads", "device", "receipt", "symlink", "special", "duplicate", "case-collision", "file-directory-collision", "expanded-bound"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("pinned release bytes")
			entries := []archiveEntry{{"release/dll", data, 0o600}}
			extra := archiveEntry{"unselected", []byte("not selected"), 0o600}
			switch kind {
			case "traversal":
				extra.name = "../ignored"
			case "absolute":
				extra.name = "/ignored"
			case "backslash":
				extra.name = `debug\ignored`
			case "ads":
				extra.name = "debug/ignored:stream"
			case "device":
				extra.name = "debug/NUL.dll"
			case "receipt":
				extra.name = "debug/" + receiptName
			case "symlink":
				extra.mode = os.ModeSymlink | 0o700
			case "special":
				extra.mode = os.ModeNamedPipe | 0o600
			case "duplicate":
				entries = append(entries, extra)
			case "case-collision":
				entries = append(entries, archiveEntry{"UNSELECTED", []byte("other"), 0o600})
			case "file-directory-collision":
				entries = append(entries, archiveEntry{"unselected/child", []byte("other"), 0o600})
			case "expanded-bound":
				extra.data = bytes.Repeat([]byte("x"), 2048)
			}
			entries = append(entries, extra)
			a, archive := localArtifact(t, zipBytes(t, entries...))
			a.Format, a.ExpandedBytes = "zip", 1024
			dest := filepath.Join(t.TempDir(), "private-crt")
			if err := ExtractSelected(context.Background(), a, archive, dest, []SelectedFile{selectedPin("release/dll", "dll", data)}); err == nil {
				t.Fatal("unsafe unselected member was accepted")
			}
			requireMissing(t, dest)
			requireNoSelectedStage(t, dest)
		})
	}
}

func TestExtractSelectedRejectsMissingWrongOrCorruptPinnedMembers(t *testing.T) {
	for _, kind := range []string{"missing", "case-different-source", "directory", "wrong-size", "wrong-hash", "bad-crc"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("pinned release bytes")
			entry := archiveEntry{"release/dll", data, 0o600}
			pin := selectedPin(entry.name, "dll", data)
			switch kind {
			case "missing":
				pin.Source = "release/missing"
			case "case-different-source":
				pin.Source = "Release/dll"
			case "directory":
				entry.name, entry.data, entry.mode = "release/dll/", nil, os.ModeDir|0o700
			case "wrong-size":
				pin.Size++
			case "wrong-hash":
				pin.SHA256 = strings.Repeat("0", 64)
			}
			b := zipBytes(t, entry)
			if kind == "bad-crc" {
				at := bytes.Index(b, data)
				if at < 0 {
					t.Fatal("stored fixture payload missing")
				}
				b[at] ^= 1
			}
			a, archive := localArtifact(t, b)
			a.Format, a.ExpandedBytes = "zip", 1024
			dest := filepath.Join(t.TempDir(), "private-crt")
			if err := ExtractSelected(context.Background(), a, archive, dest, []SelectedFile{pin}); err == nil {
				t.Fatal("invalid selected file was published")
			}
			requireMissing(t, dest)
			requireNoSelectedStage(t, dest)
		})
	}
}

func TestExtractSelectedRejectsInvalidPinsAndUnverifiedArchive(t *testing.T) {
	for _, kind := range []string{"empty-list", "source-traversal", "destination-traversal", "nested-name", "reserved-name", "receipt-name", "zero-size", "bad-sha", "duplicate-source", "duplicate-name", "unknown-expansion", "wrong-format", "archive-hash", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("pinned release bytes")
			a, archive := localArtifact(t, zipBytes(t, archiveEntry{"release/dll", data, 0o600}))
			a.Format, a.ExpandedBytes = "zip", 1024
			pin := selectedPin("release/dll", "dll", data)
			files := []SelectedFile{pin}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "empty-list":
				files = nil
			case "source-traversal":
				files[0].Source = "../dll"
			case "destination-traversal":
				files[0].Name = "../dll"
			case "nested-name":
				files[0].Name = "sub/dll"
			case "reserved-name":
				files[0].Name = "CON.dll"
			case "receipt-name":
				files[0].Name = receiptName
			case "zero-size":
				files[0].Size = 0
			case "bad-sha":
				files[0].SHA256 = "unknown"
			case "duplicate-source":
				other := pin
				other.Source, other.Name = "RELEASE/dll", "other"
				files = append(files, other)
			case "duplicate-name":
				other := pin
				other.Source, other.Name = "release/other", "DLL"
				files = append(files, other)
			case "unknown-expansion":
				a.ExpandedBytes = 0
			case "wrong-format":
				a.Format = "tar.gz"
			case "archive-hash":
				a.SHA256 = strings.Repeat("0", 64)
			case "canceled":
				cancel()
			}
			dest := filepath.Join(t.TempDir(), "private-crt")
			err := ExtractSelected(ctx, a, archive, dest, files)
			if err == nil {
				t.Fatal("invalid pins or unverified archive accepted")
			}
			if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation classification changed: %v", err)
			}
			requireMissing(t, dest)
		})
	}
}

func TestExtractSelectedRejectsReceiptRewritesAndSelectionChanges(t *testing.T) {
	for _, kind := range []string{"rewritten-hash", "different-selection", "missing-source", "extra-directory"} {
		t.Run(kind, func(t *testing.T) {
			data, other := []byte("GOOD"), []byte("NEXT")
			a, archive := localArtifact(t, zipBytes(t, archiveEntry{"release/dll", data, 0o600}, archiveEntry{"release/other", other, 0o600}))
			a.Format, a.ExpandedBytes = "zip", 1024
			files := []SelectedFile{selectedPin("release/dll", "dll", data)}
			dest := filepath.Join(t.TempDir(), "private-crt")
			if err := ExtractSelected(context.Background(), a, archive, dest, files); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "rewritten-hash":
				bad := []byte("EVIL")
				if err := os.WriteFile(filepath.Join(dest, "dll"), bad, 0o600); err != nil {
					t.Fatal(err)
				}
				modified := selectedPin("release/dll", "dll", bad)
				b, _ := json.Marshal(receipt{Version: 1, ArtifactSHA256: a.SHA256, ArtifactSize: a.Size, Files: []extractedFile{{Path: "dll", Size: modified.Size, SHA256: modified.SHA256}}})
				if err := os.WriteFile(filepath.Join(dest, receiptName), b, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := VerifyDirectory(context.Background(), a, dest); err != nil {
					t.Fatalf("fixture should satisfy the rewritten local receipt: %v", err)
				}
			case "different-selection":
				files = []SelectedFile{selectedPin("release/other", "other", other)}
			case "missing-source":
				files[0].Source = "release/missing"
			case "extra-directory":
				if err := os.Mkdir(filepath.Join(dest, "debug_nonredist"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := ExtractSelected(context.Background(), a, archive, dest, files); err == nil {
				t.Fatal("existing tree bypassed the fixed selected file list")
			}
			if _, err := os.Stat(filepath.Join(dest, "dll")); err != nil {
				t.Fatal("invalid existing tree was modified instead of retained")
			}
		})
	}
}

func TestExtractSelectedBoundsUnselectedEntryCount(t *testing.T) {
	data := []byte("pinned release bytes")
	entries := make([]archiveEntry, maxEntries+1)
	entries[0] = archiveEntry{"release/dll", data, 0o600}
	for i := 1; i < len(entries); i++ {
		entries[i] = archiveEntry{fmt.Sprintf("ignored/%05d", i), nil, 0o600}
	}
	a, archive := localArtifact(t, zipBytes(t, entries...))
	a.Format, a.ExpandedBytes = "zip", 1024
	dest := filepath.Join(t.TempDir(), "private-crt")
	if err := ExtractSelected(context.Background(), a, archive, dest, []SelectedFile{selectedPin("release/dll", "dll", data)}); err == nil {
		t.Fatal("unselected entries bypassed the archive entry-count bound")
	}
	requireMissing(t, dest)
}

func requireNoSelectedStage(t *testing.T, destination string) {
	t.Helper()
	stages, err := filepath.Glob(filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".extract-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("failed selected extraction retained a partial tree: %v %v", stages, err)
	}
}
