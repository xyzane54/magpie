//go:build !windows && !linux

package routercontrol

import (
	"errors"
	"fmt"
	"github.com/yetone/magpie/internal/dirs"
	"os"
	"os/exec"
	"time"
)

func hide(cmd *exec.Cmd)          {}
func reparse(fi os.FileInfo) bool { return false }

var selfStamp = fmt.Sprint(time.Now().UnixNano())

func processIdentity(pid int) (string, string, error) {
	if pid == os.Getpid() {
		exe, err := os.Executable()
		return selfStamp, exe, err
	}
	return "", "", errors.New("external process control is supported on Windows; Linux is a test runner only")
}
func AutostartStatus(l dirs.Layout, exe string) (Autostart, error) { return Autostart{}, nil }
func SetAutostart(l dirs.Layout, exe string, enabled bool) (Autostart, error) {
	return Autostart{}, errors.New("login autostart is supported only on Windows")
}
