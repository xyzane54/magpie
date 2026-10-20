package routerdownload

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func zipBytes(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		h.SetMode(e.mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func tarGzipBytes(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	tw := tar.NewWriter(g)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm()), Size: int64(len(e.data)), Typeflag: tar.TypeReg}
		if e.mode.IsDir() {
			h.Typeflag = tar.TypeDir
			h.Size = 0
		}
		if e.mode&os.ModeSymlink != 0 {
			h.Typeflag = tar.TypeSymlink
			h.Linkname = "outside"
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestExtractPublishesVerifiedDirectory(t *testing.T) {
	entries := []archiveEntry{{"runtime/", nil, os.ModeDir | 0o700}, {"runtime/llama-server", []byte("fake executable bytes"), 0o700}, {"runtime/LICENSE", []byte("fixture license"), 0o600}, {"empty", nil, 0o600}}
	for _, format := range []string{"zip", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			b := zipBytes(t, entries...)
			if format == "tar.gz" {
				b = tarGzipBytes(t, entries...)
			}
			a, p := localArtifact(t, b)
			a.Format = format
			a.ExpandedBytes = 1024
			dest := filepath.Join(t.TempDir(), "installed")
			if err := Extract(context.Background(), a, p, dest); err != nil {
				t.Fatal(err)
			}
			if err := VerifyDirectory(context.Background(), a, dest); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(dest, "runtime", "llama-server")); err != nil || !bytes.Equal(got, entries[1].data) {
				t.Fatalf("executable bytes: %q %v", got, err)
			}
			if err := Extract(context.Background(), a, p, dest); err != nil {
				t.Fatalf("idempotent extraction: %v", err)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".installed.extract-*"))
			if err != nil || len(files) > 0 {
				t.Fatalf("temporary trees left behind %v %v", files, err)
			}
		})
	}
}

func TestExtractRejectsTraversalLinksAliasesAndSpecialPaths(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "C:/absolute", "dir/../../outside", "dir\\outside", "dir/x:stream", "NUL", "aux.txt", "COM1.exe", "trailing.", "trailing ", "dir//file", "dir/./file", receiptName, "sub/" + receiptName} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			for _, format := range []string{"zip", "tar.gz"} {
				entry := archiveEntry{name, []byte("malicious"), 0o600}
				b := zipBytes(t, entry)
				if format == "tar.gz" {
					b = tarGzipBytes(t, entry)
				}
				a, p := localArtifact(t, b)
				a.Format = format
				a.ExpandedBytes = 1024
				dest := filepath.Join(t.TempDir(), "installed")
				if err := Extract(context.Background(), a, p, dest); err == nil {
					t.Fatalf("%s accepted %q", format, name)
				}
				requireMissing(t, dest)
			}
		})
	}
	for _, format := range []string{"zip", "tar.gz"} {
		t.Run(format+"-symlink", func(t *testing.T) {
			e := archiveEntry{"link", nil, os.ModeSymlink | 0o700}
			b := zipBytes(t, e)
			if format == "tar.gz" {
				b = tarGzipBytes(t, e)
			}
			a, p := localArtifact(t, b)
			a.Format = format
			a.ExpandedBytes = 100
			dest := filepath.Join(t.TempDir(), "installed")
			if err := Extract(context.Background(), a, p, dest); err == nil {
				t.Fatal("archive link accepted")
			}
			requireMissing(t, dest)
		})
	}
}

func TestExtractRejectsDuplicateOversizedAndCorruptEntries(t *testing.T) {
	for _, kind := range []string{"duplicate", "case-alias", "expanded-overflow", "bad-crc", "late-failure", "file-directory-collision", "no-files"} {
		t.Run(kind, func(t *testing.T) {
			entries := []archiveEntry{{"file", []byte("fixture payload"), 0o600}}
			switch kind {
			case "duplicate":
				entries = append(entries, entries[0])
			case "case-alias":
				entries = append(entries, archiveEntry{"FILE", []byte("second"), 0o600})
			case "late-failure":
				entries = append(entries, archiveEntry{"../outside", []byte("evil"), 0o600})
			case "file-directory-collision":
				entries = append(entries, archiveEntry{"file/child", []byte("evil"), 0o600})
			case "no-files":
				entries = []archiveEntry{{"empty/", nil, os.ModeDir | 0o700}}
			}
			b := zipBytes(t, entries...)
			if kind == "bad-crc" {
				at := bytes.Index(b, entries[0].data)
				if at < 0 {
					t.Fatal("fixture missing stored payload")
				}
				b[at] ^= 1
			}
			a, p := localArtifact(t, b)
			a.Format = "zip"
			a.ExpandedBytes = 1024
			if kind == "expanded-overflow" {
				a.ExpandedBytes = 1
			}
			dest := filepath.Join(t.TempDir(), "installed")
			if err := Extract(context.Background(), a, p, dest); err == nil {
				t.Fatal("invalid archive installed")
			}
			requireMissing(t, dest)
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".installed.extract-*"))
			if len(files) > 0 {
				t.Fatal("failed extraction retained partial directory")
			}
		})
	}
}

func TestExtractBoundsAndContexts(t *testing.T) {
	b := zipBytes(t, archiveEntry{"file", []byte("payload"), 0o600})
	a, p := localArtifact(t, b)
	a.Format = "zip"
	a.ExpandedBytes = 1024
	dest := filepath.Join(t.TempDir(), "installed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Extract(ctx, a, p, dest); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	requireMissing(t, dest)
	a.ExpandedBytes = 1 << 62
	if err := Extract(context.Background(), a, p, dest); !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("disk bound: %v", err)
	}
	requireMissing(t, dest)
	a.ExpandedBytes = 0
	if err := Extract(context.Background(), a, p, dest); err == nil {
		t.Fatal("unknown expansion accepted")
	}
	a.ExpandedBytes = 1024
	a.SHA256 = strings.Repeat("0", 64)
	if err := Extract(context.Background(), a, p, dest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("unverified archive: %v", err)
	}
}

func TestVerifyDirectoryRejectsChangesAndVersionReuse(t *testing.T) {
	for _, kind := range []string{"changed-file", "missing-file", "extra-file", "bad-receipt", "missing-receipt", "changed-manifest"} {
		t.Run(kind, func(t *testing.T) {
			b := zipBytes(t, archiveEntry{"runtime/llama-server", []byte("bytes"), 0o700})
			a, p := localArtifact(t, b)
			a.Format = "zip"
			a.ExpandedBytes = 1024
			dest := filepath.Join(t.TempDir(), "installed")
			if err := Extract(context.Background(), a, p, dest); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "changed-file":
				if err := os.WriteFile(filepath.Join(dest, "runtime", "llama-server"), []byte("WRONG"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(filepath.Join(dest, "runtime", "llama-server")); err != nil {
					t.Fatal(err)
				}
			case "extra-file":
				if err := os.WriteFile(filepath.Join(dest, "extra"), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "bad-receipt":
				if err := os.WriteFile(filepath.Join(dest, receiptName), []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-receipt":
				if err := os.Remove(filepath.Join(dest, receiptName)); err != nil {
					t.Fatal(err)
				}
			case "changed-manifest":
				a.SHA256 = strings.Repeat("0", 64)
			}
			if err := VerifyDirectory(context.Background(), a, dest); err == nil {
				t.Fatal("invalid installation remained verified")
			}
		})
	}
}

func TestExtractionSymlinkDestinationAndParentRefused(t *testing.T) {
	b := zipBytes(t, archiveEntry{"file", []byte("payload"), 0o600})
	a, p := localArtifact(t, b)
	a.Format = "zip"
	a.ExpandedBytes = 1024
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("native symlink creation unavailable: %v", err)
	}
	if err := Extract(context.Background(), a, p, link); err == nil {
		t.Fatal("linked directory accepted")
	}
	if err := Extract(context.Background(), a, p, filepath.Join(link, "child")); err == nil {
		t.Fatal("linked parent accepted")
	}
	requireMissing(t, filepath.Join(outside, "child"))
	if err := (&Downloader{}).Download(context.Background(), a, filepath.Join(link, "file"), nil); err == nil {
		t.Fatal("linked download parent accepted")
	}
}

func TestVerifyDirectoryDoesNotCreateMissingRecordedParents(t *testing.T) {
	b := zipBytes(t, archiveEntry{"parent/file", []byte("payload"), 0o600})
	a, p := localArtifact(t, b)
	a.Format = "zip"
	a.ExpandedBytes = 1024
	dest := filepath.Join(t.TempDir(), "installed")
	if err := Extract(context.Background(), a, p, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dest, "parent", "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dest, "parent")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectory(context.Background(), a, dest); err == nil {
		t.Fatal("missing recorded parent accepted")
	}
	requireMissing(t, filepath.Join(dest, "parent"))
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestTarDecompressionIsBoundedAfterEndMarker(t *testing.T) {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	tw := tar.NewWriter(g)
	if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(g, zeroReader{}, tarHeaderAllowance+2); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	a, p := localArtifact(t, b.Bytes())
	a.Format = "tar.gz"
	a.ExpandedBytes = 1
	dest := filepath.Join(t.TempDir(), "installed")
	if err := Extract(context.Background(), a, p, dest); err == nil {
		t.Fatal("unbounded trailing decompression was accepted")
	}
	requireMissing(t, dest)
}
