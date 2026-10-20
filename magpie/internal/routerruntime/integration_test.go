package routerruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
)

func artifactFor(id, name string, data []byte) routermanifest.Artifact {
	h := sha256.Sum256(data)
	return routermanifest.Artifact{ID: id, FileName: name, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), Format: "file"}
}

func TestFileInstallerVerifiedStateSurvivesRestartAndRepairsCorruption(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("release/llama-server.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("fixture-executable"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	model := []byte("fixture-gguf")
	template := []byte("fixture-template")
	contents := map[string][]byte{"/runtime": archive.Bytes(), "/model": model, "/template": template}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if data, ok := contents[r.URL.Path]; ok {
			_, _ = w.Write(data)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := routermanifest.Model{ID: "fixture", Tier: "1.7B", Revision: "fixture-r1", ContextTokens: 1024, Artifact: artifactFor("fixture-model", "model.gguf", model), Template: artifactFor("fixture-template", "template.jinja", template)}
	m.Artifact.URL = server.URL + "/model"
	m.Template.URL = server.URL + "/template"
	r := routermanifest.Runtime{ID: "fixture-runtime", Executable: "llama-server.exe", Backend: "cpu", Artifact: artifactFor("fixture-runtime", "runtime.zip", archive.Bytes())}
	r.Artifact.URL = server.URL + "/runtime"
	r.Artifact.Format = "zip"
	r.Artifact.ExpandedBytes = 1024
	root := t.TempDir()
	layout := dirs.Layout{Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime")}
	f := &FileInstaller{Layout: layout, Downloader: &routerdownload.Downloader{}}
	if _, err := f.Verify(context.Background(), m, r); err == nil {
		t.Fatal("missing files installed")
	}
	spec, err := f.Install(context.Background(), m, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.ManifestFile()); err != nil {
		t.Fatal(err)
	}
	f = &FileInstaller{Layout: layout, Downloader: &routerdownload.Downloader{}}
	if _, err := f.Verify(context.Background(), m, r); err != nil {
		t.Fatal("restart lost verified installation", err)
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	if _, err := f.Install(context.Background(), m, r, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before {
		t.Fatal("duplicate download")
	}
	if err := os.WriteFile(spec.Executable, []byte("corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Verify(context.Background(), m, r); err == nil {
		t.Fatal("damaged runtime still installed")
	}
	if _, err := f.Install(context.Background(), m, r, nil); err != nil {
		t.Fatal("repair failed", err)
	}
	if err := os.WriteFile(spec.ModelPath, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Verify(context.Background(), m, r); err == nil {
		t.Fatal("damaged model still installed")
	}
	if _, err := f.Install(context.Background(), m, r, nil); err != nil {
		t.Fatal(err)
	}
}

type nativeInstaller struct{ spec routerprocess.Spec }

func (i nativeInstaller) Verify(context.Context, routermanifest.Model, routermanifest.Runtime) (routerprocess.Spec, error) {
	return i.spec, nil
}
func (i nativeInstaller) Install(context.Context, routermanifest.Model, routermanifest.Runtime, func(Progress)) (routerprocess.Spec, error) {
	return i.spec, nil
}

type trackedFactory struct {
	mu        sync.Mutex
	processes []routerprocess.Process
}

func (f *trackedFactory) Start(ctx context.Context, s routerprocess.Spec) (routerprocess.Process, error) {
	p, err := (routerprocess.NativeFactory{}).Start(ctx, s)
	if err == nil {
		f.mu.Lock()
		f.processes = append(f.processes, p)
		f.mu.Unlock()
	}
	return p, err
}

func TestNativeManagerFakeGenerationSSECancelCrashAndClose(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "fixture.gguf")
	if err := os.WriteFile(model, []byte("fixture-only"), 0600); err != nil {
		t.Fatal(err)
	}
	factory := &trackedFactory{}
	o := Options{Catalog: routermanifest.Fixed(), RuntimeID: "windows-amd64-cpu", Store: &memoryIntent{i: Intent{Enabled: true, Selected: "spark-x2.5-1.7B"}}, Installer: nativeInstaller{routerprocess.Spec{Executable: executable, ModelPath: model, Backend: "cpu", Env: []string{"API_ROUTER_RUNTIME_TEST_HELPER=1"}}}, Factory: factory}
	m, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	consume := func(r *http.Response) error {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("empty fixture response")
		}
		return err
	}
	for _, body := range []string{`{"messages":[{"role":"user","content":"hello"}]}`, `{"messages":[{"role":"user","content":"hello"}],"stream":true}`} {
		if err := m.Generate(context.Background(), Execution, []byte(body), consume); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Generate(context.Background(), Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}],"fake":{"mode":"error","status":500}}`), consume); err == nil {
		t.Fatal("ignored model error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = m.Generate(ctx, Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}],"fake":{"mode":"hang"}}`), consume)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background(), Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}],"fake":{"mode":"crash"}}`), consume); err == nil {
		t.Fatal("ignored runtime crash")
	}
	eventually(t, func() bool { return m.Status().Runtime == Fault })
	if err := m.Generate(context.Background(), Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}]}`), consume); err != nil {
		t.Fatal(err)
	}
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background(), Execution, []byte(`{"messages":[{"role":"user","content":"fixture"}]}`), consume); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.processes) != 2 {
		t.Fatalf("unexpected native process count %d", len(factory.processes))
	}
	for _, p := range factory.processes {
		select {
		case <-p.Done():
		default:
			t.Fatal("Router exit left model child", p.PID())
		}
	}
}
