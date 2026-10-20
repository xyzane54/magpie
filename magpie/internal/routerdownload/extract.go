package routerdownload

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/steady"
)

const receiptName = ".router-verified.json"
const maxEntries = 10000
const maxReceiptBytes = 4 << 20

// Tar headers, padding and PAX names also consume decompression work. Bound
// them independently of payload bytes, including data after tar's end marker.
const tarHeaderAllowance = maxEntries * 4096

type extractedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type receipt struct {
	Version        int             `json:"version"`
	ArtifactSHA256 string          `json:"artifactSHA256"`
	ArtifactSize   int64           `json:"artifactSize"`
	Files          []extractedFile `json:"files"`
}

// Extract verifies the archive, extracts into a private sibling directory, and
// atomically publishes the complete tree. Existing verified installations are
// reused; a damaged existing tree fails closed instead of replacing live files.
// ExpandedBytes is a hard total-byte bound, not merely an estimate.
func Extract(ctx context.Context, a routermanifest.Artifact, archivePath, destinationDir string) error {
	if a.Format != "zip" && a.Format != "tar.gz" {
		return errors.New("unsupported artifact archive format")
	}
	if a.ExpandedBytes <= 0 {
		return errors.New("archive requires confirmed expanded-byte bound")
	}
	if err := Verify(ctx, a, archivePath); err != nil {
		return err
	}
	unlock, err := lockDestination(destinationDir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(destinationDir); err == nil {
		return VerifyDirectory(ctx, a, destinationDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	available, err := freeBytes(filepath.Dir(destinationDir))
	if err != nil {
		return fmt.Errorf("check extraction space: %w", err)
	}
	if uint64(a.ExpandedBytes) > available {
		return ErrDiskSpace
	}
	stage, err := os.MkdirTemp(filepath.Dir(destinationDir), "."+filepath.Base(destinationDir)+".extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	e := extractor{ctx: ctx, root: stage, limit: a.ExpandedBytes, seen: make(map[string]bool), receipt: receipt{Version: 1, ArtifactSHA256: strings.ToLower(a.SHA256), ArtifactSize: a.Size}}
	if a.Format == "zip" {
		err = e.zip(archivePath)
	} else {
		err = e.tarGzip(archivePath)
	}
	if err != nil {
		return err
	}
	if len(e.receipt.Files) == 0 {
		return errors.New("archive contains no regular files")
	}
	b, err := json.Marshal(e.receipt)
	if err != nil {
		return err
	}
	if len(b) > maxReceiptBytes {
		return errors.New("extraction receipt too large")
	}
	if err := steady.WriteFileContext(ctx, filepath.Join(stage, receiptName), b, 0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return steady.Rename(stage, destinationDir)
}

type extractor struct {
	ctx          context.Context
	root         string
	limit, total int64
	seen         map[string]bool
	receipt      receipt
}

func (e *extractor) entry(name string, size int64, mode fs.FileMode, source io.Reader) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	clean, err := safeEntry(name)
	if err != nil {
		return err
	}
	key := strings.ToLower(clean)
	if e.seen[key] || len(e.seen) >= maxEntries {
		return errors.New("duplicate or excessive archive entries")
	}
	e.seen[key] = true
	dest := filepath.Join(e.root, filepath.FromSlash(clean))
	if mode.IsDir() {
		return os.MkdirAll(dest, 0o700)
	}
	if !mode.IsRegular() {
		return fmt.Errorf("archive links and special files refused: %s", name)
	}
	if size < 0 || size > e.limit-e.total {
		return errors.New("archive exceeds expanded-byte bound")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	perm := fs.FileMode(0o600)
	if mode&0o111 != 0 {
		perm = 0o700
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(&contextReader{ctx: e.ctx, reader: source}, size+1))
	syncErr, closeErr := f.Sync(), f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return fmt.Errorf("archive entry length differs: %s", name)
	}
	e.total += n
	e.receipt.Files = append(e.receipt.Files, extractedFile{Path: clean, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

func (e *extractor) zip(archivePath string) error {
	z, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) > maxEntries {
		return errors.New("archive has too many entries")
	}
	for _, f := range z.File {
		if f.UncompressedSize64 > uint64(e.limit-e.total) {
			return errors.New("archive exceeds expanded-byte bound")
		}
		if f.Mode().IsDir() {
			if err := e.entry(f.Name, 0, f.Mode(), nil); err != nil {
				return err
			}
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = e.entry(f.Name, int64(f.UncompressedSize64), f.Mode(), r)
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (e *extractor) tarGzip(archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	g, err := gzip.NewReader(&contextReader{ctx: e.ctx, reader: f})
	if err != nil {
		return err
	}
	defer g.Close()
	if e.limit > int64(^uint64(0)>>1)-tarHeaderAllowance-1 {
		return errors.New("archive expanded-byte bound overflows tar stream limit")
	}
	stream := &io.LimitedReader{R: g, N: e.limit + tarHeaderAllowance + 1}
	t := tar.NewReader(stream)
	for {
		h, err := t.Next()
		if err == io.EOF { // Read to gzip EOF as well, checking its CRC and length.
			_, err = io.Copy(io.Discard, &contextReader{ctx: e.ctx, reader: stream})
			if stream.N <= 0 {
				return errors.New("archive exceeds tar decompression bound")
			}
			return err
		}
		if err != nil {
			return err
		}
		mode := h.FileInfo().Mode()
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive links and special files refused: %s", h.Name)
		}
		if err := e.entry(h.Name, h.Size, mode, t); err != nil {
			return err
		}
	}
}

// safeEntry applies portable restrictions, including Windows alternate streams,
// device names, case collisions and trailing-dot aliases even on Unix tests.
func safeEntry(name string) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00:*?\"<>|") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return "", fmt.Errorf("unsafe archive path: %q", name)
	}
	for _, r := range name {
		if r < 32 {
			return "", fmt.Errorf("unsafe archive path: %q", name)
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." || strings.TrimRight(component, ". ") != component || strings.EqualFold(component, receiptName) {
			return "", fmt.Errorf("unsafe archive path: %q", name)
		}
		stem := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
			return "", fmt.Errorf("reserved archive path: %q", name)
		}
	}
	return name, nil
}

// VerifyDirectory rehashes every file recorded during extraction and rejects
// missing, changed, extra and linked files. Receipts are local integrity state,
// bound to the exact pinned archive; they are not a local-adversary signature.
func VerifyDirectory(ctx context.Context, a routermanifest.Artifact, destinationDir string) error {
	if a.ExpandedBytes <= 0 || (a.Format != "zip" && a.Format != "tar.gz") {
		return errors.New("archive requires confirmed format and expanded-byte bound")
	}
	if err := validate(a); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Lstat(destinationDir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation is not a regular directory")
	}
	rp := filepath.Join(destinationDir, receiptName)
	if err := regularOrMissing(rp); err != nil {
		return err
	}
	rf, err := os.Open(rp)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(rf, maxReceiptBytes+1))
	rf.Close()
	if err != nil {
		return err
	}
	if len(b) > maxReceiptBytes {
		return errors.New("extraction receipt too large")
	}
	var r receipt
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.Version != 1 || !strings.EqualFold(r.ArtifactSHA256, a.SHA256) || r.ArtifactSize != a.Size || len(r.Files) == 0 || len(r.Files) > maxEntries {
		return fmt.Errorf("%w: extraction receipt differs", ErrIntegrity)
	}
	return verifyTree(ctx, destinationDir, r.Files, a.ExpandedBytes)
}

func verifyTree(ctx context.Context, destinationDir string, files []extractedFile, limit int64) error {
	want := make(map[string]bool, len(files))
	caseKeys := make(map[string]bool, len(files))
	total := int64(0)
	for _, f := range files {
		clean, err := safeEntry(f.Path)
		if err != nil {
			return err
		}
		if caseKeys[strings.ToLower(clean)] || f.Size < 0 || f.Size > limit-total {
			return fmt.Errorf("%w: invalid extracted-file record", ErrIntegrity)
		}
		caseKeys[strings.ToLower(clean)] = true
		want[clean] = true
		total += f.Size
		if err := verifyExtracted(ctx, destinationDir, f); err != nil {
			return err
		}
	}
	return filepath.WalkDir(destinationDir, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if p == destinationDir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: linked installation entry", ErrIntegrity)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(destinationDir, p)
		if err != nil {
			return err
		}
		if rel == receiptName {
			return nil
		}
		if !want[filepath.ToSlash(rel)] {
			return fmt.Errorf("%w: unexpected installation file", ErrIntegrity)
		}
		return nil
	})
}

func verifyExtracted(ctx context.Context, root string, f extractedFile) error {
	p := filepath.Join(root, filepath.FromSlash(f.Path))
	// Parent symlinks must be refused before opening a recorded child file.
	if err := checkParents(filepath.Dir(p)); err != nil {
		return err
	}
	if err := regularOrMissing(p); err != nil {
		return err
	}
	file, err := os.Open(p)
	if err != nil {
		return err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() != f.Size {
		return fmt.Errorf("%w: extracted file size differs", ErrIntegrity)
	}
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx: ctx, reader: file}); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256) {
		return fmt.Errorf("%w: extracted file hash differs", ErrIntegrity)
	}
	return nil
}
