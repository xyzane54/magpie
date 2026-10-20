package routerprocess

import (
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	DependencyMissingCode      = "local_model_dependency_missing"
	DependencyCorruptCode      = "local_model_dependency_corrupt"
	DependencyArchitectureCode = "local_model_dependency_architecture"
	DependencyVersionCode      = "local_model_dependency_version"
	DependencyLayoutCode       = "local_model_dependency_layout"
	maxDependencyFiles         = 32
	maxDependencyFileBytes     = 64 << 20
)

// DependencySpec binds the app-local Windows DLLs to immutable catalog metadata.
// Paths describe the installed execution tree; hashes and versions must come
// from the catalog, never a receipt generated from the local files themselves.
type DependencySpec struct {
	ID, Version, Architecture string
	Files                     []DependencyFile
}

type DependencyFile struct {
	Path, Name, SHA256, Version string
	Size                        int64
}

// DependencyError exposes a stable management error without requiring callers
// to infer the cause from OS-specific loader text.
type DependencyError struct {
	Code string
	Err  error
}

func (e *DependencyError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *DependencyError) Unwrap() error { return e.Err }

func dependencyError(code string, err error) error { return &DependencyError{Code: code, Err: err} }

// ValidateDependencies does not load a library or start a process. It is
// portable so installer fixtures can validate Windows PE bytes on other hosts.
// A nil spec is reserved for runtimes without this dependency contract and
// explicit fake-engine tests; the Windows production installer requires one.
func ValidateDependencies(ctx context.Context, executable string, spec *DependencySpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec == nil {
		return nil
	}
	if !dependencyID(spec.ID) || len(spec.Files) == 0 || len(spec.Files) > maxDependencyFiles {
		return dependencyError(DependencyLayoutCode, errors.New("invalid dependency manifest"))
	}
	if spec.Architecture != "amd64" {
		return dependencyError(DependencyArchitectureCode, errors.New("dependency architecture must be amd64"))
	}
	if _, err := dependencyVersion(spec.Version); err != nil {
		return dependencyError(DependencyVersionCode, err)
	}
	if !dependencyAbsolutePath(executable) {
		return dependencyError(DependencyLayoutCode, errors.New("executable path must be absolute"))
	}
	dir := filepath.Dir(filepath.Clean(executable))
	if _, err := dependencyPath(executable, false); err != nil {
		return dependencyError(DependencyLayoutCode, fmt.Errorf("unsafe executable: %w", err))
	}
	names, paths := make(map[string]bool), make(map[string]bool)
	for _, item := range spec.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.ToLower(item.Name)
		path := filepath.Clean(item.Path)
		pathKey := path
		if runtime.GOOS == "windows" {
			pathKey = strings.ToLower(pathKey)
		}
		if !dependencyName(item.Name) || !dependencyAbsolutePath(item.Path) || !sameDependencyPath(filepath.Dir(path), dir) ||
			!strings.EqualFold(filepath.Base(path), item.Name) || names[name] || paths[pathKey] {
			return dependencyError(DependencyLayoutCode, errors.New("dependencies must have unique names directly beside the executable"))
		}
		names[name], paths[pathKey] = true, true
		if item.Size <= 0 || item.Size > maxDependencyFileBytes || len(item.SHA256) != 64 {
			return dependencyError(DependencyLayoutCode, errors.New("invalid dependency size or SHA256 metadata"))
		}
		if _, err := hex.DecodeString(item.SHA256); err != nil {
			return dependencyError(DependencyLayoutCode, errors.New("invalid dependency SHA256 metadata"))
		}
		wantVersion, err := dependencyVersion(item.Version)
		if err != nil {
			return dependencyError(DependencyVersionCode, err)
		}
		if item.Version != spec.Version {
			return dependencyError(DependencyVersionCode, errors.New("dependency file version differs from its runtime dependency set"))
		}
		if err := validateDependencyFile(ctx, path, item, wantVersion); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func dependencyID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return id != "." && id != ".."
}

func dependencyName(name string) bool {
	if !dependencyID(name) || !strings.EqualFold(filepath.Ext(name), ".dll") || strings.ContainsAny(name, `/\\:`) {
		return false
	}
	stem, _, _ := strings.Cut(strings.ToUpper(name), ".")
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" {
		return false
	}
	return !(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9')
}

func dependencyAbsolutePath(path string) bool {
	if !filepath.IsAbs(path) || len(path) > 32767 {
		return false
	}
	// Do not let lexical cleaning hide a link that the OS would traverse
	// before resolving a parent component.
	for _, part := range strings.Split(strings.ReplaceAll(path, `\`, "/"), "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func sameDependencyPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Every component is checked, including the execution directory's ancestors:
// a safe-looking leaf beneath a junction still points outside the verified tree.
func dependencyPath(path string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, errors.New("dependency path is a link or has the wrong file type")
	}
	if err := dependencyPlatformPath(path); err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if _, err := dependencyPath(parent, true); err != nil {
			return nil, err
		}
	}
	return info, nil
}

func validateDependencyFile(ctx context.Context, path string, item DependencyFile, wantVersion [4]uint16) error {
	info, err := dependencyPath(path, false)
	if err != nil {
		code := DependencyLayoutCode
		if errors.Is(err, os.ErrNotExist) {
			code = DependencyMissingCode
		}
		return dependencyError(code, fmt.Errorf("%s: %w", item.Name, err))
	}
	f, err := os.Open(path)
	if err != nil {
		return dependencyError(DependencyCorruptCode, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return dependencyError(DependencyCorruptCode, err)
	}
	if !os.SameFile(info, opened) {
		return dependencyError(DependencyLayoutCode, errors.New("dependency changed while opening"))
	}
	if err := dependencyPlatformFile(f, opened); err != nil {
		return dependencyError(DependencyLayoutCode, err)
	}
	if opened.Size() != item.Size {
		return dependencyError(DependencyCorruptCode, fmt.Errorf("%s size mismatch", item.Name))
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	reader := io.LimitReader(f, item.Size+1)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > item.Size {
				return dependencyError(DependencyCorruptCode, errors.New("dependency grew during verification"))
			}
			hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return dependencyError(DependencyCorruptCode, readErr)
		}
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.SHA256) {
		return dependencyError(DependencyCorruptCode, fmt.Errorf("%s SHA256 mismatch", item.Name))
	}
	image, err := pe.NewFile(f)
	if err != nil {
		return dependencyError(DependencyCorruptCode, fmt.Errorf("%s is not a valid PE image: %w", item.Name, err))
	}
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || image.Characteristics&pe.IMAGE_FILE_DLL == 0 {
		return dependencyError(DependencyArchitectureCode, fmt.Errorf("%s must be an amd64 DLL", item.Name))
	}
	if _, ok := image.OptionalHeader.(*pe.OptionalHeader64); !ok {
		return dependencyError(DependencyArchitectureCode, fmt.Errorf("%s must be PE32+", item.Name))
	}
	if err := validateDependencyPEVersions(f, opened.Size(), image, wantVersion); err != nil {
		return dependencyError(DependencyVersionCode, fmt.Errorf("%s: %w", item.Name, err))
	}
	after, err := f.Stat()
	if err != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return dependencyError(DependencyCorruptCode, errors.New("dependency changed during verification"))
	}
	return ctx.Err()
}

func dependencyVersion(value string) ([4]uint16, error) {
	var version [4]uint16
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, errors.New("dependency version must have four numeric components")
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return version, errors.New("invalid dependency version component")
		}
		version[i] = uint16(n)
	}
	return version, nil
}

func dependencyLaunch(ctx context.Context, s Spec) (string, []string, error) {
	if s.RuntimeDependencies == nil {
		return "", append(os.Environ(), s.Env...), nil
	}
	if err := ValidateDependencies(ctx, s.Executable, s.RuntimeDependencies); err != nil {
		return "", nil, err
	}
	env, err := dependencyPlatformEnvironment(os.Environ(), s.Env)
	if err != nil {
		return "", nil, err
	}
	return filepath.Dir(s.Executable), env, nil
}

func dependencyLoaderKey(key string) bool {
	key = strings.ToUpper(key)
	return key == "PATH" || key == "SYSTEMROOT" || key == "WINDIR" || key == "GGML_BACKEND_PATH" || strings.HasPrefix(key, "LLAMA_ARG_")
}

func dependencyEnvironment(base, extra []string, systemDir, windowsDir string) ([]string, error) {
	for _, entry := range extra {
		key, _, _ := strings.Cut(entry, "=")
		if dependencyLoaderKey(key) {
			return nil, dependencyError(DependencyLayoutCode, errors.New("explicit environment cannot override the isolated runtime loader"))
		}
	}
	env := make([]string, 0, len(base)+len(extra)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !dependencyLoaderKey(key) {
			env = append(env, entry)
		}
	}
	env = append(env, extra...)
	return append(env, "PATH="+systemDir+";"+windowsDir, "SystemRoot="+windowsDir, "WINDIR="+windowsDir), nil
}
