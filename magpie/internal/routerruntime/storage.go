package routerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/steady"
)

func artifactHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type ConfigIntentStore struct{ Store *routerconfig.Store }

func (s ConfigIntentStore) Load(ctx context.Context) (Intent, error) {
	d, err := s.Store.LoadContext(ctx)
	if err != nil {
		return Intent{}, err
	}
	var i Intent
	if err = json.Unmarshal(d["runtime"], &i); err != nil {
		return i, err
	}
	return i, i.Settings.Validate()
}
func (s ConfigIntentStore) Save(ctx context.Context, i Intent) error {
	if err := i.Settings.Validate(); err != nil {
		return err
	}
	return s.Store.UpdateContext(ctx, func(d routerconfig.Document) error {
		var r map[string]json.RawMessage
		if err := json.Unmarshal(d["runtime"], &r); err != nil || r == nil {
			return errors.New("invalid runtime configuration")
		}
		r["enabled"], _ = json.Marshal(i.Enabled)
		r["selected"], _ = json.Marshal(i.Selected)
		if i.Settings.IsZero() {
			delete(r, "settings")
		} else {
			var settings map[string]json.RawMessage
			if raw, exists := r["settings"]; exists && string(raw) != "null" {
				if err := json.Unmarshal(raw, &settings); err != nil {
					return err
				}
			}
			if settings == nil {
				settings = make(map[string]json.RawMessage)
			}
			knownJSON, _ := json.Marshal(i.Settings)
			var known map[string]json.RawMessage
			_ = json.Unmarshal(knownJSON, &known)
			for key, value := range known {
				settings[key] = value
			}
			r["settings"], _ = json.Marshal(settings)
		}
		if !i.Trial.Judgment && !i.Trial.Execution {
			delete(r, "trial")
		} else {
			r["trial"], _ = json.Marshal(i.Trial)
		}
		var err error
		d["runtime"], err = json.Marshal(r)
		return err
	})
}

type FileInstaller struct {
	Layout     dirs.Layout
	Downloader *routerdownload.Downloader
}

func (f *FileInstaller) paths(m routermanifest.Model, r routermanifest.Runtime) (model, template, archive, folder string) {
	model = filepath.Join(f.Layout.Models, m.Artifact.ID, m.Artifact.FileName)
	template = filepath.Join(f.Layout.Models, m.Artifact.ID, m.Template.FileName)
	archive = filepath.Join(f.Layout.Runtime, r.Artifact.ID+"-archive", r.Artifact.FileName)
	folder = filepath.Join(f.Layout.Runtime, r.Artifact.ID)
	return
}
func (f *FileInstaller) Verify(ctx context.Context, m routermanifest.Model, r routermanifest.Runtime) (routerprocess.Spec, error) {
	model, template, _, folder := f.paths(m, r)
	var dependencies *routerprocess.DependencySpec
	if r.Dependencies != nil {
		var err error
		folder, dependencies, err = f.verifyWindowsRuntime(ctx, r)
		if err != nil {
			return routerprocess.Spec{}, err
		}
	}
	for _, v := range []struct {
		a routermanifest.Artifact
		p string
	}{{m.Artifact, model}, {m.Template, template}} {
		if err := routerdownload.Verify(ctx, v.a, v.p); err != nil {
			return routerprocess.Spec{}, err
		}
	}
	if r.Dependencies == nil {
		if err := routerdownload.VerifyDirectory(ctx, r.Artifact, folder); err != nil {
			return routerprocess.Spec{}, err
		}
	}
	// Upstream archives may contain a single release directory. Find exactly
	// one regular executable within the already verified extraction.
	var executable string
	err := filepath.WalkDir(folder, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == r.Executable && !entry.IsDir() {
			if executable != "" {
				return errors.New("ambiguous runtime executable")
			}
			executable = path
		}
		return ctx.Err()
	})
	if err != nil || executable == "" {
		return routerprocess.Spec{}, errors.New("verified runtime executable missing")
	}
	return routerprocess.Spec{Executable: executable, ModelPath: model, ContextSize: m.ContextTokens, Parallel: 1, Backend: r.Backend, ExtraArgs: []string{"--chat-template-file", template}, RuntimeDependencies: dependencies}, nil
}
func (f *FileInstaller) Install(ctx context.Context, m routermanifest.Model, r routermanifest.Runtime, progress func(Progress)) (routerprocess.Spec, error) {
	if err := m.Artifact.Validate(); err != nil {
		return routerprocess.Spec{}, err
	}
	if err := m.Template.Validate(); err != nil {
		return routerprocess.Spec{}, err
	}
	if err := r.Artifact.Validate(); err != nil {
		return routerprocess.Spec{}, err
	}
	model, template, archive, folder := f.paths(m, r)
	downloader := f.Downloader
	if downloader == nil {
		downloader = &routerdownload.Downloader{}
	}
	totalSteps := 3
	stepOffset := 0
	if r.Dependencies != nil {
		totalSteps = 4
		stepOffset = 1
		if err := f.installWindowsDependencies(ctx, r, downloader, progress); err != nil {
			return routerprocess.Spec{}, err
		}
	}
	for _, v := range []struct {
		a     routermanifest.Artifact
		path  string
		stage string
		step  int
	}{
		{r.Artifact, archive, "runtime", stepOffset + 1},
		{m.Artifact, model, "model", stepOffset + 2},
		{m.Template, template, "template", stepOffset + 3},
	} {
		host := artifactHost(v.a.URL)
		reported := false
		if err := downloader.Download(ctx, v.a, v.path, func(p routerdownload.Progress) {
			reported = true
			if progress != nil {
				progress(Progress{
					Artifact:   v.a.ID,
					Downloaded: p.Downloaded,
					Total:      p.Total,
					Stage:      v.stage,
					Step:       v.step,
					TotalSteps: totalSteps,
					Host:       host,
				})
			}
		}); err != nil {
			if !reported && progress != nil && !errors.Is(err, context.Canceled) {
				progress(Progress{
					Artifact:   v.a.ID,
					Downloaded: 0,
					Total:      v.a.Size,
					Stage:      v.stage,
					Step:       v.step,
					TotalSteps: totalSteps,
					Host:       host,
				})
			}
			return routerprocess.Spec{}, err
		}
	}
	reportVerifyFailure := func(err error) {
		if progress != nil && dependencyCode(err) == "" && !errors.Is(err, context.Canceled) {
			progress(Progress{
				Artifact:   r.Artifact.ID,
				Downloaded: r.Artifact.Size,
				Total:      r.Artifact.Size,
				Stage:      "verify",
				Step:       totalSteps,
				TotalSteps: totalSteps,
			})
		}
	}
	if err := routerdownload.VerifyDirectory(ctx, r.Artifact, folder); err != nil {
		if ctx.Err() != nil {
			return routerprocess.Spec{}, ctx.Err()
		}
		if info, statErr := os.Lstat(folder); statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				err := errors.New("runtime directory is not a regular directory")
				reportVerifyFailure(err)
				return routerprocess.Spec{}, err
			}
			// Install runs only with no process/loading users. Preserve damaged
			// files for diagnosis, and let Extract publish a fresh complete tree.
			if err := os.Rename(folder, folder+".invalid-"+time.Now().UTC().Format("20060102T150405.000000000")); err != nil {
				reportVerifyFailure(err)
				return routerprocess.Spec{}, err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			reportVerifyFailure(statErr)
			return routerprocess.Spec{}, statErr
		}
	}
	if err := routerdownload.Extract(ctx, r.Artifact, archive, folder); err != nil {
		reportVerifyFailure(err)
		return routerprocess.Spec{}, err
	}
	if r.Dependencies != nil {
		if err := f.assembleWindowsRuntime(ctx, r); err != nil {
			reportVerifyFailure(err)
			return routerprocess.Spec{}, err
		}
	}
	spec, err := f.Verify(ctx, m, r)
	if err != nil {
		reportVerifyFailure(err)
		return spec, err
	}
	schema := 1
	if r.Dependencies != nil {
		schema = 2
	}
	record, err := json.Marshal(struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Model         routermanifest.Model   `json:"model"`
		Runtime       routermanifest.Runtime `json:"runtime"`
	}{schema, m, r})
	if err != nil {
		reportVerifyFailure(err)
		return spec, err
	}
	if err := steady.WriteFileContext(ctx, f.Layout.ManifestFile(), record, 0600); err != nil {
		reportVerifyFailure(err)
		return routerprocess.Spec{}, err
	}
	return spec, nil
}

// PlatformRuntime consumes the catalog; it makes no independent hardware rule.
func PlatformRuntime(c routermanifest.Catalog, goos, arch string) string {
	for _, r := range c.Runtimes {
		if r.Platform == goos && r.Architecture == arch && (r.Backend == "cpu" || strings.EqualFold(r.Backend, "metal")) {
			return r.ID
		}
	}
	return ""
}
