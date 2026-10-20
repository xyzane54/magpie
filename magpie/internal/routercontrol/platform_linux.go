package routercontrol

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/yetone/magpie/internal/dirs"
)

func hide(cmd *exec.Cmd)          {}
func reparse(fi os.FileInfo) bool { return false }
func processIdentity(pid int) (string, string, error) {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(string(b)[strings.LastIndex(string(b), ")")+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return "", "", errors.New("process exited or unknown")
	}
	return fields[19], exe, nil
}
func AutostartStatus(l dirs.Layout, exe string) (Autostart, error) {
	return Autostart{Name: entryName(l)}, nil
}
func SetAutostart(l dirs.Layout, exe string, enabled bool) (Autostart, error) {
	return Autostart{}, errors.New("login autostart is supported only on Windows")
}
