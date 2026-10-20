//go:build !windows

package routerprocess

import (
	"errors"
	"os"
	"syscall"
)

func dependencyPlatformPath(string) error { return nil }

func dependencyPlatformFile(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("dependency file must not be a hard link")
	}
	return nil
}

func dependencyPlatformEnvironment(_, _ []string) ([]string, error) {
	return nil, dependencyError(DependencyArchitectureCode, errors.New("Windows runtime dependencies cannot be launched on this platform"))
}
