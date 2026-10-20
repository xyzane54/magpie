// Package routerdelivery installs an explicitly supplied, fixed Windows package.
// It does not initialize config, start services, touch hosts or acquire models.
package routerdelivery

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const ReceiptName = ".api-router-install.json"
const maxPackage = 256 << 20
const maxManifest = 64 << 10

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var packageFiles = []string{"api-router.exe", "README-windows.md", "install-windows.ps1", "uninstall-windows.ps1"}

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Product       string `json:"product"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	Package       File   `json:"package"`
	Files         []File `json:"files"`
}

type InstallOptions struct {
	ManifestPath, PackagePath, InstallDir string
	// ProtectedDirs are config/data/model roots; installation may not overlap them.
	ProtectedDirs []string
}

type InstallResult struct {
	InstallDir string `json:"installDir"`
	Executable string `json:"executable"`
	Version    string `json:"version"`
	Reused     bool   `json:"reused"`
}

type receipt struct {
	InstallDir string   `json:"installDir"`
	Manifest   Manifest `json:"manifest"`
}

// ReadManifest accepts only the bounded, closed schema used by package-windows.ps1.
// A local manifest is a trust input, not a publisher signature.
func ReadManifest(name string) (Manifest, error) {
	var m Manifest
	b, err := readPlain(name, maxManifest)
	if err != nil {
		return m, err
	}
	if err := decode(b, &m); err != nil {
		return m, fmt.Errorf("invalid package manifest: %w", err)
	}
	return m, validateManifest(m)
}

func validateManifest(m Manifest) error {
	if m.SchemaVersion != 1 || m.Product != "api-router" || !versionPattern.MatchString(m.Version) {
		return errors.New("manifest requires api-router schema 1 and an explicit fixed version")
	}
	if m.OS != "windows" || m.Architecture != "amd64" {
		return errors.New("unsupported package platform: this installer requires windows amd64")
	}
	if !safeName(m.Package.Name) || !strings.HasSuffix(m.Package.Name, ".zip") || !validFile(m.Package) || len(m.Files) != len(packageFiles) {
		return errors.New("invalid package metadata or managed file list")
	}
	seen := make(map[string]bool)
	var total int64
	for _, f := range m.Files {
		if !allowed(f.Name) || !validFile(f) || seen[strings.ToLower(f.Name)] {
			return errors.New("manifest contains invalid, duplicate or unexpected managed files")
		}
		seen[strings.ToLower(f.Name)] = true
		total += f.Size
	}
	if total > maxPackage {
		return errors.New("package expansion exceeds 256 MiB limit")
	}
	return nil
}

func validFile(f File) bool {
	return f.Size > 0 && f.Size <= maxPackage && hashPattern.MatchString(f.SHA256)
}
func allowed(n string) bool {
	for _, name := range packageFiles {
		if n == name {
			return true
		}
	}
	return false
}
func safeName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, `/\:<>"|?*`) && !strings.HasSuffix(n, ".") && !strings.HasSuffix(n, " ")
}

// Install validates the complete archive before publishing a new directory.
// Existing installs are never replaced; an exact healthy repeat is reused.
func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	var result InstallResult
	root, err := installRoot(opts.InstallDir)
	if err != nil {
		return result, err
	}
	for _, p := range opts.ProtectedDirs {
		if p != "" && (within(root, p) || within(p, root)) {
			return result, errors.New("installation directory must be separate from config, data and models")
		}
	}
	m, err := ReadManifest(opts.ManifestPath)
	if err != nil {
		return result, err
	}
	if err := verifyFile(ctx, opts.PackagePath, m.Package); err != nil {
		return result, fmt.Errorf("package validation failed; existing installation preserved: %w", err)
	}
	if err := mkdirParents(filepath.Dir(root)); err != nil {
		return result, err
	}
	lock := root + ".install-lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		return result, fmt.Errorf("installation is busy or its lock is unavailable: %w", err)
	}
	defer os.Remove(lock)
	stage, err := os.MkdirTemp(filepath.Dir(root), ".api-router-install-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	if err := extract(ctx, m, opts.PackagePath, stage); err != nil {
		return result, fmt.Errorf("package validation failed; existing installation preserved: %w", err)
	}
	result = InstallResult{InstallDir: root, Executable: filepath.Join(root, "api-router.exe"), Version: m.Version}
	if _, err := os.Lstat(root); err == nil {
		r, err := readReceipt(root)
		if err != nil {
			return InstallResult{}, fmt.Errorf("existing directory is not a verified installation; no files changed: %w", err)
		}
		if !sameManifest(m, r.Manifest) {
			return InstallResult{}, errors.New("another version or package is installed; stop Router and follow the manual version replacement instructions")
		}
		if err := verifyTree(ctx, root, m); err != nil {
			return InstallResult{}, fmt.Errorf("same-version installation is damaged; no files changed; preserve it and install into a new directory: %w", err)
		}
		result.Reused = true
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, err
	}
	b, err := json.MarshalIndent(receipt{InstallDir: root, Manifest: m}, "", "  ")
	if err != nil {
		return InstallResult{}, err
	}
	if err := writeExclusive(filepath.Join(stage, ReceiptName), b); err != nil {
		return InstallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return InstallResult{}, err
	}
	if err := checkPath(filepath.Dir(root)); err != nil {
		return InstallResult{}, err
	}
	if err := os.Rename(stage, root); err != nil {
		return InstallResult{}, fmt.Errorf("cannot publish installation; existing files preserved: %w", err)
	}
	return result, nil
}

func extract(ctx context.Context, m Manifest, archive, stage string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) != len(m.Files) {
		return errors.New("archive file count does not match manifest")
	}
	files := make(map[string]File)
	for _, f := range m.Files {
		files[f.Name] = f
	}
	for _, entry := range z.File {
		pin, ok := files[entry.Name]
		if !ok || !entry.Mode().IsRegular() || entry.UncompressedSize64 != uint64(pin.Size) {
			return errors.New("archive contains a link, unsafe/unexpected path, duplicate or wrong-sized entry")
		}
		delete(files, entry.Name)
		r, err := entry.Open()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(stage, entry.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			r.Close()
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(&contextReader{ctx, r}, pin.Size+1))
		syncErr, closeErr, readCloseErr := f.Sync(), f.Close(), r.Close()
		if err := errors.Join(copyErr, syncErr, closeErr, readCloseErr); err != nil {
			return err
		}
		if n != pin.Size || hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
			return fmt.Errorf("archive file checksum or size mismatch: %s", pin.Name)
		}
	}
	return verifyTree(ctx, stage, m)
}

func verifyTree(ctx context.Context, root string, m Manifest) error {
	if err := checkPath(root); err != nil {
		return err
	}
	for _, f := range m.Files {
		if err := verifyFile(ctx, filepath.Join(root, f.Name), f); err != nil {
			return err
		}
	}
	f, err := pe.Open(filepath.Join(root, "api-router.exe"))
	if err != nil {
		return fmt.Errorf("api-router.exe is not a valid Windows PE executable: %w", err)
	}
	defer f.Close()
	if f.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || f.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || f.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return errors.New("api-router.exe has the wrong PE architecture or executable type; windows amd64 required")
	}
	return nil
}

func verifyFile(ctx context.Context, name string, pin File) error {
	if err := checkPath(name); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != pin.Size {
		return fmt.Errorf("file size/type mismatch: %s", pin.Name)
	}
	if err := checkFile(f, info); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return fmt.Errorf("SHA256 mismatch: %s", pin.Name)
	}
	return nil
}

func readReceipt(root string) (receipt, error) {
	var r receipt
	if _, err := installRoot(root); err != nil {
		return r, err
	}
	b, err := readPlain(filepath.Join(root, ReceiptName), maxManifest)
	if err != nil {
		return r, err
	}
	if err := decode(b, &r); err != nil {
		return r, err
	}
	if !strings.EqualFold(filepath.Clean(r.InstallDir), filepath.Clean(root)) {
		return r, errors.New("installation receipt belongs to another directory")
	}
	return r, validateManifest(r.Manifest)
}

func sameManifest(a, b Manifest) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func decode(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func readPlain(name string, limit int64) ([]byte, error) {
	if err := checkPath(name); err != nil {
		return nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Size() > limit || i.Size() <= 0 {
		return nil, errors.New("metadata must be a bounded regular file")
	}
	if err := checkFile(f, i); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

func writeExclusive(name string, b []byte) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func installRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("installation directory must be an absolute path")
	}
	if err := checkPath(root); err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	if filepath.Dir(root) == root || filepath.Dir(filepath.Dir(root)) == filepath.Dir(root) {
		return "", errors.New("installation directory must be a dedicated product subdirectory")
	}
	if err := checkPath(root); err != nil {
		return "", err
	}
	return root, nil
}

func within(child, parent string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(child)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// checkPath rejects symlinks/reparse points at every existing path component.
func checkPath(name string) error {
	if !filepath.IsAbs(name) {
		return errors.New("package and installation paths must be absolute")
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, `\`, "/"), "/") {
		if part == "." || part == ".." {
			return errors.New("installation paths must not contain dot or parent components")
		}
	}
	if err := checkPlatformName(name); err != nil {
		return err
	}
	for p := filepath.Clean(name); ; p = filepath.Dir(p) {
		i, err := os.Lstat(p)
		if err == nil {
			if i.Mode()&os.ModeSymlink != 0 {
				return errors.New("installation paths must not contain links or reparse points")
			}
			if err := checkPlatformPath(p); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func mkdirParents(name string) error {
	if err := checkPath(name); err != nil {
		return err
	}
	if err := os.MkdirAll(name, 0o700); err != nil {
		return err
	}
	return checkPath(name)
}
