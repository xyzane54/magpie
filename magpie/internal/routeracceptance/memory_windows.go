//go:build windows

package routeracceptance

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sampleMemory(pid int) memorySample {
	s := memorySample{At: time.Now().UTC(), PID: pid}
	var status struct {
		Length, MemoryLoad                                                                                   uint32
		TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExtendedVirtual uint64
	}
	status.Length = uint32(unsafe.Sizeof(status))
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		s.Error = fmt.Sprintf("GlobalMemoryStatusEx: %v", err)
	} else {
		s.AvailableRAMBytes, s.TotalRAMBytes = status.AvailPhys, status.TotalPhys
	}
	if pid == 0 {
		return s
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		s.Error += fmt.Sprintf(" OpenProcess: %v", err)
		return s
	}
	defer windows.CloseHandle(handle)
	var counters struct {
		Size, PageFaultCount                                                                               uint32
		PeakWorkingSetSize, WorkingSetSize, QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage                   uintptr
		QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage, PagefileUsage, PeakPagefileUsage, PrivateUsage uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	ok, _, err = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo").Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if ok == 0 {
		s.Error += fmt.Sprintf(" GetProcessMemoryInfo: %v", err)
	} else {
		s.WorkingSetBytes, s.PeakWorkingSetBytes, s.PrivateBytes = uint64(counters.WorkingSetSize), uint64(counters.PeakWorkingSetSize), uint64(counters.PrivateUsage)
	}
	return s
}
