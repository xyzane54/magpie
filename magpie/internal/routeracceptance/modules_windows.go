//go:build windows

package routeracceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/yetone/magpie/internal/routerprocess"
	"golang.org/x/sys/windows"
)

func nativeLoaderState() (any, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDllDirectoryW")
	buffer := make([]uint16, 32768)
	// GetDllDirectoryW legitimately returns zero for the default empty path.
	// Clear thread-local last-error so unrelated earlier calls cannot mask it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	kernel32.NewProc("SetLastError").Call(0)
	length, _, callErr := proc.Call(uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	if length >= uintptr(len(buffer)) {
		return nil, errors.New("native DLL directory exceeds observation bound")
	}
	value := windows.UTF16ToString(buffer)
	// Empty is the documented default and returns zero without an OS error.
	if length == 0 && callErr != windows.ERROR_SUCCESS {
		return nil, callErr
	}
	return map[string]any{"dllDirectory": value, "manualOverride": value != ""}, check(value == "", "process inherited a nondefault DLL-directory override")
}

type loadedModule struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

func loadedDependencyModules(ctx context.Context, pid int, spec routerprocess.Spec) (any, error) {
	if err := noExplicitEnvironment(spec); err != nil {
		return nil, err
	}
	if err := routerprocess.ValidateDependencies(ctx, spec.Executable, spec.RuntimeDependencies); err != nil {
		return nil, err
	}
	if spec.RuntimeDependencies == nil {
		return nil, errors.New("native Windows product started without dependency contract")
	}
	handle, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	expected := map[string]routerprocess.DependencyFile{}
	for _, file := range spec.RuntimeDependencies.Files {
		expected[strings.ToLower(file.Name)] = file
	}
	found := map[string]bool{}
	result := []loadedModule{}
	entry := windows.ModuleEntry32{Size: uint32(unsafe.Sizeof(windows.ModuleEntry32{}))}
	err = windows.Module32First(handle, &entry)
	for err == nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := strings.ToLower(windows.UTF16ToString(entry.Module[:]))
		if pinned, ok := expected[name]; ok {
			path := windows.UTF16ToString(entry.ExePath[:])
			if !strings.EqualFold(filepath.Clean(path), filepath.Clean(pinned.Path)) {
				return result, fmt.Errorf("%s loaded outside pinned execution directory: %s", name, path)
			}
			f, openErr := os.Open(path)
			if openErr != nil {
				return result, openErr
			}
			hash := sha256.New()
			size, readErr := io.Copy(hash, io.LimitReader(f, (20<<20)+1))
			f.Close()
			digest := hex.EncodeToString(hash.Sum(nil))
			if readErr != nil || size != pinned.Size || digest != pinned.SHA256 {
				return result, errors.Join(readErr, fmt.Errorf("loaded module bytes differ from pin: %s", name))
			}
			result = append(result, loadedModule{Name: name, Path: path, Size: size, SHA256: digest, Version: pinned.Version})
			found[name] = true
		}
		err = windows.Module32Next(handle, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return result, err
	}
	for _, name := range []string{"msvcp140.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		if !found[name] {
			return result, fmt.Errorf("required native module was not observed: %s", name)
		}
	}
	return result, nil
}
