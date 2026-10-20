package routeracceptance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerruntime"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func fixtureArtifact(id, file string, data []byte) routermanifest.Artifact {
	hash := sha256.Sum256(data)
	return routermanifest.Artifact{ID: id, URL: "https://pinned.invalid/" + file, FileName: file, Format: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
}

func TestOfflineImportOnlyMapsExactURLsAndOwnedLoopback(t *testing.T) {
	data := []byte("known fixture bytes")
	path := filepath.Join(t.TempDir(), "fixture.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := fixtureArtifact("fixture", "fixture.bin", data)
	client, close := offlineClient([]source{{artifact, path}})
	defer close()
	request, _ := http.NewRequest(http.MethodGet, artifact.URL, nil)
	request.Header.Set("Range", "bytes=6-")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusPartialContent || !bytes.Equal(body, data[6:]) {
		t.Fatalf("range was not preserved: status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
	for _, rawURL := range []string{"https://other.invalid/fixture.bin", artifact.URL + "?different=1", "http://127.0.0.1:1/fixture.bin"} {
		if response, err := client.Get(rawURL); err == nil {
			response.Body.Close()
			t.Fatalf("unmapped URL reached a transport: %s", rawURL)
		}
	}
	transport := client.Transport.(mappedTransport).inner.(*http.Transport)
	if connection, err := transport.DialContext(context.Background(), "tcp", "example.invalid:443"); err == nil {
		connection.Close()
		t.Fatal("inner transport permitted a destination outside its owned loopback server")
	}
}

func TestFormalInstallerRechecksImportedBytes(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("release/llama-server.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("fixture executable; never started"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	modelBytes, templateBytes := []byte("fixture gguf content"), []byte("fixture template")
	model := routermanifest.Model{ID: "fixture-model", ContextTokens: 512, Artifact: fixtureArtifact("fixture-model", "fixture.gguf", modelBytes), Template: fixtureArtifact("fixture-template", "chat_template.jinja", templateBytes)}
	rt := routermanifest.Runtime{ID: "fixture-runtime", Executable: "llama-server.exe", Backend: "cpu", Artifact: fixtureArtifact("fixture-runtime", "runtime.zip", archive.Bytes())}
	rt.Artifact.Format, rt.Artifact.ExpandedBytes = "zip", 1<<20
	for _, variant := range []string{"valid", "missing", "wrong-size", "wrong-hash"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			cache := filepath.Join(root, "cache")
			if err := os.MkdirAll(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			sources := []source{{rt.Artifact, filepath.Join(cache, rt.Artifact.FileName)}, {model.Artifact, filepath.Join(cache, model.Artifact.FileName)}, {model.Template, filepath.Join(cache, model.Template.FileName)}}
			contents := [][]byte{archive.Bytes(), append([]byte(nil), modelBytes...), templateBytes}
			if variant == "wrong-size" {
				contents[1] = []byte("short")
			}
			if variant == "wrong-hash" {
				contents[1][0] ^= 0x20
			}
			for i, source := range sources {
				if variant == "missing" && i == 1 {
					continue
				}
				if err := os.WriteFile(source.Path, contents[i], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			client, close := offlineClient(sources)
			defer close()
			layout := dirs.Layout{Models: filepath.Join(root, "models"), Runtime: filepath.Join(root, "runtime")}
			installer := &routerruntime.FileInstaller{Layout: layout, Downloader: &routerdownload.Downloader{Client: client}}
			_, err := installer.Install(context.Background(), model, rt, nil)
			if variant == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := installer.Verify(context.Background(), model, rt); err != nil {
					t.Fatal("formal verification rejected the valid import", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid local cache installed")
			}
			if variant == "wrong-hash" && !errors.Is(err, routerdownload.ErrIntegrity) {
				t.Fatalf("installer did not reject bytes by exact digest: %v", err)
			}
			if _, err := os.Stat(layout.ManifestFile()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed import published a manifest: %v", err)
			}
			if _, err := installer.Verify(context.Background(), model, rt); err == nil {
				t.Fatal("failed import acquired installed state")
			}
		})
	}
}

func TestFailedPreparationClosesWithoutModelStart(t *testing.T) {
	root := t.TempDir()
	options := options{Model: "spark-x2.5-1.7B", DataDir: filepath.Join(root, "data"), Output: filepath.Join(root, "evidence", "result.json"),
		ModelFile: filepath.Join(root, "missing.gguf"), RuntimeArchive: filepath.Join(root, "missing.zip"), TemplateFile: filepath.Join(root, "missing.jinja"),
		LoadTimeout: time.Minute, GenerationTimeout: time.Minute, InstallTimeout: time.Minute}
	report, err := run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "source_verification") || len(report.Processes) != 0 {
		t.Fatalf("missing source did not fail without starting a model: %v %+v", err, report.Processes)
	}
	if report.FinalStatus.PID != 0 || report.FinalStatus.Runtime != routerruntime.Unloaded {
		t.Fatal("failed preparation did not leave an unloaded manager", report.FinalStatus)
	}
	for _, assertion := range report.Assertions {
		if assertion.Name == "cleanup" && assertion.State != "pass" {
			t.Fatalf("failed preparation skipped cleanup: %+v", assertion)
		}
		if assertion.Name == "load" && assertion.State != "not_run" {
			t.Fatal("missing files were treated as a real load result", assertion)
		}
	}
}
