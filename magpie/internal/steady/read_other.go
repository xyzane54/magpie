//go:build !windows

package steady

import "os"

func readOnce(path string) ([]byte, error) { return os.ReadFile(path) }
