package routerprocess

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func dependencyPlatformPath(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("dependency path contains a reparse point")
	}
	return nil
}

func dependencyPlatformFile(f *os.File, _ os.FileInfo) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("dependency file must not be a hard link or reparse point")
	}
	return nil
}

func dependencyPlatformEnvironment(base, extra []string) ([]string, error) {
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, dependencyError(DependencyLayoutCode, err)
	}
	windowsDir, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, dependencyError(DependencyLayoutCode, err)
	}
	return dependencyEnvironment(base, extra, systemDir, windowsDir)
}
