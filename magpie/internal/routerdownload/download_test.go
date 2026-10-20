package routerdownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func artifact(b []byte) routermanifest.Artifact {
	h := sha256.Sum256(b)
	return routermanifest.Artifact{ID: "fixture", FileName: "fixture.bin", Size: int64(len(b)), SHA256: hex.EncodeToString(h[:]), Format: "file"}
}
func localArtifact(t *testing.T, b []byte) (routermanifest.Artifact, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.archive")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return artifact(b), p
}
func requireMissing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should be absent: %v", p, err)
	}
}
func serveRange(w http.ResponseWriter, r *http.Request, b []byte) {
	w.Header().Set("ETag", `"revision-1"`)
	if r.Header.Get("Range") == "" {
		w.Write(b)
		return
	}
	var start int
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &start); err != nil || start < 0 || start >= len(b) {
		w.WriteHeader(416)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(b)-1, len(b)))
	w.WriteHeader(206)
	w.Write(b[start:])
}

func TestDownloadCancelAndRestartResume(t *testing.T) {
	b := bytes.Repeat([]byte("pinned model bytes"), 16384)
	var requests atomic.Int32
	var resumed atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("ETag", `"revision-1"`)
			w.Write(b[:32768])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") == "bytes=32768-" && r.Header.Get("If-Range") == `"revision-1"` {
			resumed.Store(true)
		}
		serveRange(w, r, b)
	}))
	defer s.Close()
	a := artifact(b)
	a.URL = s.URL
	dest := filepath.Join(t.TempDir(), "model.gguf")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := Downloader{Client: s.Client()}
	err := d.Download(ctx, a, dest, func(p Progress) {
		if p.Downloaded >= 32768 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	requireMissing(t, dest)
	fi, err := os.Stat(dest + ".part")
	if err != nil || fi.Size() != 32768 {
		t.Fatalf("partial %v %v", fi, err)
	}
	// No in-memory downloader state is required to continue after restart.
	if err := (&Downloader{Client: s.Client()}).Download(context.Background(), a, dest, nil); err != nil {
		t.Fatal(err)
	}
	if !resumed.Load() {
		t.Fatal("restart did not resume persisted bytes with If-Range")
	}
	if err := Verify(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	requireMissing(t, dest+".part")
	requireMissing(t, dest+".part.json")
}

func seedPartial(t *testing.T, a routermanifest.Artifact, dest string, b []byte) {
	t.Helper()
	if err := os.WriteFile(dest+".part", b, 0o600); err != nil {
		t.Fatal(err)
	}
	m := partial{URL: a.URL, SHA256: a.SHA256, Size: a.Size, ETag: `"old"`}
	j, _ := json.Marshal(m)
	if err := os.WriteFile(dest+".part.json", j, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResumeServerIgnoresRangeAndResourceChanges(t *testing.T) {
	for _, kind := range []string{"ignore-range", "changed-206", "bad-range"} {
		t.Run(kind, func(t *testing.T) {
			b := []byte("the exact pinned artifact version")
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Range") != "" && kind != "ignore-range" {
					w.Header().Set("ETag", `"old"`)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 8-%d/%d", len(b)-1, len(b)))
					if kind == "changed-206" {
						w.Header().Set("ETag", `"new"`)
					} else {
						w.Header().Set("Content-Range", "bytes 0-2/3")
					}
					w.WriteHeader(206)
					w.Write(b[8:])
					return
				}
				w.Write(b)
			}))
			defer s.Close()
			a := artifact(b)
			a.URL = s.URL
			dest := filepath.Join(t.TempDir(), "artifact")
			seedPartial(t, a, dest, []byte("OLDprefix"[:8]))
			if err := (&Downloader{Client: s.Client()}).Download(context.Background(), a, dest, nil); err != nil {
				t.Fatal(err)
			}
			if err := Verify(context.Background(), a, dest); err != nil {
				t.Fatal(err)
			}
			want := int32(2)
			if kind == "ignore-range" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("request count %d, want %d", calls.Load(), want)
			}
		})
	}
}

func TestDownloadTruncationCorruptionAndUnexpectedStatus(t *testing.T) {
	for _, kind := range []string{"truncated", "corrupt", "oversized", "http-error", "unsolicited-partial", "encoded"} {
		t.Run(kind, func(t *testing.T) {
			b := []byte("confirmed complete artifact")
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "truncated":
					w.Header().Set("Content-Length", fmt.Sprint(len(b)))
					w.Write(b[:8])
				case "corrupt":
					w.Write(bytes.Repeat([]byte("x"), len(b)))
				case "oversized":
					w.Write(append(b, 'x'))
				case "http-error":
					w.WriteHeader(503)
				case "unsolicited-partial":
					w.WriteHeader(206)
					w.Write(b)
				case "encoded":
					w.Header().Set("Content-Encoding", "gzip")
					w.Write(b)
				}
			}))
			defer s.Close()
			a := artifact(b)
			a.URL = s.URL
			dest := filepath.Join(t.TempDir(), "artifact")
			if err := (&Downloader{Client: s.Client()}).Download(context.Background(), a, dest, nil); err == nil {
				t.Fatal("failure was installed")
			}
			requireMissing(t, dest)
			if kind == "corrupt" || kind == "oversized" {
				requireMissing(t, dest+".part")
			}
			if kind == "truncated" {
				fi, err := os.Stat(dest + ".part")
				if err != nil || fi.Size() != 8 {
					t.Fatalf("truncated bytes not resumable: %v %v", fi, err)
				}
			}
		})
	}
}

func TestDownloadDiskErrorsAndUnknownManifestMakeNoRequest(t *testing.T) {
	for _, kind := range []string{"insufficient", "disk-unknown", "unknown-hash", "unknown-size"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte("data")) }))
			defer s.Close()
			a := artifact([]byte("data"))
			a.URL = s.URL
			a.ExpandedBytes = 10
			if kind == "unknown-hash" {
				a.SHA256 = ""
			}
			if kind == "unknown-size" {
				a.Size = 0
			}
			d := Downloader{Client: s.Client(), FreeBytes: func(string) (uint64, error) {
				if kind == "disk-unknown" {
					return 0, errors.New("disk probe failed")
				}
				return 13, nil
			}}
			dest := filepath.Join(t.TempDir(), "model")
			if err := d.Download(context.Background(), a, dest, nil); err == nil {
				t.Fatal("expected closed failure")
			}
			if calls.Load() != 0 {
				t.Fatal("sent request without known space/manifest")
			}
			requireMissing(t, dest)
		})
	}
}

func TestDownloadDuplicatesAndAlreadyInstalled(t *testing.T) {
	b := []byte("installed bytes")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); close(entered); <-release; w.Write(b) }))
	defer s.Close()
	a := artifact(b)
	a.URL = s.URL
	dest := filepath.Join(t.TempDir(), "model")
	d := Downloader{Client: s.Client()}
	done := make(chan error, 1)
	go func() { done <- d.Download(context.Background(), a, dest, nil) }()
	<-entered
	err := (&Downloader{Client: s.Client()}).Download(context.Background(), a, dest, nil)
	close(release)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("duplicate %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := d.Download(context.Background(), a, dest, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("re-downloaded verified installed artifact")
	}
}

func TestCompletePartialPublishesOnlyAfterVerification(t *testing.T) {
	b := []byte("complete verified artifact")
	a := artifact(b)
	a.URL = "http://127.0.0.1:1/never-reached"
	dest := filepath.Join(t.TempDir(), "model")
	seedPartial(t, a, dest, b)
	if err := (&Downloader{}).Download(context.Background(), a, dest, nil); err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), a, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, bytes.Repeat([]byte("x"), len(b)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), a, dest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("installed corruption %v", err)
	}
}

// TestDownloadRestartHelper runs in a separate process so persisted state, file
// handles and lock release are exercised across an actual process boundary.
func TestDownloadRestartHelper(t *testing.T) {
	if os.Getenv("ROUTERDOWNLOAD_TEST_HELPER") != "1" {
		return
	}
	var a routermanifest.Artifact
	if err := json.Unmarshal([]byte(os.Getenv("ROUTERDOWNLOAD_TEST_ARTIFACT")), &a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (&Downloader{}).Download(ctx, a, os.Getenv("ROUTERDOWNLOAD_TEST_DEST"), func(p Progress) {
		if p.Downloaded >= 32768 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("child cancellation: %v", err)
	}
}

func TestDownloadResumesAfterChildProcessExit(t *testing.T) {
	b := bytes.Repeat([]byte("restart persistence"), 8192)
	var calls atomic.Int32
	var resumed atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"revision-1"`)
			w.Write(b[:32768])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		resumed.Store(strings.HasPrefix(r.Header.Get("Range"), "bytes=32768-"))
		serveRange(w, r, b)
	}))
	defer s.Close()
	a := artifact(b)
	a.URL = s.URL
	dest := filepath.Join(t.TempDir(), "model")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(a)
	cmd := proc.Command(exe, "-test.run=^TestDownloadRestartHelper$")
	cmd.Env = append(os.Environ(), "ROUTERDOWNLOAD_TEST_HELPER=1", "ROUTERDOWNLOAD_TEST_ARTIFACT="+string(j), "ROUTERDOWNLOAD_TEST_DEST="+dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	requireMissing(t, dest)
	if err := (&Downloader{Client: s.Client()}).Download(context.Background(), a, dest, nil); err != nil {
		t.Fatal(err)
	}
	if !resumed.Load() {
		t.Fatal("new process did not resume child partial")
	}
}

func TestVerifyIsReadOnlyAndContextBound(t *testing.T) {
	a := artifact([]byte("x"))
	missing := filepath.Join(t.TempDir(), "missing", "file")
	if err := Verify(context.Background(), a, missing); err == nil {
		t.Fatal("missing accepted")
	}
	requireMissing(t, filepath.Dir(missing))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Verify(ctx, a, missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	if err := (&Downloader{}).Download(ctx, a, missing, nil); err == nil {
		t.Fatal("canceled download accepted")
	}
}

func TestDownloadReportsInitialProgressAndRecoversFromStall(t *testing.T) {
	b := bytes.Repeat([]byte("stall-recovery-payload-"), 2048)
	var calls atomic.Int32
	var sawInitialBeforeHeaders atomic.Bool
	var progressReportedBeforeRequest atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			if progressReportedBeforeRequest.Load() {
				sawInitialBeforeHeaders.Store(true)
			}
			w.Header().Set("ETag", `"revision-1"`)
			_, _ = w.Write(b[:8192])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=8192-" {
			t.Errorf("expected resume Range bytes=8192-, got %q", r.Header.Get("Range"))
		}
		serveRange(w, r, b)
	}))
	defer s.Close()

	a := artifact(b)
	a.URL = s.URL
	dest := filepath.Join(t.TempDir(), "stalled-model")
	d := &Downloader{Client: s.Client(), StallTimeout: 80 * time.Millisecond}
	var first Progress
	var count int
	if err := d.Download(context.Background(), a, dest, func(p Progress) {
		if count == 0 {
			first = p
			progressReportedBeforeRequest.Store(true)
		}
		count++
	}); err != nil {
		t.Fatalf("expected mid-stream stall to auto-resume and succeed, got %v", err)
	}
	if !sawInitialBeforeHeaders.Load() || first.Downloaded != 0 || first.Total != a.Size {
		t.Fatalf("expected initial 0/%d progress before HTTP headers, got %+v (beforeHeaders=%v)", a.Size, first, sawInitialBeforeHeaders.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 HTTP attempts, got %d", calls.Load())
	}

	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hangServer.Close()
	aHang := artifact(b)
	aHang.URL = hangServer.URL
	hangDest := filepath.Join(t.TempDir(), "hang-model")
	err := (&Downloader{Client: hangServer.Client(), StallTimeout: 60 * time.Millisecond}).Download(context.Background(), aHang, hangDest, nil)
	if err == nil || !strings.Contains(err.Error(), "download stalled") {
		t.Fatalf("expected stall timeout error, got %v", err)
	}
}
