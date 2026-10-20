package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func rangeFixture(t *testing.T, size int) ([]byte, routermanifest.Artifact) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + i/1024)
	}
	hash := sha256.Sum256(data)
	return data, routermanifest.Artifact{ID: "range-fixture", FileName: "fixture.gguf", Format: "file", Size: int64(size), SHA256: hex.EncodeToString(hash[:])}
}

func rangeRequest(t *testing.T, req *http.Request, size int) (int, int) {
	t.Helper()
	var start, end int
	if _, err := fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start || end >= size {
		t.Errorf("invalid chunk range %q", req.Header.Get("Range"))
		return 0, 0
	}
	if req.URL.Query().Get("download") != "true" || req.URL.Query().Get("range") != fmt.Sprintf("%d-%d", start, end) {
		t.Errorf("cache-busting query differs from actual range: %s", req.URL.RawQuery)
	}
	return start, end
}

func serveChunk(w http.ResponseWriter, data []byte, start, end int) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[start : end+1])
}

func testRangeTransport(base http.RoundTripper, a routermanifest.Artifact, folder string, workers int) *rangeTransport {
	t := newRangeTransport(base, a, folder, workers, `"persisted-primary"`).(*rangeTransport)
	t.chunkBytes = 64 << 10
	return t
}

func TestRangeTransportOrdersOutOfOrderChunksAndReusesReceipts(t *testing.T) {
	const chunkSize = 64 << 10
	data, a := rangeFixture(t, 3*chunkSize+123)
	thirdStarted := make(chan struct{})
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		start, end := rangeRequest(t, req, len(data))
		if start == 2*chunkSize {
			close(thirdStarted)
		}
		if start == 0 {
			select {
			case <-thirdStarted:
			case <-req.Context().Done():
				return
			}
		}
		serveChunk(w, data, start, end)
	}))
	defer s.Close()
	a.URL = s.URL + "/fixed"
	folder := t.TempDir()
	transport := testRangeTransport(s.Client().Transport, a, folder, 3)
	destination := filepath.Join(t.TempDir(), a.FileName)
	d := routerdownload.Downloader{Client: &http.Client{Transport: transport}}
	if err := d.Download(context.Background(), a, destination, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("assembled bytes differ: %v", err)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests=%d, want 4", got)
	}
	// A new transport/process can reuse disk receipts without upstream access.
	other := filepath.Join(t.TempDir(), a.FileName)
	d.Client.Transport = testRangeTransport(s.Client().Transport, a, folder, 2)
	if err := d.Download(context.Background(), a, other, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatal("completed cache chunks were downloaded again")
	}
}

func TestRangeTransportShortReadResumesWithinChunk(t *testing.T) {
	data, a := rangeFixture(t, 64<<10)
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start, end := rangeRequest(t, req, len(data))
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:16384])
			return
		}
		if start != 16384 {
			t.Errorf("short-read retry started at %d; want retained 16384-byte prefix", start)
		}
		serveChunk(w, data, start, end)
	}))
	defer s.Close()
	a.URL = s.URL + "/fixed"
	transport := testRangeTransport(s.Client().Transport, a, t.TempDir(), 1)
	d := routerdownload.Downloader{Client: &http.Client{Transport: transport}}
	destination := filepath.Join(t.TempDir(), a.FileName)
	if err := d.Download(context.Background(), a, destination, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d, want 2", requests.Load())
	}
}

func TestRangeTransportRejectsFullResponsesWithoutClearingPrimaryPartial(t *testing.T) {
	data, a := rangeFixture(t, 64<<10)
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		_, _ = w.Write(data)
	}))
	defer s.Close()
	a.URL = s.URL + "/fixed"
	destination := filepath.Join(t.TempDir(), a.FileName)
	prefix := data[:12345]
	if err := os.WriteFile(destination+".part", prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"url": a.URL, "sha256": a.SHA256, "size": a.Size, "etag": `"persisted-primary"`})
	if err := os.WriteFile(destination+".part.json", meta, 0o600); err != nil {
		t.Fatal(err)
	}
	transport := testRangeTransport(s.Client().Transport, a, t.TempDir(), 1)
	d := routerdownload.Downloader{Client: &http.Client{Transport: transport}}
	if err := d.Download(context.Background(), a, destination, nil); err == nil {
		t.Fatal("HTTP 200 chunks were accepted")
	}
	actual, err := os.ReadFile(destination + ".part")
	if err != nil || !bytes.Equal(actual, prefix) {
		t.Fatalf("existing primary partial was changed: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests=%d, want bounded 3", requests.Load())
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified final file exists: %v", err)
	}
}

func TestRangeTransportCancellationJoinsAndPreservesCompletedChunks(t *testing.T) {
	const chunkSize = 64 << 10
	data, a := rangeFixture(t, 3*chunkSize)
	blocked := make(chan struct{})
	var blockOnce sync.Once
	var requests atomic.Int32
	var active atomic.Int32
	var permitLast atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		requests.Add(1)
		start, end := rangeRequest(t, req, len(data))
		if start == 2*chunkSize && !permitLast.Load() {
			blockOnce.Do(func() { close(blocked) })
			<-req.Context().Done()
			return
		}
		serveChunk(w, data, start, end)
	}))
	defer s.Close()
	a.URL = s.URL + "/fixed"
	folder := t.TempDir()
	transport := testRangeTransport(s.Client().Transport, a, folder, 3)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	var prefix bytes.Buffer
	if _, err := io.CopyN(&prefix, resp.Body, 2*chunkSize); err != nil {
		t.Fatal(err)
	}
	<-blocked
	cancel()
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	// Close joins the transport's workers and releases its OS cache lock.
	if !bytes.Equal(prefix.Bytes(), data[:2*chunkSize]) {
		t.Fatal("prefix changed")
	}
	for offset := 0; offset < 2*chunkSize; offset += chunkSize {
		path := filepath.Join(folder, a.SHA256, fmt.Sprintf("%012d-%d.chunk", offset, chunkSize))
		if _, err := os.Stat(path + ".json"); err != nil {
			t.Fatalf("completed chunk receipt missing: %v", err)
		}
	}
	permitLast.Store(true)
	second, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = testRangeTransport(s.Client().Transport, a, folder, 3).RoundTrip(second)
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(actual, data) {
		t.Fatalf("restart failed: read=%v close=%v", readErr, closeErr)
	}
	if requests.Load() != 4 {
		t.Fatalf("requests=%d; completed chunks should be reused", requests.Load())
	}
	// Server teardown also waits for the canceled local handler.
	s.Close()
	if active.Load() != 0 {
		t.Fatal("a canceled fixture request survived server shutdown")
	}
}

func TestRangeTransportRejectsWrongRangeAndValidator(t *testing.T) {
	data, a := rangeFixture(t, 1024)
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 1-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data)
	}))
	defer s.Close()
	a.URL = s.URL + "/fixed"
	transport := testRangeTransport(s.Client().Transport, a, t.TempDir(), 1)
	req, _ := http.NewRequest(http.MethodGet, a.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err == nil || requests.Load() != 3 {
		t.Fatalf("bad Content-Range accepted or retry unbounded: err=%v calls=%d", err, requests.Load())
	}
	req, _ = http.NewRequest(http.MethodGet, a.URL, nil)
	req.Header.Set("Range", "bytes=16-")
	req.Header.Set("If-Range", time.Unix(0, 0).UTC().Format(http.TimeFormat))
	if _, err := transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "validator differs") {
		t.Fatalf("incompatible validator was not refused: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatal("incompatible resume made an upstream request")
	}
}
