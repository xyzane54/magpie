package steady

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	winapi "golang.org/x/sys/windows"
)

// A successful Windows replacement can still hide the name while another
// thread is publishing it. Coordinate each lookup/read and publication by
// pathname across processes, without creating a lock file during a read.
// A busy mutex is a sharing error, handled by the existing bounded retries.
func lockPath(path string) (func(), error) {
	handle, err := openPathMutex(path)
	if err != nil {
		return nil, err
	}
	// Windows mutex ownership belongs to an OS thread, not a goroutine.
	runtime.LockOSThread()
	// Queue briefly in the kernel: polling a busy mutex lets continuous
	// publishers starve a reader even though each replacement is short.
	state, err := winapi.WaitForSingleObject(handle, uint32(pause/time.Millisecond))
	if err == nil && (state == winapi.WAIT_OBJECT_0 || state == winapi.WAIT_ABANDONED) {
		return func() {
			winapi.ReleaseMutex(handle)
			winapi.CloseHandle(handle)
			runtime.UnlockOSThread()
		}, nil
	}
	winapi.CloseHandle(handle)
	runtime.UnlockOSThread()
	if err == nil {
		err = winapi.ERROR_SHARING_VIOLATION
	}
	return nil, &os.PathError{Op: "lock", Path: path, Err: err}
}

func openPathMutex(path string) (winapi.Handle, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	// Extended and ordinary Win32 spellings must name the same mutex.
	abs = strings.ToUpper(abs)
	if strings.HasPrefix(abs, `\\?\UNC\`) {
		abs = `\\` + abs[len(`\\?\UNC\`):]
	} else {
		abs = strings.TrimPrefix(abs, `\\?\`)
	}
	key := sha256.Sum256([]byte(filepath.Clean(abs)))
	name, err := winapi.UTF16PtrFromString(fmt.Sprintf(`Global\steady-file-%x`, key))
	if err != nil {
		return 0, err
	}
	handle, err := winapi.CreateMutexEx(nil, name, 0, winapi.SYNCHRONIZE|winapi.MUTEX_MODIFY_STATE)
	// x/sys preserves ERROR_ALREADY_EXISTS alongside a valid shared handle.
	if handle != 0 && errors.Is(err, winapi.ERROR_ALREADY_EXISTS) {
		err = nil
	}
	if err != nil {
		if handle != 0 {
			winapi.CloseHandle(handle)
		}
		return 0, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return handle, nil
}
