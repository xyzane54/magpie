package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/steady"
)

const rangeChunkBytes = 4 << 20

// rangeTransport is an acquisition-only adapter. The outer Downloader retains
// the immutable artifact identity, its primary .part, and final size/SHA checks.
// Chunk receipts detect local cache changes; they do not authenticate a model.
type rangeTransport struct {
	base         http.RoundTripper
	artifact     routermanifest.Artifact
	folder       string
	workers      int
	etag         string
	chunkBytes   int64
	attemptLimit int
	attemptTime  time.Duration
	logMu        sync.Mutex
}

// resumeETag is copied from the primary .part.json, never from an alternate
// source. This private assembled response keeps that validator so switching
// transfer strategy cannot reset the Downloader's existing verified prefix.
// Date-based If-Range resumes are refused rather than changing their metadata.
func newRangeTransport(base http.RoundTripper, a routermanifest.Artifact, folder string, workers int, resumeETag string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if resumeETag == "" {
		resumeETag = `"sha256-` + strings.ToLower(a.SHA256) + `"`
	}
	return &rangeTransport{base: base, artifact: a, folder: folder, workers: workers,
		etag: resumeETag, chunkBytes: rangeChunkBytes, attemptLimit: 3, attemptTime: 60 * time.Second}
}

type rangeChunk struct {
	offset int64
	length int64
}

type rangeReceipt struct {
	Version        int    `json:"version"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	ArtifactSize   int64  `json:"artifactSize"`
	Offset         int64  `json:"offset"`
	Length         int64  `json:"length"`
	SHA256         string `json:"sha256"`
}

type rangeResult struct {
	path string
	err  error
}

func (t *rangeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.String() != t.artifact.URL {
		return t.base.RoundTrip(req)
	}
	if err := t.artifact.Validate(); err != nil {
		return nil, err
	}
	if t.artifact.Format != "file" || t.workers < 1 || t.workers > 8 || t.chunkBytes <= 0 || t.chunkBytes > rangeChunkBytes || t.attemptLimit < 1 || t.attemptLimit > 3 || t.attemptTime <= 0 || t.attemptTime > 60*time.Second {
		return nil, errors.New("invalid bounded range acquisition settings")
	}
	if req.Body != nil && req.Body != http.NoBody {
		return nil, errors.New("range acquisition requires an empty GET body")
	}
	start, err := requestedRange(req.Header.Get("Range"), t.artifact.Size)
	if err != nil {
		return nil, err
	}
	if validator := req.Header.Get("If-Range"); validator != "" && validator != t.etag {
		return nil, errors.New("range acquisition validator differs; primary partial preserved")
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	folder := filepath.Join(t.folder, strings.ToLower(t.artifact.SHA256))
	if err := rangeCacheDirectory(folder); err != nil {
		return nil, err
	}
	unlock, err := lockRangeCache(filepath.Join(folder, "chunks.lock"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(req.Context())
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go t.stream(ctx, cancel, writer, done, unlock, folder, start)
	header := make(http.Header)
	header.Set("ETag", t.etag)
	header.Set("Accept-Ranges", "bytes")
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Content-Length", strconv.FormatInt(t.artifact.Size-start, 10))
	status := http.StatusOK
	if req.Header.Get("Range") != "" {
		status = http.StatusPartialContent
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, t.artifact.Size-1, t.artifact.Size))
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: header, ContentLength: t.artifact.Size - start, Request: req,
		Body: &rangeBody{reader: reader, cancel: cancel, done: done}}, nil
}

func requestedRange(value string, total int64) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if !strings.HasPrefix(value, "bytes=") || !strings.HasSuffix(value, "-") {
		return 0, errors.New("range acquisition requires a single open-ended range")
	}
	start, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, "bytes="), "-"), 10, 64)
	if err != nil || start < 0 || start >= total || value != fmt.Sprintf("bytes=%d-", start) {
		return 0, errors.New("invalid acquisition range")
	}
	return start, nil
}

type rangeBody struct {
	reader *io.PipeReader
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func (b *rangeBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *rangeBody) Close() error {
	b.once.Do(func() {
		b.cancel()
		_ = b.reader.Close()
		<-b.done
	})
	return nil
}

func (t *rangeTransport) stream(ctx context.Context, cancel context.CancelFunc, writer *io.PipeWriter, done chan struct{}, unlock func(), folder string, start int64) {
	var workers sync.WaitGroup
	watchStop, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = writer.CloseWithError(ctx.Err())
		case <-watchStop:
		}
	}()
	defer func() {
		cancel()
		workers.Wait()
		close(watchStop)
		<-watchDone
		unlock()
		close(done)
	}()
	first := start / t.chunkBytes * t.chunkBytes
	chunks := make([]rangeChunk, 0, (t.artifact.Size-first+t.chunkBytes-1)/t.chunkBytes)
	for offset := first; offset < t.artifact.Size; offset += t.chunkBytes {
		chunks = append(chunks, rangeChunk{offset: offset, length: min(t.chunkBytes, t.artifact.Size-offset)})
	}
	results := make([]chan rangeResult, len(chunks))
	for i := range results {
		results[i] = make(chan rangeResult, 1)
	}
	var next atomic.Int64
	failed := make(chan error, 1)
	for range min(t.workers, len(chunks)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= len(chunks) {
					return
				}
				path, err := t.chunk(ctx, folder, chunks[i])
				results[i] <- rangeResult{path: path, err: err}
				if err != nil {
					select {
					case failed <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	buffer := make([]byte, 32<<10)
	for i, chunk := range chunks {
		var result rangeResult
		select {
		case result = <-results[i]:
		case err := <-failed:
			_ = writer.CloseWithError(err)
			return
		case <-ctx.Done():
			select {
			case err := <-failed:
				_ = writer.CloseWithError(err)
			default:
				_ = writer.CloseWithError(ctx.Err())
			}
			return
		}
		if result.err != nil {
			_ = writer.CloseWithError(result.err)
			return
		}
		file, err := os.Open(result.path)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		skip := max(int64(0), start-chunk.offset)
		_, seekErr := file.Seek(skip, io.SeekStart)
		var copied int64
		if seekErr == nil {
			copied, err = io.CopyBuffer(writer, io.LimitReader(file, chunk.length-skip), buffer)
		} else {
			err = seekErr
		}
		closeErr := file.Close()
		if err == nil && copied != chunk.length-skip {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
	}
	_ = writer.Close()
}

func (t *rangeTransport) chunk(ctx context.Context, folder string, chunk rangeChunk) (string, error) {
	path := filepath.Join(folder, fmt.Sprintf("%012d-%d.chunk", chunk.offset, chunk.length))
	var last error
	for attempt := 1; attempt <= t.attemptLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if cached, err := t.cachedChunk(ctx, path, chunk); err != nil || cached {
			if cached && err == nil {
				t.log(map[string]any{"event": "range_chunk", "offset": chunk.offset, "bytes": chunk.length, "cached": true})
			}
			return path, err
		}
		call, cancel := context.WithTimeout(ctx, t.attemptTime)
		last = t.fetchChunk(call, path, chunk)
		cancel()
		if last == nil {
			t.log(map[string]any{"event": "range_chunk", "offset": chunk.offset, "bytes": chunk.length, "cached": false, "attempt": attempt})
			return path, nil
		}
		t.log(map[string]any{"event": "range_chunk_error", "offset": chunk.offset, "attempt": attempt, "error": last.Error()})
	}
	return "", fmt.Errorf("chunk at %d failed after %d attempts: %w", chunk.offset, t.attemptLimit, last)
}

func (t *rangeTransport) cachedChunk(ctx context.Context, path string, chunk rangeChunk) (bool, error) {
	if err := rangeRegularOrMissing(path); err != nil {
		return false, err
	}
	if err := rangeRegularOrMissing(path + ".json"); err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Size() != chunk.length {
		return false, fmt.Errorf("cached chunk has invalid length: %s", path)
	}
	hash, err := hashRangeFile(ctx, path)
	if err != nil {
		return false, err
	}
	receipt := rangeReceipt{Version: 1, ArtifactSHA256: strings.ToLower(t.artifact.SHA256), ArtifactSize: t.artifact.Size,
		Offset: chunk.offset, Length: chunk.length, SHA256: hash}
	file, err := os.Open(path + ".json")
	if errors.Is(err, os.ErrNotExist) {
		// A crash after publishing the flushed chunk but before its receipt is
		// recoverable. The outer Downloader still authenticates the full model.
		return true, writeRangeReceipt(ctx, path+".json", receipt)
	}
	if err != nil {
		return false, err
	}
	var actual rangeReceipt
	data, readErr := io.ReadAll(io.LimitReader(file, 16<<10))
	closeErr := file.Close()
	if readErr != nil {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := json.Unmarshal(data, &actual); err != nil || actual != receipt {
		return false, fmt.Errorf("cached chunk receipt or hash differs: %s", path)
	}
	return true, nil
}

func (t *rangeTransport) fetchChunk(ctx context.Context, path string, chunk rangeChunk) error {
	partial := path + ".part"
	if err := rangeRegularOrMissing(partial); err != nil {
		return err
	}
	var have int64
	if info, err := os.Stat(partial); err == nil {
		have = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if have < 0 || have > chunk.length {
		return errors.New("partial chunk exceeds its fixed range; retained for diagnosis")
	}
	if have < chunk.length {
		address, err := url.Parse(t.artifact.URL)
		if err != nil {
			return err
		}
		start, end := chunk.offset+have, chunk.offset+chunk.length-1
		query := address.Query()
		query.Set("download", "true")
		query.Set("range", fmt.Sprintf("%d-%d", start, end))
		address.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Cache-Control", "no-cache")
		client := &http.Client{Transport: t.base, CheckRedirect: rangeRedirect}
		resp, err := client.Do(req)
		if err != nil {
			// url.Error can include a signed CDN redirect. Preserve the cause,
			// never the redirect URL, in acquisition logs.
			var addressError *url.Error
			for errors.As(err, &addressError) {
				err = addressError.Err
			}
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end, t.artifact.Size) {
			return fmt.Errorf("range %d-%d refused: HTTP %d Content-Range %q", start, end, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
		if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
			return errors.New("encoded chunk response refused")
		}
		remaining := chunk.length - have
		if resp.ContentLength >= 0 && resp.ContentLength != remaining {
			return fmt.Errorf("range Content-Length %d differs from %d", resp.ContentLength, remaining)
		}
		file, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Seek(have, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		n, copyErr := io.Copy(file, io.LimitReader(resp.Body, remaining))
		var extra [1]byte
		if copyErr == nil && n == remaining {
			if count, readErr := io.ReadFull(resp.Body, extra[:]); count != 0 || !errors.Is(readErr, io.EOF) {
				copyErr = errors.New("overlong or interrupted chunk response")
				if err := file.Truncate(have); err != nil {
					copyErr = errors.Join(copyErr, err)
				}
			}
		}
		syncErr, closeErr := file.Sync(), file.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return errors.Join(copyErr, syncErr, closeErr)
		}
		if n != remaining {
			return io.ErrUnexpectedEOF
		}
	}
	hash, err := hashRangeFile(ctx, partial)
	if err != nil {
		return err
	}
	if err := steady.Rename(partial, path); err != nil {
		return err
	}
	return writeRangeReceipt(ctx, path+".json", rangeReceipt{Version: 1, ArtifactSHA256: strings.ToLower(t.artifact.SHA256),
		ArtifactSize: t.artifact.Size, Offset: chunk.offset, Length: chunk.length, SHA256: hash})
}

func rangeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 8 || req.URL.User != nil {
		return errors.New("artifact redirect limit or credentials refused")
	}
	initial := via[0].URL
	if req.URL.Scheme == initial.Scheme && req.URL.Host == initial.Host {
		return nil
	}
	host := strings.ToLower(req.URL.Hostname())
	if initial.Scheme == "https" && req.URL.Scheme == "https" && (host == "hf-mirror.com" || host == "huggingface.co" || strings.HasSuffix(host, ".hf.co") || strings.HasSuffix(host, ".huggingface.co")) {
		return nil
	}
	return errors.New("artifact redirect leaves trusted Hugging Face transport hosts")
}

func writeRangeReceipt(ctx context.Context, path string, receipt rangeReceipt) error {
	if err := rangeRegularOrMissing(path); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return steady.WriteFileContext(ctx, path, data, 0o600)
}

func hashRangeFile(ctx context.Context, path string) (string, error) {
	if err := rangeRegularOrMissing(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func (t *rangeTransport) log(event map[string]any) {
	event["artifact"] = t.artifact.ID
	event["at"] = time.Now().UTC()
	t.logMu.Lock()
	defer t.logMu.Unlock()
	_ = json.NewEncoder(os.Stderr).Encode(event)
}

func rangeCacheDirectory(folder string) error {
	if !filepath.IsAbs(folder) {
		return errors.New("range cache must be an absolute directory")
	}
	info, err := os.Lstat(folder)
	if errors.Is(err, os.ErrNotExist) {
		if err := rangeCacheDirectory(filepath.Dir(folder)); err != nil {
			return err
		}
		if err := os.Mkdir(folder, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(folder)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("range cache has an unsafe directory")
	}
	if parent := filepath.Dir(folder); parent != folder {
		return rangeCacheDirectory(parent)
	}
	return nil
}

func rangeRegularOrMissing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("range cache path is not a regular file: %s", path)
	}
	return nil
}

func lockRangeCache(path string) (func(), error) {
	if err := rangeRegularOrMissing(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryLock(file)
	if err != nil || !held {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("range cache is busy")
	}
	return func() { filelock.Unlock(file); file.Close() }, nil
}
