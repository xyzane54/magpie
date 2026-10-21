package steady

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Reads hold one generation without denying an authorized replacement.
// Keep ordinary Win32 path resolution, including links outside the parent.
func openReadFile(path string) (*os.File, error) { return openSharedFile(path, syscall.GENERIC_READ) }

func openSharedFile(path string, access uint32) (*os.File, error) {
	if path == "" {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ERROR_FILE_NOT_FOUND}
	}
	open := func(name string) (syscall.Handle, error) {
		p, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return syscall.InvalidHandle, err
		}
		return syscall.CreateFile(p, access,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	}
	handle, err := open(path)
	if err != nil && (len(path) >= 248 || !filepath.IsAbs(path)) {
		// Match Go's long-path support even when this Windows process needs
		// the extended prefix. FullPath also resolves a long relative path.
		if full, fullErr := syscall.FullPath(path); fullErr == nil && len(full) >= 248 &&
			!strings.HasPrefix(full, `\\?\`) && !strings.HasPrefix(full, `\\.\`) && !strings.HasPrefix(full, `\??\`) {
			if strings.HasPrefix(full, `\\`) {
				full = `\\?\UNC\` + full[2:]
			} else {
				full = `\\?\` + full
			}
			handle, err = open(full)
		}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	if f == nil {
		syscall.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	return f, nil
}

func readOnce(path string) ([]byte, error) {
	f, err := openReadFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
