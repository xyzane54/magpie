package routerdevice

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func nativeProbes() Probes {
	return Probes{
		System: windowsSystem,
		Memory: func(context.Context) (uint64, error) {
			// Installed RAM avoids classifying an 8 GiB device as <8 GiB merely
			// because firmware reserved a small part of physical address space.
			proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetPhysicallyInstalledSystemMemory")
			if err := proc.Find(); err != nil {
				return 0, err
			}
			var kib uint64
			ok, _, err := proc.Call(uintptr(unsafe.Pointer(&kib)))
			if ok == 0 {
				return 0, err
			}
			return byteSize(kib, 1024)
		},
		AvailableMemory: func(context.Context) (uint64, error) {
			var status struct {
				Length, MemoryLoad                                                                 uint32
				TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, Ex uint64
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
			return status.AvailPhys, nil
		},
		Adapters: windowsAdapters,
		Disk: func(_ context.Context, path string) (uint64, error) {
			path, err := existingDirectory(path)
			if err != nil {
				return 0, err
			}
			p, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return 0, err
			}
			var available uint64
			err = windows.GetDiskFreeSpaceEx(p, &available, nil, nil)
			return available, err
		},
		Backends: func(context.Context) ([]Backend, error) { return baselineBackends(runtime.GOARCH), nil },
	}
}

func windowsSystem(context.Context) (System, error) {
	// SYSTEM_INFO's native architecture also detects an emulated process.
	var info struct {
		Architecture, Reserved                 uint16
		PageSize                               uint32
		Minimum, Maximum, ActiveMask           uintptr
		Processors, ProcessorType, Granularity uint32
		Level, Revision                        uint16
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNativeSystemInfo")
	if err := proc.Find(); err != nil {
		return System{}, err
	}
	proc.Call(uintptr(unsafe.Pointer(&info)))
	s := System{OS: "windows", Architecture: Unknown[string]("unrecognized native architecture"), LogicalCPUs: Known(runtime.NumCPU())}
	switch info.Architecture {
	case 9:
		s.Architecture = Known("amd64")
	case 12:
		s.Architecture = Known("arm64")
	case 0:
		s.Architecture = Known("386")
	}
	v := windows.RtlGetVersion()
	if v != nil {
		s.Version = Known(fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber))
	}
	s.CPU = Unknown[string]("CPU name unavailable")
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		name, _, err := k.GetStringValue("ProcessorNameString")
		if err == nil && name != "" {
			s.CPU = Known(name)
		}
	}
	return s, nil
}
