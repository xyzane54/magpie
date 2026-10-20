package routerdevice

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Probes allows each platform read to be replaced independently, including
// partial errors and cancellation. Error values always discard accompanying
// data. Nil functions produce unknown facts. Implementations must be read-only.
type Probes struct {
	System          func(context.Context) (System, error)
	Memory          func(context.Context) (uint64, error)
	AvailableMemory func(context.Context) (uint64, error)
	Adapters        func(context.Context) ([]Adapter, error)
	Disk            func(context.Context, string) (uint64, error)
	Backends        func(context.Context) ([]Backend, error)
}

func (p Probes) Probe(ctx context.Context, path string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{
		System: observe(ctx, p.System), MemoryBytes: observe(ctx, p.Memory),
		AvailableMemoryBytes: observe(ctx, p.AvailableMemory),
		Adapters:             observe(ctx, p.Adapters), Backends: observe(ctx, p.Backends),
		DiskFreeBytes: Unknown[uint64]("disk probe unavailable"),
	}
	if p.Disk != nil {
		s.DiskFreeBytes = observe(ctx, func(ctx context.Context) (uint64, error) { return p.Disk(ctx, path) })
	}
	if s.MemoryBytes.Value != nil && *s.MemoryBytes.Value == 0 {
		s.MemoryBytes = Unknown[uint64]("invalid physical memory size")
	}
	return s, ctx.Err()
}

func observe[T any](ctx context.Context, probe func(context.Context) (T, error)) Fact[T] {
	if err := ctx.Err(); err != nil {
		return Unknown[T](err.Error())
	}
	if probe == nil {
		return Unknown[T]("probe unavailable")
	}
	v, err := probe(ctx)
	if err != nil {
		return Unknown[T](err.Error())
	}
	if err := ctx.Err(); err != nil {
		return Unknown[T](err.Error())
	}
	return Known(v)
}

// Native constructs an explicit detector. It performs no reads until Detect.
func Native() *Detector { return New(nativeProbes()) }

// existingDirectory locates the destination volume even before first install.
// It ascends only on not-exist; denied access or a non-directory stays an error.
func existingDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("model destination must be absolute")
	}
	for {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return "", errors.New("model destination is not a directory")
			}
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// A dangling symlink is an existing but unreadable destination, not
		// an absent directory on its parent's volume.
		if _, linkErr := os.Lstat(path); linkErr == nil {
			return "", err
		} else if !errors.Is(linkErr, fs.ErrNotExist) {
			return "", linkErr
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		path = parent
	}
}

func byteSize(count, unit uint64) (uint64, error) {
	if unit == 0 || count > math.MaxUint64/unit {
		return 0, errors.New("invalid or overflowing byte size")
	}
	return count * unit, nil
}

func parseLinuxMemAvailable(content string) (uint64, bool) {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "MemAvailable:"))
		if len(fields) == 0 {
			return 0, false
		}
		kib, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		if len(fields) >= 2 && strings.ToLower(fields[1]) != "kb" {
			return 0, false
		}
		b, err := byteSize(kib, 1024)
		return b, err == nil
	}
	return 0, false
}

func baselineBackends(arch string) []Backend {
	state := UnknownAvailability
	if arch == "amd64" || arch == "arm64" {
		state = Available
	}
	return []Backend{
		{Kind: CPU, State: state},
		{Kind: CUDA, State: UnknownAvailability, Reason: "accelerator not probed"},
		{Kind: Vulkan, State: UnknownAvailability, Reason: "accelerator not probed"},
		{Kind: Metal, State: UnknownAvailability, Reason: "accelerator not probed"},
	}
}
