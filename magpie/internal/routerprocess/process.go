// Package routerprocess owns the single llama-server child requested by the
// runtime manager. A healthy process does not establish model capabilities.
package routerprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/proc"
)

// Spec describes verified local files. The caller owns artifact verification.
type Spec struct {
	Executable, ModelPath string
	ContextSize, Parallel int
	Backend               string
	ExtraArgs             []string
	Env                   []string
	RuntimeDependencies   *DependencySpec
}

type Process interface {
	Endpoint() string
	PID() int
	Done() <-chan struct{}
	Err() error
	Health(context.Context) error
	Stop(context.Context) error
}

type Factory interface {
	Start(context.Context, Spec) (Process, error)
}

type NativeFactory struct {
	// OnStart is an optional trusted Go observer. It cannot be supplied by a
	// management request and receives the same owned process returned to Manager.
	OnStart func(Process, Spec)
}

// NewHTTPClient has no proxy or redirect path by which a loopback request can
// disclose prompts to another server. Caller contexts own request budgets.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (f NativeFactory) Start(ctx context.Context, spec Spec) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	dir, env, err := dependencyLaunch(ctx, spec)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve llama-server port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	args := arguments(spec, port)
	cmd := proc.Command(spec.Executable, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	// llama-server cannot inherit a listener. A bind race becomes a failed
	// child/health check, never permission to adopt another process.
	if err := listener.Close(); err != nil {
		return nil, err
	}
	owner, err := startOwned(cmd)
	if err != nil {
		return nil, fmt.Errorf("start owned llama-server: %w", err)
	}
	p := &nativeProcess{endpoint: "http://127.0.0.1:" + strconv.Itoa(port), pid: cmd.Process.Pid,
		owner: owner, done: make(chan struct{}), client: NewHTTPClient()}
	go func() {
		err := owner.wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		p.client.CloseIdleConnections()
		close(p.done)
	}()
	if err := ctx.Err(); err != nil {
		owner.kill()
		<-p.done
		return nil, err
	}
	if f.OnStart != nil {
		f.OnStart(p, spec)
	}
	return p, nil
}

func validate(s Spec) error {
	for name, path := range map[string]string{"executable": s.Executable, "model": s.ModelPath} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", name)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be an existing regular file", name)
		}
	}
	if s.ContextSize < 0 || s.Parallel < 0 || s.Parallel > 1 {
		return errors.New("context must be nonnegative and llama-server has exactly one slot")
	}
	switch s.Backend {
	case "", "cpu", "cuda", "vulkan", "metal":
	default:
		return errors.New("unknown llama-server backend")
	}
	// Extra arguments cannot replace the fixed local binding, model or slot
	// contract. This seam exists for explicit test controls and future tuning.
	for _, a := range s.ExtraArgs {
		key, _, _ := strings.Cut(a, "=")
		switch key {
		case "--host", "--port", "-m", "--model", "-c", "--ctx-size", "-np", "--parallel",
			"--cache-reuse", "--cache-prompt", "--no-cache-prompt", "--jinja", "--no-jinja",
			"-ngl", "--gpu-layers", "--n-gpu-layers", "--model-url", "-mu", "--hf-repo", "-hf":
			return fmt.Errorf("extra argument %s overrides the fixed process contract", key)
		}
	}
	return nil
}

// Flags are pinned to llama.cpp b10828 tools/server/README.md. Prompt caching
// is enabled by default there; cache-reuse configures KV shifting, not proof
// of a speedup. No performance or specialist capability is inferred from it.
func arguments(s Spec, port int) []string {
	contextSize := s.ContextSize
	if contextSize == 0 {
		contextSize = 4096
	}
	args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--model", s.ModelPath,
		"--ctx-size", strconv.Itoa(contextSize), "--parallel", "1", "--cache-reuse", "256", "--jinja"}
	if s.Backend == "" || s.Backend == "cpu" {
		args = append(args, "--n-gpu-layers", "0")
	}
	return append(args, s.ExtraArgs...)
}

type nativeProcess struct {
	endpoint string
	pid      int
	owner    ownedProcess
	done     chan struct{}
	client   *http.Client
	mu       sync.Mutex
	err      error
	stop     sync.Once
}

func (p *nativeProcess) Endpoint() string      { return p.endpoint }
func (p *nativeProcess) PID() int              { return p.pid }
func (p *nativeProcess) Done() <-chan struct{} { return p.done }
func (p *nativeProcess) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *nativeProcess) Health(ctx context.Context) error {
	select {
	case <-p.done:
		return fmt.Errorf("llama-server exited: %v", p.Err())
	default:
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/health", nil)
	if err != nil {
		return err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("llama-server health HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return errors.New("invalid llama-server health response")
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &health); err != nil || health.Status != "ok" {
		return errors.New("llama-server health status is not ok")
	}
	select {
	case <-p.done:
		return errors.New("llama-server exited during health check")
	default:
		return nil
	}
}

// Stop always initiates termination, even when its wait context has expired.
// The one background waiter reaps the child and closes Done for every caller.
func (p *nativeProcess) Stop(ctx context.Context) error {
	p.stop.Do(p.owner.kill)
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type ownedProcess interface {
	wait() error
	kill()
}
