package routerdevice

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// Linux is a provider implementation with fixture coverage. Native GPU/driver
// and Ubuntu distribution acceptance are explicitly deferred to real hardware.
func nativeProbes() Probes {
	return Probes{
		System: func(context.Context) (System, error) {
			var uts unix.Utsname
			if err := unix.Uname(&uts); err != nil {
				return System{}, err
			}
			return System{OS: "linux", Version: Known(unix.ByteSliceToString(uts.Release[:])), Architecture: Known(runtime.GOARCH), CPU: Unknown[string]("CPU model not probed"), LogicalCPUs: Known(runtime.NumCPU())}, nil
		},
		Memory: func(context.Context) (uint64, error) {
			var info unix.Sysinfo_t
			if err := unix.Sysinfo(&info); err != nil {
				return 0, err
			}
			return byteSize(uint64(info.Totalram), uint64(info.Unit))
		},
		AvailableMemory: func(context.Context) (uint64, error) {
			if data, err := os.ReadFile("/proc/meminfo"); err == nil {
				if avail, ok := parseLinuxMemAvailable(string(data)); ok {
					return avail, nil
				}
			}
			var info unix.Sysinfo_t
			if err := unix.Sysinfo(&info); err != nil {
				return 0, err
			}
			return byteSize(uint64(info.Freeram)+uint64(info.Bufferram), uint64(info.Unit))
		},
		Disk: func(_ context.Context, path string) (uint64, error) {
			path, err := existingDirectory(path)
			if err != nil {
				return 0, err
			}
			var stat unix.Statfs_t
			if err := unix.Statfs(path, &stat); err != nil {
				return 0, err
			}
			if stat.Bsize <= 0 {
				return 0, fmt.Errorf("invalid filesystem block size")
			}
			return byteSize(stat.Bavail, uint64(stat.Bsize))
		},
		Backends: func(context.Context) ([]Backend, error) { return baselineBackends(runtime.GOARCH), nil },
	}
}
