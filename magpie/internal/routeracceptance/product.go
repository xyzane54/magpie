package routeracceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerruntime"
	"github.com/yetone/magpie/internal/routerservice"
)

var dependencyAssertions = []string{
	"dependency_pins", "dependencies_default_no_install", "dependencies_verified",
	"dependencies_validated_before_load", "dependencies_loaded_modules",
	"dependencies_no_manual_environment", "dependencies_repeat_install",
}

type managementEvidence struct {
	Action     string          `json:"action"`
	Started    time.Time       `json:"started"`
	DurationMS float64         `json:"durationMs"`
	HTTPStatus int             `json:"httpStatus"`
	Response   json.RawMessage `json:"response"`
}

type productSession struct {
	manager     *routerruntime.Manager
	client      *http.Client
	base, token string
	cancel      context.CancelFunc
	done        <-chan error
	stopOnce    sync.Once
	stopped     chan struct{}
	stopErr     error
	mu          sync.Mutex
	requests    []managementEvidence
}

func startProduct(ctx context.Context, layout dirs.Layout, factory *trackingFactory) (*productSession, error) {
	// Reserve a literal loopback port, then let the unchanged service validate
	// and bind it. A lost bind race fails; no other listener is adopted.
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		return nil, err
	}
	store := routerconfig.New(layout)
	if err := store.UpdateContext(ctx, func(d routerconfig.Document) error {
		d["listen"], _ = json.Marshal(map[string]string{"address": address})
		return nil
	}); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	managers := make(chan *routerruntime.Manager, 1)
	ready := make(chan routerservice.Info, 1)
	done := make(chan error, 1)
	go func() {
		done <- routerservice.RunWithModelOptions(life, layout, "local-diagnostic", func(info routerservice.Info) error {
			ready <- info
			return nil
		}, routerservice.ModelOptions{
			LocalModelsReady:    func(local *routerservice.LocalModels) { managers <- local.Manager },
			LocalProcessStarted: factory.observe,
		})
	}()
	s := &productSession{cancel: cancel, done: done, stopped: make(chan struct{}), client: routerprocess.NewHTTPClient()}
	finished := false
	fail := func(err error) (*productSession, error) {
		cancel()
		if !finished {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		s.client.CloseIdleConnections()
		return nil, err
	}
	select {
	case s.manager = <-managers:
	case err := <-done:
		finished = true
		return fail(fmt.Errorf("product service startup: %v", err))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	select {
	case info := <-ready:
		s.base = "http://" + info.Address
	case err := <-done:
		finished = true
		return fail(fmt.Errorf("product service readiness: %v", err))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	token, err := os.ReadFile(routerservice.TokenFile(layout))
	if err != nil || len(token) > 256 {
		return fail(errors.Join(err, check(len(token) <= 256, "invalid management token file")))
	}
	s.token = strings.TrimSpace(string(token))
	if s.token == "" {
		return fail(errors.New("product service created an empty management token"))
	}
	return s, nil
}

func (s *productSession) action(ctx context.Context, action, model string) error {
	method, path := http.MethodPost, "/api/local-model/"+action
	if action == "status" {
		method, path = http.MethodGet, "/api/local-model"
	}
	input := map[string]string{}
	if action == "select" {
		input["model"] = model
	}
	body, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.Join(err, check(len(data) <= 1<<20, "management response exceeded one MiB"))
	}
	if !json.Valid(data) {
		return errors.New("product management response was not JSON")
	}
	s.mu.Lock()
	s.requests = append(s.requests, managementEvidence{Action: action, Started: started.UTC(), DurationMS: milliseconds(time.Since(started)), HTTPStatus: response.StatusCode, Response: append([]byte(nil), data...)})
	s.mu.Unlock()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("product management %s HTTP %d: %s", action, response.StatusCode, string(data))
	}
	return nil
}

func (s *productSession) stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.cancel()
		go func() { s.stopErr = <-s.done; s.client.CloseIdleConnections(); close(s.stopped) }()
	})
	select {
	case <-s.stopped:
		return s.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *productSession) snapshot() []managementEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]managementEvidence(nil), s.requests...)
}

func warmSources(ctx context.Context, sources []source, layout dirs.Layout, client *http.Client) error {
	paths := []string{
		filepath.Join(layout.Runtime, sources[0].Artifact.ID+"-archive", sources[0].Artifact.FileName),
		filepath.Join(layout.Models, sources[1].Artifact.ID, sources[1].Artifact.FileName),
		filepath.Join(layout.Models, sources[1].Artifact.ID, sources[2].Artifact.FileName),
	}
	downloader := &routerdownload.Downloader{Client: client}
	for i, source := range sources {
		if err := downloader.Download(ctx, source.Artifact, paths[i], nil); err != nil {
			return err
		}
	}
	return nil
}

func reserveEvidence(output string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	for _, path := range []string{output, filepath.Join(filepath.Dir(output), "normal.json"), filepath.Join(filepath.Dir(output), "tool-history.json"), filepath.Join(filepath.Dir(output), "after-cancel.json"), filepath.Join(filepath.Dir(output), "stream.sse"), filepath.Join(filepath.Dir(output), "cancel.sse")} {
		if err := requireAbsent(path); err != nil {
			return err
		}
	}
	marker, err := os.OpenFile(filepath.Join(filepath.Dir(output), ".acceptance-run"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("evidence directory was already used; select a new output directory: %w", err)
	}
	return marker.Close()
}

func executableHash() (string, string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return path, "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return path, "", err
	}
	return path, hex.EncodeToString(hash.Sum(nil)), nil
}
