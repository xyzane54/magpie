package steady

import (
	"os"

	winapi "golang.org/x/sys/windows"
)

func statOnce(path string) (os.FileInfo, error) {
	// Query an opened generation instead of Win32's path-attribute lookup,
	// which can report a replaced name absent. Metadata needs no read-data
	// access and must not deny another publisher's delete access.
	f, err := openSharedFile(path, winapi.FILE_READ_ATTRIBUTES)
	if err != nil {
		if pathErr, ok := err.(*os.PathError); ok {
			return nil, &os.PathError{Op: "stat", Path: path, Err: pathErr.Err}
		}
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
