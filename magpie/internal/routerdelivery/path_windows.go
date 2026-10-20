package routerdelivery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func checkPlatformName(path string) error {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return errors.New("installation requires local drive paths; UNC and device namespaces are unsupported")
	}
	for _, part := range strings.Split(strings.ReplaceAll(path[len(volume):], `\`, "/"), "/") {
		if part == "" {
			continue
		}
		if !safeName(part) {
			return errors.New("installation path contains an unsafe Windows alias or character")
		}
		for _, r := range part {
			if r < 32 {
				return errors.New("installation path contains a control character")
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
			return errors.New("installation path contains a reserved Windows device name")
		}
	}
	return nil
}

func checkPlatformPath(path string) error {
	n, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	a, err := windows.GetFileAttributes(n)
	if err != nil {
		return err
	}
	if a&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("installation path contains a Windows reparse point")
	}
	return nil
}

func checkFile(f *os.File, _ os.FileInfo) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("installation file must not be a hard link or reparse point")
	}
	return nil
}
