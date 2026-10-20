package routerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
)

func dependencyFailure(code string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var dependency *routerprocess.DependencyError
	if errors.As(err, &dependency) {
		return err
	}
	return &routerprocess.DependencyError{Code: code, Err: err}
}

func (f *FileInstaller) dependencyPaths(r routermanifest.Runtime) (archive, source, execution string) {
	d := r.Dependencies
	archive = filepath.Join(f.Layout.Runtime, d.Artifact.ID+"-archive", d.Artifact.FileName)
	source = filepath.Join(f.Layout.Runtime, d.ID)
	execution = filepath.Join(f.Layout.Runtime, r.Artifact.ID+"-"+d.ID)
	return
}

func dependencySpec(d *routermanifest.Dependencies, directory string) *routerprocess.DependencySpec {
	s := &routerprocess.DependencySpec{ID: d.ID, Version: d.Version, Architecture: d.Architecture}
	for _, file := range d.Files {
		s.Files = append(s.Files, routerprocess.DependencyFile{Path: filepath.Join(directory, file.Name), Name: file.Name, Size: file.Size, SHA256: file.SHA256, Version: file.Version})
	}
	return s
}

func dependencyFileArtifact(d *routermanifest.Dependencies, file routermanifest.DependencyFile) routermanifest.Artifact {
	return routermanifest.Artifact{ID: d.ID + "-" + file.Name, URL: d.Artifact.URL, FileName: file.Name, Size: file.Size, SHA256: file.SHA256, Format: "file"}
}

func verifyDependencyFiles(ctx context.Context, d *routermanifest.Dependencies, directory string) error {
	if err := d.Validate(); err != nil {
		return dependencyFailure(routerprocess.DependencyLayoutCode, err)
	}
	for _, file := range d.Files {
		if err := routerdownload.Verify(ctx, dependencyFileArtifact(d, file), filepath.Join(directory, file.Name)); err != nil {
			code := routerprocess.DependencyCorruptCode
			if errors.Is(err, os.ErrNotExist) {
				code = routerprocess.DependencyMissingCode
			}
			return dependencyFailure(code, err)
		}
	}
	var files []routerdownload.SelectedFile
	for _, file := range d.Files {
		files = append(files, routerdownload.SelectedFile{Source: file.Source, Name: file.Name, Size: file.Size, SHA256: file.SHA256})
	}
	return dependencyFailure(routerprocess.DependencyCorruptCode, routerdownload.VerifySelectedDirectory(ctx, d.Artifact, directory, files))
}

func (f *FileInstaller) assembly(r routermanifest.Runtime) (routerdownload.Assembly, string, error) {
	_, source, execution := f.dependencyPaths(r)
	base := filepath.Join(f.Layout.Runtime, r.Artifact.ID)
	executable, err := findRuntimeExecutable(base, r.Executable)
	if err != nil {
		return routerdownload.Assembly{}, execution, err
	}
	rel, err := filepath.Rel(base, filepath.Dir(executable))
	if err != nil {
		return routerdownload.Assembly{}, execution, err
	}
	if rel == "." {
		rel = ""
	}
	return routerdownload.Assembly{Base: r.Artifact, Overlay: r.Dependencies.Artifact, BaseDirectory: base, OverlayDirectory: source, OverlayPrefix: filepath.ToSlash(rel)}, execution, nil
}

func findRuntimeExecutable(folder, name string) (string, error) {
	var executable string
	err := filepath.WalkDir(folder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == name && !d.IsDir() {
			if executable != "" {
				return errors.New("ambiguous runtime executable")
			}
			executable = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if executable == "" {
		return "", os.ErrNotExist
	}
	return executable, nil
}

func (f *FileInstaller) verifyWindowsRuntime(ctx context.Context, r routermanifest.Runtime) (string, *routerprocess.DependencySpec, error) {
	_, source, execution := f.dependencyPaths(r)
	if err := verifyDependencyFiles(ctx, r.Dependencies, source); err != nil {
		return "", nil, err
	}
	a, _, err := f.assembly(r)
	if err == nil {
		err = a.Verify(ctx, execution)
	}
	if err != nil {
		code := routerprocess.DependencyCorruptCode
		if errors.Is(err, os.ErrNotExist) {
			code = routerprocess.DependencyMissingCode
		}
		return "", nil, dependencyFailure(code, err)
	}
	executable, err := findRuntimeExecutable(execution, r.Executable)
	if err != nil {
		return "", nil, dependencyFailure(routerprocess.DependencyLayoutCode, err)
	}
	spec := dependencySpec(r.Dependencies, filepath.Dir(executable))
	if err := routerprocess.ValidateDependencies(ctx, executable, spec); err != nil {
		return "", nil, err
	}
	return execution, spec, nil
}

func quarantine(ctx context.Context, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("repair target is not a regular directory")
	}
	return os.Rename(directory, directory+".invalid-"+time.Now().UTC().Format("20060102T150405.000000000"))
}

func (f *FileInstaller) installWindowsDependencies(ctx context.Context, r routermanifest.Runtime, downloader *routerdownload.Downloader, progress func(Progress)) error {
	d := r.Dependencies
	if err := d.Validate(); err != nil {
		return dependencyFailure(routerprocess.DependencyLayoutCode, err)
	}
	archive, source, _ := f.dependencyPaths(r)
	host := artifactHost(d.Artifact.URL)
	reported := false
	if err := downloader.Download(ctx, d.Artifact, archive, func(p routerdownload.Progress) {
		reported = true
		if progress != nil {
			progress(Progress{Artifact: d.Artifact.ID, Downloaded: p.Downloaded, Total: p.Total, Stage: "dependency", Step: 1, TotalSteps: 4, Host: host})
		}
	}); err != nil {
		if !reported && progress != nil && !errors.Is(err, context.Canceled) {
			progress(Progress{Artifact: d.Artifact.ID, Downloaded: 0, Total: d.Artifact.Size, Stage: "dependency", Step: 1, TotalSteps: 4, Host: host})
		}
		code := "local_model_dependency_download_failed"
		if errors.Is(err, routerdownload.ErrIntegrity) {
			code = routerprocess.DependencyCorruptCode
		}
		return dependencyFailure(code, err)
	}
	if err := routerdownload.VerifyDirectory(ctx, d.Artifact, source); err != nil {
		if err := quarantine(ctx, source); err != nil {
			return dependencyFailure(routerprocess.DependencyCorruptCode, err)
		}
	} else if err := verifyDependencyFiles(ctx, d, source); err != nil {
		if err := quarantine(ctx, source); err != nil {
			return err
		}
	}
	var files []routerdownload.SelectedFile
	for _, file := range d.Files {
		files = append(files, routerdownload.SelectedFile{Source: file.Source, Name: file.Name, Size: file.Size, SHA256: file.SHA256})
	}
	if err := routerdownload.ExtractSelected(ctx, d.Artifact, archive, source, files); err != nil {
		return dependencyFailure(routerprocess.DependencyCorruptCode, err)
	}
	return verifyDependencyFiles(ctx, d, source)
}

func (f *FileInstaller) assembleWindowsRuntime(ctx context.Context, r routermanifest.Runtime) error {
	a, execution, err := f.assembly(r)
	if err != nil {
		return dependencyFailure(routerprocess.DependencyLayoutCode, err)
	}
	if err := a.Verify(ctx, execution); err != nil {
		if err := quarantine(ctx, execution); err != nil {
			return dependencyFailure(routerprocess.DependencyCorruptCode, err)
		}
	}
	if err := a.Install(ctx, execution); err != nil {
		return dependencyFailure(routerprocess.DependencyCorruptCode, err)
	}
	return nil
}
