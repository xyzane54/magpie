package routerprocess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A minimal PE32+ DLL with an actual RT_VERSION tree and VS_FIXEDFILEINFO.
// It is parsed, never executed or loaded. No Windows installation is needed.
func dependencyPEFixture(t *testing.T, machine uint16, version [4]uint16) []byte {
	t.Helper()
	return testenv.PEDLLFixture(t, machine, version)
}

func dependencyFixture(t *testing.T) (string, *DependencySpec) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "llama-server.exe")
	if err := os.WriteFile(executable, []byte("fixture executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 50, 35719, 0})
	item := DependencyFile{Path: filepath.Join(dir, "msvcp140.dll"), Name: "msvcp140.dll", Version: "14.50.35719.0"}
	writeDependencyFixture(t, &item, data)
	return executable, &DependencySpec{ID: "vc-runtime-x64", Version: item.Version, Architecture: "amd64", Files: []DependencyFile{item}}
}

func writeDependencyFixture(t *testing.T, item *DependencyFile, data []byte) {
	t.Helper()
	if err := os.WriteFile(item.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	item.Size, item.SHA256 = int64(len(data)), hex.EncodeToString(hash[:])
}

func requireDependencyCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *DependencyError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("dependency error = %v, want %s", err, code)
	}
}

func TestDependenciesValidateFixedPEBytes(t *testing.T) {
	executable, spec := dependencyFixture(t)
	if err := ValidateDependencies(context.Background(), executable, spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateDependencies(ctx, executable, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification = %v", err)
	}
	if err := ValidateDependencies(context.Background(), executable, nil); err != nil {
		t.Fatalf("fake runtime nil contract = %v", err)
	}
}

func TestDependenciesFailClosed(t *testing.T) {
	cases := []struct {
		name string
		code string
		edit func(*testing.T, string, *DependencySpec)
	}{
		{"missing", DependencyMissingCode, func(t *testing.T, _ string, s *DependencySpec) { os.Remove(s.Files[0].Path) }},
		{"size", DependencyCorruptCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files[0].Size++ }},
		{"hash", DependencyCorruptCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files[0].SHA256 = strings.Repeat("0", 64) }},
		{"architecture-metadata", DependencyArchitectureCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Architecture = "386" }},
		{"architecture-image", DependencyArchitectureCode, func(t *testing.T, _ string, s *DependencySpec) {
			writeDependencyFixture(t, &s.Files[0], dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_I386, [4]uint16{14, 50, 35719, 0}))
		}},
		{"not-DLL", DependencyArchitectureCode, func(t *testing.T, _ string, s *DependencySpec) {
			data := dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 50, 35719, 0})
			binary.LittleEndian.PutUint16(data[0x84+18:], pe.IMAGE_FILE_EXECUTABLE_IMAGE)
			writeDependencyFixture(t, &s.Files[0], data)
		}},
		{"version-image", DependencyVersionCode, func(t *testing.T, _ string, s *DependencySpec) {
			writeDependencyFixture(t, &s.Files[0], dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 32, 31326, 0}))
		}},
		{"version-malformed", DependencyVersionCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files[0].Version = "14.50" }},
		{"version-mixed-set", DependencyVersionCode, func(t *testing.T, _ string, s *DependencySpec) {
			s.Files[0].Version = "14.32.31326.0"
			writeDependencyFixture(t, &s.Files[0], dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 32, 31326, 0}))
		}},
		{"version-tree-outside-image", DependencyVersionCode, func(t *testing.T, _ string, s *DependencySpec) {
			data := dependencyPEFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 50, 35719, 0})
			binary.LittleEndian.PutUint32(data[512+72:], 0xfffffff0)
			writeDependencyFixture(t, &s.Files[0], data)
		}},
		{"duplicate-name", DependencyLayoutCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files = append(s.Files, s.Files[0]) }},
		{"name-case-alias", DependencyLayoutCode, func(_ *testing.T, _ string, s *DependencySpec) {
			alias := s.Files[0]
			alias.Name = strings.ToUpper(alias.Name)
			s.Files = append(s.Files, alias)
		}},
		{"outside-executable-directory", DependencyLayoutCode, func(t *testing.T, _ string, s *DependencySpec) {
			data, err := os.ReadFile(s.Files[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			s.Files[0].Path = filepath.Join(t.TempDir(), s.Files[0].Name)
			writeDependencyFixture(t, &s.Files[0], data)
		}},
		{"parent-component", DependencyLayoutCode, func(_ *testing.T, executable string, s *DependencySpec) {
			s.Files[0].Path = filepath.Dir(executable) + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(filepath.Dir(executable)) + string(filepath.Separator) + s.Files[0].Name
		}},
		{"invalid-hash-metadata", DependencyLayoutCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files[0].SHA256 = strings.Repeat("z", 64) }},
		{"unbounded-file-metadata", DependencyLayoutCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files[0].Size = maxDependencyFileBytes + 1 }},
		{"empty-contract", DependencyLayoutCode, func(_ *testing.T, _ string, s *DependencySpec) { s.Files = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executable, spec := dependencyFixture(t)
			tc.edit(t, executable, spec)
			requireDependencyCode(t, ValidateDependencies(context.Background(), executable, spec), tc.code)
		})
	}
}

func TestDependenciesRejectHardLinksAndAncestorLinks(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		executable, spec := dependencyFixture(t)
		if err := os.Link(spec.Files[0].Path, filepath.Join(filepath.Dir(executable), "other.dll")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		requireDependencyCode(t, ValidateDependencies(context.Background(), executable, spec), DependencyLayoutCode)
	})
	t.Run("ancestor-symlink", func(t *testing.T) {
		executable, spec := dependencyFixture(t)
		link := filepath.Join(t.TempDir(), "linked-runtime")
		if err := os.Symlink(filepath.Dir(executable), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		spec.Files[0].Path = filepath.Join(link, spec.Files[0].Name)
		requireDependencyCode(t, ValidateDependencies(context.Background(), filepath.Join(link, filepath.Base(executable)), spec), DependencyLayoutCode)
	})
}

func TestDependencyEnvironmentIsChildLocalAndIndependentOfDeveloperPATH(t *testing.T) {
	base := []string{"PATH=C:\\untrusted", "SystemRoot=C:\\untrusted", "windir=C:\\untrusted", "GGML_BACKEND_PATH=C:\\untrusted", "llama_arg_model=untrusted", "KEEP=present"}
	original := append([]string(nil), base...)
	env, err := dependencyEnvironment(base, []string{"EXPLICIT=present"}, `C:\Windows\System32`, `C:\Windows`)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{`PATH=C:\Windows\System32;C:\Windows`, `SystemRoot=C:\Windows`, "KEEP=present", "EXPLICIT=present"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s: %v", want, env)
		}
	}
	if strings.Contains(strings.ToLower(joined), "untrusted") || strings.Join(base, "\n") != strings.Join(original, "\n") {
		t.Fatalf("loader override survived or parent environment changed: %v", env)
	}
	for _, override := range []string{"Path=x", "SYSTEMROOT=x", "WINDIR=x", "ggml_backend_path=x", "LLAMA_ARG_HOST=0.0.0.0"} {
		_, err := dependencyEnvironment(base, []string{override}, `C:\Windows\System32`, `C:\Windows`)
		requireDependencyCode(t, err, DependencyLayoutCode)
	}
}

func TestNativeFactoryDependencyFailureHasNoProcessOrObserver(t *testing.T) {
	called := false
	factory := NativeFactory{OnStart: func(Process, Spec) { called = true }}
	spec := fixtureSpec(t)
	spec.RuntimeDependencies = &DependencySpec{ID: "empty", Version: "14.50.35719.0", Architecture: "amd64"}
	p, err := factory.Start(context.Background(), spec)
	requireDependencyCode(t, err, DependencyLayoutCode)
	if p != nil || called {
		t.Fatal("failed dependency verification started or observed a process")
	}
}

func TestNativeFactoryObserverGetsSameOwnedProcess(t *testing.T) {
	var observed Process
	spec := fixtureSpec(t)
	p, err := (NativeFactory{OnStart: func(p Process, got Spec) {
		observed = p
		if got.Executable != spec.Executable {
			t.Error("observer got a different spec")
		}
	}}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer p.Stop(ctx)
	if observed != p || observed.PID() != p.PID() {
		t.Fatal("observer did not receive the returned owned process")
	}
}

func TestDependenciesCannotLaunchOnOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows launch is covered by the native environment probe")
	}
	executable, deps := dependencyFixture(t)
	model := filepath.Join(filepath.Dir(executable), "model.gguf")
	if err := os.WriteFile(model, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := (NativeFactory{}).Start(context.Background(), Spec{Executable: executable, ModelPath: model, RuntimeDependencies: deps})
	requireDependencyCode(t, err, DependencyArchitectureCode)
	if p != nil {
		t.Fatal("Windows dependency spec launched on another OS")
	}
}

func TestRuntimeVersionOutputIsBounded(t *testing.T) {
	output := &dependencyVersionOutput{}
	data := bytes.Repeat([]byte("x"), 128<<10)
	if n, err := output.Write(data); n != len(data) || err != nil || len(output.data) != 64<<10 || !output.truncated {
		t.Fatalf("unbounded version output: n=%d err=%v len=%d truncated=%v", n, err, len(output.data), output.truncated)
	}
}
