package routeracceptance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
)

func equalJSON(a, b any) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return string(first) == string(second)
}

type storedFile struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func storageInventory(layout dirs.Layout) ([]storedFile, error) {
	result := []storedFile{}
	for _, root := range []string{layout.Models, layout.Runtime} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if len(result) >= 10000 {
				return errors.New("native storage inventory exceeds bound")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			result = append(result, storedFile{Path: path, Size: info.Size(), Modified: info.ModTime()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func validateDependencyPins(rt routermanifest.Runtime) error {
	if rt.Platform != "windows" {
		return nil
	}
	d := rt.Dependencies
	if d == nil {
		return errors.New("fixed Windows runtime has no dependency pins")
	}
	if err := d.Artifact.Validate(); err != nil {
		return err
	}
	if d.ID == "" || d.Version == "" || d.Architecture != "amd64" || d.LicenseURL == "" || len(d.Files) != 6 {
		return errors.New("incomplete fixed Windows dependency metadata")
	}
	names := map[string]bool{}
	for _, f := range d.Files {
		hash, err := hex.DecodeString(f.SHA256)
		if err != nil || len(hash) != 32 || f.Size <= 0 || f.Size > 20<<20 || f.Version == "" || f.Source == "" || filepath.Base(f.Name) != f.Name || names[strings.ToLower(f.Name)] {
			return fmt.Errorf("invalid pinned dependency %s", f.Name)
		}
		names[strings.ToLower(f.Name)] = true
	}
	for _, name := range []string{"msvcp140.dll", "vcruntime140.dll", "vcruntime140_1.dll", "msvcp140_1.dll", "msvcp140_atomic_wait.dll", "concrt140.dll"} {
		if !names[name] {
			return fmt.Errorf("fixed dependency set missing %s", name)
		}
	}
	return nil
}

type dependencyState struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	Error    string `json:"error"`
	Recovery string `json:"recovery"`
}

func dependencyStatus(m *routerruntime.Manager) dependencyState {
	data, _ := json.Marshal(m.Status())
	var snapshot struct {
		Dependencies dependencyState `json:"dependencies"`
	}
	_ = json.Unmarshal(data, &snapshot)
	return snapshot.Dependencies
}

func validateInstalledDependencies(ctx context.Context, m *routerruntime.Manager, spec routerprocess.Spec, rt routermanifest.Runtime) error {
	status := dependencyStatus(m)
	if rt.Dependencies == nil {
		return check(status.State == "not_required", "native dependency status did not report not_required")
	}
	if status.State != "ready" || status.Error != "" || status.Version != rt.Dependencies.Version {
		return fmt.Errorf("installed dependency status is not ready for pinned version: %+v", status)
	}
	if spec.RuntimeDependencies == nil {
		return errors.New("product installer omitted process dependency specification")
	}
	if spec.RuntimeDependencies.ID != rt.Dependencies.ID || spec.RuntimeDependencies.Version != rt.Dependencies.Version || spec.RuntimeDependencies.Architecture != rt.Dependencies.Architecture || len(spec.RuntimeDependencies.Files) != len(rt.Dependencies.Files) {
		return errors.New("process dependencies differ from fixed runtime catalog")
	}
	pinned := map[string]routerprocess.DependencyFile{}
	for _, f := range rt.Dependencies.Files {
		pinned[strings.ToLower(f.Name)] = routerprocess.DependencyFile{Name: f.Name, SHA256: f.SHA256, Version: f.Version, Size: f.Size}
	}
	for _, f := range spec.RuntimeDependencies.Files {
		expected, ok := pinned[strings.ToLower(f.Name)]
		actual := f
		actual.Path = ""
		if !ok || !reflect.DeepEqual(expected, actual) {
			return fmt.Errorf("process dependency metadata was not pinned: %s", f.Name)
		}
	}
	return routerprocess.ValidateDependencies(ctx, spec.Executable, spec.RuntimeDependencies)
}

func waitInstalled(ctx context.Context, m *routerruntime.Manager, budget time.Duration) error {
	call, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s := m.Status()
		if s.Installation == "installed" {
			return nil
		}
		if s.Installation != "downloading" {
			return fmt.Errorf("product installation ended in %s: %s", s.Installation, s.Error)
		}
		select {
		case <-call.Done():
			return call.Err()
		case <-ticker.C:
		}
	}
}

// Keep the source tree independent of the accepting process's environment.
// The actual child module paths are checked separately on native Windows.
func noExplicitEnvironment(spec routerprocess.Spec) error {
	return check(len(spec.Env) == 0, "product process spec contains caller-provided environment overrides")
}

func requireAbsent(path string) error {
	_, err := os.Lstat(path)
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unexpected pre-existing evidence file: %s", path)
	}
	return nil
}
