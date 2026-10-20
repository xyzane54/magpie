//go:build !windows

package routerservice

import (
	"errors"
	"syscall"
)

func listenUnavailable(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EACCES)
}
