package routercontrol

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/steady"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}
func reparse(fi os.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func processIdentity(pid int) (string, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if pid > 0 && errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", "", fmt.Errorf("%w: %v", os.ErrProcessDone, err)
		}
		return "", "", err
	}
	defer windows.CloseHandle(h)
	var created, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return "", "", err
	}
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return "", "", os.ErrProcessDone
	}
	b := make([]uint16, 32768)
	n := uint32(len(b))
	if err = windows.QueryFullProcessImageName(h, 0, &b[0], &n); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%08x%08x", created.HighDateTime, created.LowDateTime), windows.UTF16ToString(b[:n]), nil
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func psLiteral(s string) string         { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func launcherPath(l dirs.Layout) string { return filepath.Join(l.State, "login.ps1") }
func launcher(l dirs.Layout, exe string) []byte {
	return []byte("\xef\xbb\xbf# API-router private login launcher.\r\n$ErrorActionPreference='Stop'\r\n$env:APPDATA=" + psLiteral(filepath.Dir(l.Config)) + "\r\n$env:LOCALAPPDATA=" + psLiteral(filepath.Dir(l.Data)) + "\r\nStart-Process -FilePath " + psLiteral(exe) + " -ArgumentList @('service','start') -WindowStyle Hidden\r\n")
}
func autostartCommand(l dirs.Layout, exe string) string {
	return syscall.EscapeArg(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")) + ` -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File ` + syscall.EscapeArg(launcherPath(l))
}
func AutostartStatus(l dirs.Layout, exe string) (Autostart, error) {
	s := Autostart{Supported: true, Name: entryName(l)}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(s.Name)
	if errors.Is(err, registry.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if value != autostartCommand(l, exe) {
		return s, errors.New("autostart entry differs from this installation; refusing to adopt or overwrite it")
	}
	s.Enabled = true
	b, err := readRegular(launcherPath(l))
	if err != nil {
		return s, fmt.Errorf("autostart launcher unavailable: %w", err)
	}
	if string(b) != string(launcher(l, exe)) {
		return s, errors.New("autostart launcher differs from this installation; refusing to adopt or overwrite it")
	}
	return s, nil
}
func SetAutostart(l dirs.Layout, exe string, enabled bool) (Autostart, error) {
	if !enabled {
		return disableAutostart(l, exe)
	}
	s, err := AutostartStatus(l, exe)
	if err != nil {
		return s, err
	}
	if s.Enabled == enabled {
		return s, nil
	}
	if enabled {
		if err := safePath(exe); err != nil {
			return s, err
		}
		if len(utf16.Encode([]rune(autostartCommand(l, exe)))) > 260 {
			return s, errors.New("Windows login command exceeds 260 characters; choose a shorter user data path")
		}
		if err := safePath(l.State); err != nil {
			return s, err
		}
		if err := os.MkdirAll(l.State, 0o700); err != nil {
			return s, err
		}
		p := launcherPath(l)
		if b, err := readRegular(p); err == nil && string(b) != string(launcher(l, exe)) {
			return s, errors.New("existing login launcher differs; no file or registry entry overwritten")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return s, err
		}
		if err := steady.WriteFile(p, launcher(l, exe), 0o600); err != nil {
			return s, err
		}
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return s, err
	}
	defer k.Close()
	if enabled {
		err = k.SetStringValue(s.Name, autostartCommand(l, exe))
	} else {
		err = k.DeleteValue(s.Name)
	}
	if err != nil {
		return s, err
	}
	if !enabled {
		if err := os.Remove(launcherPath(l)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return s, err
		}
	}
	return AutostartStatus(l, exe)
}

func disableAutostart(l dirs.Layout, exe string) (Autostart, error) {
	s := Autostart{Supported: true, Name: entryName(l)}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(s.Name)
	if errors.Is(err, registry.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if value != autostartCommand(l, exe) {
		return s, errors.New("autostart entry differs; no unrelated entry removed")
	}
	if err = k.DeleteValue(s.Name); err != nil {
		return s, err
	}
	p := launcherPath(l)
	b, err := readRegular(p)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || string(b) != string(launcher(l, exe)) {
		s.Error = "Registration disabled; modified or unreadable login launcher retained."
		return s, nil
	}
	if err = os.Remove(p); err != nil {
		return s, err
	}
	return s, nil
}
