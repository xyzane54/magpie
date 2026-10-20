// model-fetch is an internal, bounded acquisition tool for the fixed catalog.
// It keeps the production downloader's validation and persistent resume state.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
)

func main() {
	tier := flag.String("tier", "1.7B", "fixed catalog tier: 1.7B or 4B")
	cache := flag.String("cache", "", "absolute persistent cache directory")
	attempts := flag.Int("attempts", 6, "bounded attempts per artifact, at most 12")
	budget := flag.Duration("attempt-timeout", 4*time.Minute, "deadline per resumable attempt, at most 15m")
	workers := flag.Int("workers", 0, "0 for direct HTTP/1.1; 1..8 for persistent bounded ranges")
	flag.Parse()
	if !filepath.IsAbs(*cache) || *attempts < 1 || *attempts > 12 || *budget <= 0 || *budget > 15*time.Minute || *workers < 0 || *workers > 8 {
		fmt.Fprintln(os.Stderr, "absolute cache, 1..12 attempts and a timeout in (0,15m] required")
		os.Exit(2)
	}
	m, ok := routermanifest.Fixed().Model("spark-x2.5-" + *tier)
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown fixed tier")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// HTTP/1.1 is a diagnostic transport choice, not a production source change.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	encoder := json.NewEncoder(os.Stdout)
	failed := false
	for _, a := range []routermanifest.Artifact{m.Template, m.Artifact} {
		destination := filepath.Join(*cache, *tier, a.FileName)
		var transfer http.RoundTripper = transport
		if *workers > 0 && a.ID == m.Artifact.ID {
			var metadata struct {
				ETag string `json:"etag"`
			}
			if data, err := os.ReadFile(destination + ".part.json"); err == nil {
				if err := json.Unmarshal(data, &metadata); err != nil {
					fmt.Fprintln(os.Stderr, "invalid existing resume metadata; preserved:", err)
					os.Exit(1)
				}
			} else if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			transfer = newRangeTransport(transport, a, destination+".ranges", *workers, metadata.ETag)
		}
		d := &routerdownload.Downloader{Client: &http.Client{Transport: transfer}}
		var err error
		for attempt := 1; attempt <= *attempts && ctx.Err() == nil; attempt++ {
			started := time.Now()
			encoder.Encode(map[string]any{"event": "attempt", "artifact": a.ID, "attempt": attempt, "path": destination, "partialBytes": fileSize(destination + ".part"), "at": started.UTC()})
			call, cancel := context.WithTimeout(ctx, *budget)
			last := time.Time{}
			err = d.Download(call, a, destination, func(p routerdownload.Progress) {
				if time.Since(last) >= 5*time.Second || p.Downloaded == p.Total {
					encoder.Encode(map[string]any{"event": "progress", "downloaded": p.Downloaded, "total": p.Total, "at": time.Now().UTC()})
					last = time.Now()
				}
			})
			cancel()
			result := map[string]any{"event": "attempt_result", "artifact": a.ID, "attempt": attempt, "elapsedMS": time.Since(started).Milliseconds(), "partialBytes": fileSize(destination + ".part"), "verified": err == nil, "at": time.Now().UTC()}
			if err != nil {
				result["error"] = err.Error()
			}
			encoder.Encode(result)
			if err == nil {
				break
			}
			if attempt < *attempts {
				select {
				case <-ctx.Done():
				case <-time.After(2 * time.Second):
				}
			}
		}
		if err != nil || ctx.Err() != nil {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
