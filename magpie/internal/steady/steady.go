// Package steady reads and replaces files that another process may be
// replacing at that moment. On Windows a file being renamed over refuses
// to open, and one held open refuses to be renamed over, both for a
// moment ("Access is denied"): read as none, a file of accounts is no
// accounts, and the next write leaves them out. A file being renamed over
// is also, for a moment (two milliseconds seen), not there at all, to
// another process or thread: Stat and ReadThere wait that out for a file
// that was there. Elsewhere a rename is atomic and these are os.Stat,
// os.ReadFile and os.Rename.
package steady

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// tries and pause bound the wait to half a second: what holds the file is
// another read or rename, done in milliseconds.
const (
	tries = 50
	pause = 10 * time.Millisecond
)

// goneTries and gonePause bound the wait on a file not there to 15ms or
// so: the moment a rename over it leaves is a couple of milliseconds, and
// a file that is really not there is waited on that long.
const (
	goneTries = 15
	gonePause = time.Millisecond
)

// windows is whether files behave as Windows' do; a test sets it.
var windows = runtime.GOOS == "windows"

// stat and readFile perform one stat or shared read; tests stand in for them.
var (
	stat     = statOnce
	readFile = readOnce
)

func retry(f func() error) error {
	return retryContext(context.Background(), f)
}

func retryContext(ctx context.Context, f func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := f()
	for i := 0; err != nil && windows && !errors.Is(err, fs.ErrNotExist) && i < tries; i++ {
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err = f()
	}
	return err
}

// retryGone is retry, waiting out a moment of the file not there too.
func retryGone(f func() error) error {
	err := retry(f)
	for i := 0; err != nil && windows && errors.Is(err, fs.ErrNotExist) && i < goneTries; i++ {
		time.Sleep(gonePause)
		err = retry(f)
	}
	return err
}

// Stat is os.Stat of a file that was there: one being replaced is waited
// for, so it isn't taken for gone. A file not there costs the wait, so a
// caller asking again remembers it gone (filememo does).
func Stat(p string) (fs.FileInfo, error) {
	var fi fs.FileInfo
	err := retryGone(func() (err error) {
		fi, err = stat(p)
		return err
	})
	return fi, err
}

// StatOnce makes one metadata lookup with the platform's replacement sharing.
// A caller that already found a path absent can use it without retrying absence.
func StatOnce(p string) (fs.FileInfo, error) { return stat(p) }

// ReadThere is ReadFile of a file just found there (Stat): not there now is
// it being replaced, waited out.
func ReadThere(p string) ([]byte, error) {
	var b []byte
	err := retryGone(func() (err error) {
		b, err = readFile(p)
		return err
	})
	return b, err
}

// ReadFile is os.ReadFile, waiting out a file being replaced.
func ReadFile(p string) ([]byte, error) {
	return ReadFileContext(context.Background(), p)
}

// ReadFileContext cancels retry waits without leaving a detached reader.
// An in-progress OS read is synchronous and cannot be interrupted by context.
func ReadFileContext(ctx context.Context, p string) ([]byte, error) {
	var b []byte
	err := retryContext(ctx, func() (err error) {
		b, err = readFile(p)
		return err
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b, err
}

// Rename replaces sibling files through the platform's native replacement
// operation, waiting out a reader that refuses replacement. Other moves use
// os.Rename with the same retry policy.
func Rename(oldpath, newpath string) error {
	return retry(func() error { return rename(oldpath, newpath) })
}

// Sync is (*os.File).Sync; a test stands in for it to make a flush fail.
var Sync = (*os.File).Sync

// WriteFile replaces p with b, mode perm, through a temp file beside it
// that is flushed to the disk before it is renamed over p. A rename reaches
// the disk on its own schedule and the data behind it later: a Windows
// machine that went down between the two left logins.json and
// plugin-auth.json at their full length with every byte zero (#1505). A
// write or flush that fails removes the temp file and leaves p as it was.
func WriteFile(p string, b []byte, perm fs.FileMode) error {
	return WriteFileContext(context.Background(), p, b, perm)
}

// WriteFileContext checks cancellation before each stage and while retrying
// rename. A successful final rename is a committed write, even if cancellation
// races with it. OS write/flush/rename calls themselves remain synchronous.
func WriteFileContext(ctx context.Context, p string, b []byte, perm fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = ctx.Err()
		if err == nil {
			err = Sync(tmp)
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = retryContext(ctx, func() error { return rename(tmp.Name(), p) })
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
