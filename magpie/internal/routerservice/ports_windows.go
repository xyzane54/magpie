package routerservice

import (
	"errors"

	"golang.org/x/sys/windows"
)

func listenUnavailable(err error) bool {
	// Winsock reports WSA errors rather than syscall's portable errno values.
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}
