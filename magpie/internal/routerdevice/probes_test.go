package routerdevice

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fixtureProbes(s Snapshot) Probes {
	p := Probes{
		System:   func(context.Context) (System, error) { return *s.System.Value, nil },
		Memory:   func(context.Context) (uint64, error) { return *s.MemoryBytes.Value, nil },
		Adapters: func(context.Context) ([]Adapter, error) { return *s.Adapters.Value, nil },
		Disk:     func(context.Context, string) (uint64, error) { return *s.DiskFreeBytes.Value, nil },
		Backends: func(context.Context) ([]Backend, error) { return *s.Backends.Value, nil },
	}
	if s.AvailableMemoryBytes.Value != nil {
		avail := *s.AvailableMemoryBytes.Value
		p.AvailableMemory = func(context.Context) (uint64, error) { return avail, nil }
	}
	return p
}

func TestPlatformProbesDiscardValuesOnReadFailure(t *testing.T) {
	for _, platform := range []string{"windows", "darwin", "linux"} {
		fail := errors.New("fixture read denied")
		p := Probes{
			System:          func(context.Context) (System, error) { return *fixture(platform, 16*GiB).System.Value, fail },
			Memory:          func(context.Context) (uint64, error) { return 32 * GiB, fail },
			AvailableMemory: func(context.Context) (uint64, error) { return 16 * GiB, fail },
			Adapters: func(context.Context) ([]Adapter, error) {
				return []Adapter{{Discrete: Available, MemoryBytes: Known(8 * GiB)}}, fail
			},
			Disk:     func(context.Context, string) (uint64, error) { return 100 * GiB, fail },
			Backends: func(context.Context) ([]Backend, error) { return []Backend{{Kind: CPU, State: Available}}, fail },
		}
		r := New(p).Detect(context.Background(), t.TempDir(), fixtureRequirements)
		if r.Recommendation.Tier != TierUnknown || r.Detection.State != CheckUnknown {
			t.Fatalf("%s: failed read passed: %+v", platform, r)
		}
		s := r.Device
		if s.System.Value != nil || s.MemoryBytes.Value != nil || s.AvailableMemoryBytes.Value != nil || s.Adapters.Value != nil || s.DiskFreeBytes.Value != nil || s.Backends.Value != nil {
			t.Fatalf("failed read retained numbers: %+v", s)
		}
		// Partial failures do not erase independent successful observations.
		p = fixtureProbes(fixture(platform, 16*GiB))
		p.Memory = func(context.Context) (uint64, error) { return 32 * GiB, fail }
		r = New(p).Detect(context.Background(), t.TempDir(), fixtureRequirements)
		if r.Recommendation.Tier != TierUnknown || r.Device.System.Value == nil || r.Device.DiskFreeBytes.Value == nil {
			t.Fatalf("partial read = %+v", r)
		}
		p.Memory = func(context.Context) (uint64, error) { return 0, nil }
		if r := New(p).Detect(context.Background(), t.TempDir(), fixtureRequirements); r.Device.MemoryBytes.Value != nil {
			t.Fatalf("invalid RAM became measured zero: %+v", r)
		}
	}
}

func TestProbesCancellationDiscardsPartialSuccess(t *testing.T) {
	p := fixtureProbes(fixture("linux", 16*GiB))
	ctx, cancel := context.WithCancel(context.Background())
	p.Memory = func(context.Context) (uint64, error) { cancel(); return 16 * GiB, nil }
	p.Disk = func(context.Context, string) (uint64, error) {
		t.Fatal("continued probing after cancellation")
		return 0, nil
	}
	r := New(p).Detect(ctx, t.TempDir(), fixtureRequirements)
	if r.Device.System.Value != nil || r.Recommendation.Tier != TierUnknown {
		t.Fatalf("canceled partial success: %+v", r)
	}
}

func TestLinuxProviderFixturesAreIndependentOfHost(t *testing.T) {
	// These calls deliberately run on every GOOS. They validate the Linux
	// provider contract, not the operating system of the test runner.
	for _, ram := range []uint64{79 * GiB / 10, 8 * GiB, 16 * GiB, 24 * GiB} {
		p := fixtureProbes(fixture("linux", ram))
		r := New(p).Detect(context.Background(), t.TempDir(), fixtureRequirements)
		if r.Device.System.Value.OS != "linux" || r.Recommendation != Recommend(fixture("linux", ram)) {
			t.Fatalf("host affected Linux fixture: %+v", r)
		}
	}
	if got, err := byteSize(16*GiB/4096, 4096); err != nil || got != 16*GiB {
		t.Fatalf("Sysinfo units: %d %v", got, err)
	}
	for _, input := range [][2]uint64{{math.MaxUint64, 4096}, {16, 0}} {
		if _, err := byteSize(input[0], input[1]); err == nil {
			t.Fatal("invalid units accepted")
		}
	}
	if got, ok := parseLinuxMemAvailable("MemTotal:       16384000 kB\nMemFree:         2048000 kB\nMemAvailable:    8388608 kB\n"); !ok || got != 8*GiB {
		t.Fatalf("parseLinuxMemAvailable = %d %v", got, ok)
	}
	for _, invalid := range []string{"", "MemTotal: 1000 kB", "MemAvailable: bad kB", "MemAvailable: 1000 MB"} {
		if _, ok := parseLinuxMemAvailable(invalid); ok {
			t.Fatalf("accepted invalid MemAvailable %q", invalid)
		}
	}
}

func TestMissingModelDirectoryDoesNotCreateFiles(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "data", "models")
	got, err := existingDirectory(destination)
	if err != nil || got != root {
		t.Fatalf("volume parent = %q %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe created files: %v %v", entries, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := existingDirectory(file); err == nil {
		t.Fatal("file accepted as model directory")
	}
}

func TestDanglingModelSymlinkDoesNotMeasureParentVolume(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "models")
	if err := os.Symlink(filepath.Join(root, "missing-volume", "models"), link); err != nil {
		t.Skipf("symlink permission unavailable: %v", err)
	}
	for _, path := range []string{link, filepath.Join(link, "weights")} {
		if got, err := existingDirectory(path); err == nil {
			t.Fatalf("dangling model symlink measured parent volume %q", got)
		}
	}
}

func TestProductionProbeImportsCannotDownloadOrStartProcesses(t *testing.T) {
	// Includes every OS file, so a Windows run guards Darwin/Linux too.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(name, "net") || name == "os/exec" || strings.HasPrefix(name, "github.com/yetone/magpie/internal/") {
				t.Errorf("detector must not depend on networking, process or product lifecycle owners: %s imports %s", path, name)
			}
		}
	}
}
