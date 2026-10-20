package routerruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/testenv"
)

type windowsDependencyFixture struct {
	installer *FileInstaller
	model     routermanifest.Model
	runtime   routermanifest.Runtime
	mu        sync.Mutex
	contents  map[string][]byte
	calls     map[string]int
	ranges    []string
	mode      string
	prefix    chan windowsDependencyPrefix
}

type windowsDependencyPrefix struct {
	written int
	err     error
}

type dependencyStartupInstaller struct {
	Installer
	verifies atomic.Int32
	installs atomic.Int32
}

func (i *dependencyStartupInstaller) Verify(ctx context.Context, model routermanifest.Model, runtime routermanifest.Runtime) (routerprocess.Spec, error) {
	i.verifies.Add(1)
	return i.Installer.Verify(ctx, model, runtime)
}

func (i *dependencyStartupInstaller) Install(ctx context.Context, model routermanifest.Model, runtime routermanifest.Runtime, progress func(Progress)) (routerprocess.Spec, error) {
	i.installs.Add(1)
	return i.Installer.Install(ctx, model, runtime, progress)
}

func TestWindowsDependenciesUnselectedStartupReportsRequiredWithoutSideEffects(t *testing.T) {
	f := newWindowsDependencyFixture(t, nil)
	installer := &dependencyStartupInstaller{Installer: f.installer}
	factory := &fixtureFactory{}
	m, err := New(context.Background(), Options{Catalog: routermanifest.Catalog{Models: []routermanifest.Model{f.model}, Runtimes: []routermanifest.Runtime{f.runtime}}, RuntimeID: f.runtime.ID,
		Store: &memoryIntent{}, Installer: installer, Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	s := m.Status()
	want := DependencyStatus{State: "unknown", ID: f.runtime.Dependencies.ID, Version: f.runtime.Dependencies.Version, Recovery: "install_or_repair"}
	if s.Dependencies != want || s.Intent != (Intent{}) || s.Installation != "not_installed" || s.Runtime != Unloaded || s.PID != 0 || s.Error != "" {
		t.Fatalf("unselected startup hid the runtime's required dependencies: %+v", s)
	}
	if installer.verifies.Load() != 0 || installer.installs.Load() != 0 || factory.count.Load() != 0 || f.totalCalls() != 0 {
		t.Fatalf("unselected startup performed work: verify=%d install=%d start=%d HTTP=%d", installer.verifies.Load(), installer.installs.Load(), factory.count.Load(), f.totalCalls())
	}
}

func windowsDependencyZIP(t *testing.T, name string, data []byte, padding ...int) []byte {
	t.Helper()
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o700)
	entry, err := w.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	if len(padding) != 0 {
		header := &zip.FileHeader{Name: "fixture-padding.bin", Method: zip.Store}
		header.SetMode(0o600)
		entry, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(bytes.Repeat([]byte("p"), padding[0])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// These fixtures use a tiny inert PE version resource, never a system DLL,
// downloaded redistributable, model executable or external HTTP endpoint.
func newWindowsDependencyFixture(t *testing.T, dll []byte, padding ...int) *windowsDependencyFixture {
	t.Helper()
	if dll == nil {
		dll = testenv.PEDLLFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 50, 35719, 0})
	}
	const member = "Contents/Release/x64/msvcp140.dll"
	base := windowsDependencyZIP(t, "release/llama-server.exe", []byte("inert runtime executable fixture"))
	payload := windowsDependencyZIP(t, member, dll, padding...)
	f := &windowsDependencyFixture{contents: map[string][]byte{"/runtime": base, "/model": []byte("tiny fixed gguf fixture"), "/template": []byte("fixture template"), "/dependencies": payload}, calls: make(map[string]int), prefix: make(chan windowsDependencyPrefix, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.URL.Path]++
		mode := f.mode
		data := append([]byte(nil), f.contents[r.URL.Path]...)
		if r.URL.Path == "/dependencies" {
			f.ranges = append(f.ranges, r.Header.Get("Range"))
		}
		f.mu.Unlock()
		if data == nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/dependencies" && mode == "fail" {
			http.Error(w, "controlled dependency failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"dependency-fixture"`)
		if r.URL.Path == "/dependencies" && mode == "hang" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			n, err := w.Write(data[:len(data)/2])
			if err == nil {
				err = http.NewResponseController(w).Flush()
			}
			select {
			case f.prefix <- windowsDependencyPrefix{written: n, err: err}:
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
			return
		}
		if value := r.Header.Get("Range"); value != "" {
			start, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(value, "bytes="), "-"))
			if err != nil || start < 0 || start >= len(data) {
				http.Error(w, "invalid fixture range", 416)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(len(data)-start))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	f.model = routermanifest.Model{ID: "windows-dependency-model", Tier: "1.7B", Revision: "fixture", ContextTokens: 1024,
		Artifact: artifactFor("windows-dependency-model", "model.gguf", f.contents["/model"]), Template: artifactFor("windows-dependency-template", "template.jinja", f.contents["/template"])}
	f.model.Artifact.URL, f.model.Template.URL = server.URL+"/model", server.URL+"/template"
	f.runtime = routermanifest.Runtime{ID: "windows-dependency-runtime", Version: "b10828", Platform: "windows", Architecture: "amd64", Backend: "cpu", Executable: "llama-server.exe", Artifact: artifactFor("windows-dependency-b10828", "runtime.zip", base)}
	f.runtime.Artifact.URL, f.runtime.Artifact.Format, f.runtime.Artifact.ExpandedBytes = server.URL+"/runtime", "zip", 4096
	file := artifactFor("crt", "msvcp140.dll", dll)
	f.runtime.Dependencies = &routermanifest.Dependencies{ID: "fixture-msvc-x64", Version: "14.50.35719.0", Architecture: "amd64", Artifact: artifactFor("windows-dependency-payload", "runtime-dependencies.vsix", payload),
		Files: []routermanifest.DependencyFile{{Source: member, Name: file.FileName, Size: file.Size, SHA256: file.SHA256, Version: "14.50.35719.0"}}}
	f.runtime.Dependencies.Artifact.URL, f.runtime.Dependencies.Artifact.Format, f.runtime.Dependencies.Artifact.ExpandedBytes = server.URL+"/dependencies", "zip", 8192
	if len(padding) != 0 {
		f.runtime.Dependencies.Artifact.ExpandedBytes += int64(padding[0])
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.installer = &FileInstaller{Layout: dirs.Layout{Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime")}, Downloader: &routerdownload.Downloader{Client: server.Client()}}
	return f
}

func (f *windowsDependencyFixture) setMode(mode string) { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }
func (f *windowsDependencyFixture) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}
func (f *windowsDependencyFixture) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, calls := range f.calls {
		n += calls
	}
	return n
}

func (f *windowsDependencyFixture) legacyInstall(t *testing.T) {
	t.Helper()
	r := f.runtime
	r.Dependencies = nil
	if _, err := f.installer.Install(context.Background(), f.model, r, nil); err != nil {
		t.Fatal(err)
	}
	assertWindowsDependencyManifest(t, f.installer.Layout.ManifestFile(), 1)
}

func assertWindowsDependencyManifest(t *testing.T, file string, schema int) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &got); err != nil || got.SchemaVersion != schema {
		t.Fatalf("manifest schema=%d, want %d: %v", got.SchemaVersion, schema, err)
	}
}

func assertWindowsDependencyError(t *testing.T, err error, code string) {
	t.Helper()
	var dependency *routerprocess.DependencyError
	if !errors.As(err, &dependency) || dependency.Code != code {
		t.Fatalf("dependency error=%v, want %s", err, code)
	}
}

func (f *windowsDependencyFixture) manager(t *testing.T) (*Manager, *fixtureFactory) {
	t.Helper()
	factory := &fixtureFactory{}
	m, err := New(context.Background(), Options{Catalog: routermanifest.Catalog{Models: []routermanifest.Model{f.model}, Runtimes: []routermanifest.Runtime{f.runtime}}, RuntimeID: f.runtime.ID,
		Store: &memoryIntent{i: Intent{Selected: f.model.ID, Enabled: true}}, Installer: f.installer, Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m, factory
}

func (f *windowsDependencyFixture) assertLegacyCache(t *testing.T) {
	t.Helper()
	model, template, archive, base := f.installer.paths(f.model, f.runtime)
	for _, item := range []struct {
		artifact routermanifest.Artifact
		path     string
	}{{f.model.Artifact, model}, {f.model.Template, template}, {f.runtime.Artifact, archive}} {
		if err := routerdownload.Verify(context.Background(), item.artifact, item.path); err != nil {
			t.Fatalf("lost verified cache %s: %v", item.artifact.ID, err)
		}
	}
	if err := routerdownload.VerifyDirectory(context.Background(), f.runtime.Artifact, base); err != nil {
		t.Fatalf("lost original v1 runtime: %v", err)
	}
}

func TestWindowsDependenciesExplicitMigrationAndOfflineRepair(t *testing.T) {
	f := newWindowsDependencyFixture(t, nil)
	f.legacyInstall(t)
	before := f.totalCalls()
	m, factory := f.manager(t)
	s := m.Status()
	if s.Installation != "not_installed" || s.Runtime != Unloaded || s.Dependencies.State != "missing" || s.Dependencies.Error != routerprocess.DependencyMissingCode || s.Dependencies.Recovery != "install_or_repair" || factory.count.Load() != 0 || f.totalCalls() != before {
		t.Fatalf("startup installed or launched legacy runtime: %+v", s)
	}
	err := m.Use(context.Background(), Execution, noWork)
	assertWindowsDependencyError(t, err, routerprocess.DependencyMissingCode)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing dependency lost not-installed cause: %v", err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Installation != "downloading" })
	s = m.Status()
	if s.Installation != "installed" || s.Dependencies.State != "ready" || s.Dependencies.Error != "" || factory.count.Load() != 0 {
		t.Fatalf("explicit migration: %+v", s)
	}
	if f.count("/dependencies") != 1 || f.count("/model") != 1 || f.count("/template") != 1 || f.count("/runtime") != 1 {
		t.Fatal("migration downloaded existing verified weights or runtime again")
	}
	assertWindowsDependencyManifest(t, f.installer.Layout.ManifestFile(), 2)
	f.assertLegacyCache(t)
	for purpose, capability := range s.Capabilities {
		if capability.State != "unknown" || capability.Ready {
			t.Fatalf("installation passed %s capability", purpose)
		}
	}
	err = m.Invoke(context.Background(), Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}]}`), func(*http.Response) error { return nil })
	if !errors.Is(err, ErrCapability) || factory.count.Load() != 0 {
		t.Fatalf("dependency installation bypassed capability gate: %v", err)
	}
	spec, err := f.installer.Verify(context.Background(), f.model, f.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if spec.RuntimeDependencies == nil || filepath.Dir(spec.RuntimeDependencies.Files[0].Path) != filepath.Dir(spec.Executable) {
		t.Fatal("verified spec did not bind app-local DLL")
	}
	before = f.totalCalls()
	if _, err := f.installer.Install(context.Background(), f.model, f.runtime, nil); err != nil {
		t.Fatal(err)
	}
	if f.totalCalls() != before {
		t.Fatal("repeat installation reached network")
	}
	for _, target := range []string{spec.RuntimeDependencies.Files[0].Path, filepath.Join(f.installer.Layout.Runtime, f.runtime.Dependencies.ID, "msvcp140.dll")} {
		if err := os.WriteFile(target, []byte("damaged dependency"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := f.installer.Verify(context.Background(), f.model, f.runtime)
		assertWindowsDependencyError(t, err, routerprocess.DependencyCorruptCode)
		if _, err := f.installer.Install(context.Background(), f.model, f.runtime, nil); err != nil {
			t.Fatalf("offline repair: %v", err)
		}
		if f.totalCalls() != before {
			t.Fatal("repair redownloaded verified archives or weights")
		}
		f.assertLegacyCache(t)
	}
	_, source, execution := f.installer.dependencyPaths(f.runtime)
	for _, directory := range []string{source, execution} {
		matches, err := filepath.Glob(directory + ".invalid-*")
		if err != nil || len(matches) == 0 {
			t.Fatalf("repair did not retain damaged tree %s: %v", directory, err)
		}
	}
}

func TestWindowsDependenciesManagerKeepsExactFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name, code, state string
		dll               []byte
		mode              string
	}{
		{"architecture", routerprocess.DependencyArchitectureCode, "architecture", testenv.PEDLLFixture(t, pe.IMAGE_FILE_MACHINE_I386, [4]uint16{14, 50, 35719, 0}), ""},
		{"version", routerprocess.DependencyVersionCode, "version", testenv.PEDLLFixture(t, pe.IMAGE_FILE_MACHINE_AMD64, [4]uint16{14, 32, 31326, 0}), ""},
		{"download", "local_model_dependency_download_failed", "failed", nil, "fail"},
		{"corrupt", routerprocess.DependencyCorruptCode, "corrupt", nil, "corrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWindowsDependencyFixture(t, tc.dll)
			f.legacyInstall(t)
			if tc.mode == "corrupt" {
				f.mu.Lock()
				f.contents["/dependencies"][0] ^= 0xff
				f.mu.Unlock()
			} else {
				f.setMode(tc.mode)
			}
			m, factory := f.manager(t)
			if err := m.Install(); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return m.Status().Installation != "downloading" })
			s := m.Status()
			if s.Installation != "failed" || s.Error != tc.code || s.Dependencies.Error != tc.code || s.Dependencies.State != tc.state || factory.count.Load() != 0 {
				t.Fatalf("dependency failure was generalized: %+v", s)
			}
			if err := m.Use(context.Background(), Execution, noWork); err == nil {
				t.Fatal("failed dependency runtime loaded")
			} else {
				assertWindowsDependencyError(t, err, tc.code)
			}
			f.assertLegacyCache(t)
			assertWindowsDependencyManifest(t, f.installer.Layout.ManifestFile(), 1)
		})
	}
}

func TestWindowsDependenciesCancelPreservesProgressAndVerifiedWeights(t *testing.T) {
	// Keep the DLL itself tiny, while allowing a substantial prefix to reach
	// the downloader before the fixture deliberately holds the response open.
	// The unselected STORE member is never extracted into the dependency tree.
	f := newWindowsDependencyFixture(t, nil, 256<<10)
	f.legacyInstall(t)
	f.setMode("hang")
	m, factory := f.manager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	diagnostics := func() string {
		archive, _, _ := f.installer.dependencyPaths(f.runtime)
		inspect := func(name string) string {
			info, statErr := os.Stat(name)
			var size int64
			if statErr == nil {
				size = info.Size()
			}
			file, openErr := os.Open(name)
			if openErr != nil {
				return fmt.Sprintf("statErr=%v size=%d openErr=%v", statErr, size, openErr)
			}
			data, readErr := io.ReadAll(io.LimitReader(file, 8193))
			readErr = errors.Join(readErr, file.Close())
			return fmt.Sprintf("statErr=%v size=%d readBytes=%d readErr=%v", statErr, size, len(data), readErr)
		}
		return fmt.Sprintf("status=%+v dependencyHTTP=%d part={%s} meta={%s}", m.Status(), f.count("/dependencies"), inspect(archive+".part"), inspect(archive+".part.json"))
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	prefixWritten := false
	for {
		s := m.Status()
		if prefixWritten && s.Progress.Artifact == f.runtime.Dependencies.Artifact.ID && s.Progress.Downloaded > 0 {
			break
		}
		if s.Installation != "downloading" {
			t.Fatalf("dependency install ended before receiving a resumable prefix: %s", diagnostics())
		}
		select {
		case prefix := <-f.prefix:
			if prefix.err != nil || prefix.written != int(f.runtime.Dependencies.Artifact.Size)/2 {
				t.Fatalf("fixture prefix write failed: written=%d err=%v; %s", prefix.written, prefix.err, diagnostics())
			}
			prefixWritten = true
		case <-timeout.C:
			t.Fatalf("dependency prefix progress did not arrive (serverPrefixWritten=%t): %s", prefixWritten, diagnostics())
		case <-poll.C:
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.CancelInstall(ctx); err != nil {
		t.Fatal(err)
	}
	s := m.Status()
	if s.Installation != "canceled" || s.Dependencies.State != "canceled" || s.Error != "" || factory.count.Load() != 0 {
		t.Fatalf("cancel state: %+v", s)
	}
	archive, _, _ := f.installer.dependencyPaths(f.runtime)
	partial, err := os.ReadFile(archive + ".part")
	if err != nil || len(partial) == 0 || len(partial) >= int(f.runtime.Dependencies.Artifact.Size) {
		t.Fatalf("lost useful partial: bytes=%d err=%v", len(partial), err)
	}
	f.mu.Lock()
	wantPrefix := append([]byte(nil), f.contents["/dependencies"][:len(partial)]...)
	f.mu.Unlock()
	if !bytes.Equal(partial, wantPrefix) {
		t.Fatal("retained partial differs from the exact fixture prefix")
	}
	if _, err := os.Stat(archive + ".part.json"); err != nil {
		t.Fatal("lost resume metadata", err)
	}
	f.assertLegacyCache(t)
	assertWindowsDependencyManifest(t, f.installer.Layout.ManifestFile(), 1)
	f.setMode("")
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status().Installation != "downloading" })
	if s := m.Status(); s.Installation != "installed" || s.Dependencies.State != "ready" {
		t.Fatalf("resume failed: %+v", s)
	}
	f.mu.Lock()
	ranges := append([]string(nil), f.ranges...)
	f.mu.Unlock()
	if len(ranges) != 2 || ranges[1] != fmt.Sprintf("bytes=%d-", len(partial)) {
		t.Fatalf("did not resume retained prefix: %v", ranges)
	}
	if f.count("/model") != 1 || f.count("/runtime") != 1 || f.count("/template") != 1 {
		t.Fatal("cancel/resume downloaded existing weights/runtime")
	}
	f.assertLegacyCache(t)
}

func TestWindowsDependenciesColdLoadRechecksMissingAndCorruptFiles(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			f := newWindowsDependencyFixture(t, nil)
			spec, err := f.installer.Install(context.Background(), f.model, f.runtime, nil)
			if err != nil {
				t.Fatal(err)
			}
			m, factory := f.manager(t)
			target := spec.RuntimeDependencies.Files[0].Path
			code := routerprocess.DependencyCorruptCode
			if missing {
				code = routerprocess.DependencyMissingCode
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("damaged after startup verification"), 0o600); err != nil {
				t.Fatal(err)
			}
			err = m.Use(context.Background(), Execution, noWork)
			assertWindowsDependencyError(t, err, code)
			if !errors.Is(err, ErrFault) {
				t.Fatalf("cold verification lost runtime fault: %v", err)
			}
			s := m.Status()
			if s.Runtime != Fault || s.Installation != "not_installed" || s.Error != code || s.Dependencies.Error != code || factory.count.Load() != 0 {
				t.Fatalf("cold load launched or generalized dependency failure: %+v", s)
			}
		})
	}
}

func TestWindowsDependenciesSourceReceiptCannotAddDLL(t *testing.T) {
	f := newWindowsDependencyFixture(t, nil)
	if _, err := f.installer.Install(context.Background(), f.model, f.runtime, nil); err != nil {
		t.Fatal(err)
	}
	_, source, _ := f.installer.dependencyPaths(f.runtime)
	name := filepath.Join(source, ".router-verified.json")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Version int    `json:"version"`
		SHA256  string `json:"artifactSHA256"`
		Size    int64  `json:"artifactSize"`
		Files   []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	extra := []byte("untrusted backend DLL")
	if err := os.WriteFile(filepath.Join(source, "ggml-cpu-untrusted.dll"), extra, 0o600); err != nil {
		t.Fatal(err)
	}
	pin := artifactFor("extra", "ggml-cpu-untrusted.dll", extra)
	record.Files = append(record.Files, struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}{pin.FileName, pin.Size, pin.SHA256})
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := routerdownload.VerifyDirectory(context.Background(), f.runtime.Dependencies.Artifact, source); err != nil {
		t.Fatal("fixture must pass receipt-only verification", err)
	}
	err = verifyDependencyFiles(context.Background(), f.runtime.Dependencies, source)
	assertWindowsDependencyError(t, err, routerprocess.DependencyCorruptCode)
	if _, err := f.installer.Verify(context.Background(), f.model, f.runtime); err == nil {
		t.Fatal("receipt added a dependency outside the compiled allow-list")
	}
}
