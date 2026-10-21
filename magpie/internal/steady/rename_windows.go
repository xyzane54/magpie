package steady

import (
	"os"
	"path/filepath"
)

func rename(oldpath, newpath string) error {
	unlock, err := lockPath(newpath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	defer unlock()
	return renameUnlocked(oldpath, newpath)
}

func renameUnlocked(oldpath, newpath string) error {
	if oldpath == "" || newpath == "" || os.IsPathSeparator(oldpath[len(oldpath)-1]) || os.IsPathSeparator(newpath[len(newpath)-1]) {
		return os.Rename(oldpath, newpath)
	}
	// Every staged state write is beside its destination. Root.Rename uses
	// Windows' handle-based POSIX replacement where the filesystem supports
	// it, so an old reader with FILE_SHARE_DELETE does not leave the new
	// generation inaccessible until that handle closes. Root also owns the
	// platform fallback on filesystems without these semantics.
	directory := filepath.Clean(filepath.Dir(oldpath))
	if directory != filepath.Clean(filepath.Dir(newpath)) {
		return os.Rename(oldpath, newpath)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	defer root.Close()
	if err := root.Rename(filepath.Base(oldpath), filepath.Base(newpath)); err != nil {
		if link, ok := err.(*os.LinkError); ok {
			err = link.Err
		}
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}
