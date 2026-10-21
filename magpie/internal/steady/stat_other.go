//go:build !windows

package steady

import "os"

func statOnce(path string) (os.FileInfo, error) { return os.Stat(path) }
