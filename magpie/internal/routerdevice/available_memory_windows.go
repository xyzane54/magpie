package routerdevice

import (
	"context"
	"golang.org/x/sys/windows"
	"unsafe"
)

func availableMemory(context.Context) (uint64, error) {
	var status struct {
		Length, Load                                             uint32
		TotalPhysical, AvailablePhysical                         uint64
		TotalPageFile, AvailablePageFile                         uint64
		TotalVirtual, AvailableVirtual, AvailableExtendedVirtual uint64
	}
	status.Length = uint32(unsafe.Sizeof(status))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if err := proc.Find(); err != nil {
		return 0, err
	}
	ok, _, err := proc.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return 0, err
	}
	return status.AvailablePhysical, nil
}
