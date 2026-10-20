package routerprocess

import (
	"context"
	"errors"
	"sync"

	"github.com/yetone/magpie/internal/proc"
)

// RuntimeVersion executes only --version with the same dependency validation,
// app-local working directory, isolated environment and OS ownership as Start.
// The caller's context bounds the probe; cancellation terminates and reaps it.
func RuntimeVersion(ctx context.Context, spec Spec) ([]byte, error) {
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
	output := &dependencyVersionOutput{}
	cmd := proc.Command(spec.Executable, "--version")
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = output, output
	owner, err := startOwned(cmd)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- owner.wait() }()
	select {
	case err = <-done:
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	case <-ctx.Done():
		owner.kill()
		<-done
		err = ctx.Err()
	}
	if output.truncated && err == nil {
		err = errors.New("runtime version output exceeded 64 KiB")
	}
	return output.data, err
}

type dependencyVersionOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (w *dependencyVersionOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := (64 << 10) - len(w.data)
	if len(data) > remaining {
		w.truncated = true
		w.data = append(w.data, data[:remaining]...)
	} else {
		w.data = append(w.data, data...)
	}
	return len(data), nil
}
