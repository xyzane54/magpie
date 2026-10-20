// Package routercontrol controls only the authenticated instance in one product
// layout. It never kills a process or adopts a lock left by another instance.
package routercontrol

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
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/steady"
)

type Record struct {
	PID                   int       `json:"pid"`
	Executable            string    `json:"executable"`
	ProcessStamp          string    `json:"processStamp"`
	InstanceID            string    `json:"instanceId"`
	Address               string    `json:"address"`
	PreviousAddress       string    `json:"previousAddress,omitempty"`
	Version               string    `json:"version"`
	StartedAt             time.Time `json:"startedAt"`
	ExperimentalRedaction bool      `json:"experimentalRedaction"`
}
type Autostart struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Name      string `json:"name"`
	Error     string `json:"error,omitempty"`
}
type Status struct {
	Record
	Running       bool      `json:"running"`
	Autostart     Autostart `json:"autostart"`
	StartCommand  string    `json:"startCommand"`
	ManagementURL string    `json:"managementURL,omitempty"`
	TokenFile     string    `json:"tokenFile,omitempty"`
}

func RecordFile(l dirs.Layout) string { return filepath.Join(l.ServiceLock(), "instance.json") }
func StartCommand(l dirs.Layout, exe string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	// A copied command runs outside the service's environment. Preserve its
	// storage roots, otherwise a test/portable instance starts the default
	// configuration (and potentially a different port). Restore the caller's
	// environment after the child has inherited the selected layout.
	variables := [][2]string{{"APPDATA", filepath.Dir(l.Config)}, {"LOCALAPPDATA", filepath.Dir(l.Data)}}
	if runtime.GOOS != "windows" {
		variables = [][2]string{
			{"XDG_CONFIG_HOME", filepath.Dir(l.Config)}, {"XDG_DATA_HOME", filepath.Dir(l.Data)},
			{"XDG_CACHE_HOME", filepath.Dir(l.Cache)}, {"XDG_STATE_HOME", filepath.Dir(l.State)},
		}
	}
	var save, set, restore strings.Builder
	for i, variable := range variables {
		fmt.Fprintf(&save, "$routerSaved%d = $env:%s; ", i, variable[0])
		fmt.Fprintf(&set, "$env:%s = %s; ", variable[0], quote(variable[1]))
		fmt.Fprintf(&restore, "$env:%s = $routerSaved%d; ", variable[0], i)
	}
	return "& { " + save.String() + "try { " + set.String() + "& " + quote(exe) + " service start } finally { " + restore.String() + "} }"
}
func LaunchRecord(l dirs.Layout, version, instance, address string, trial bool, previous ...string) (Record, error) {
	exe, err := os.Executable()
	if err != nil {
		return Record{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return Record{}, err
	}
	stamp, actual, err := processIdentity(os.Getpid())
	if err != nil {
		return Record{}, err
	}
	if !samePath(exe, actual) {
		return Record{}, errors.New("service executable identity mismatch")
	}
	r := Record{PID: os.Getpid(), Executable: exe, ProcessStamp: stamp, InstanceID: instance, Address: address, Version: version, StartedAt: time.Now().UTC(), ExperimentalRedaction: trial}
	if len(previous) == 1 && previous[0] != address {
		r.PreviousAddress = previous[0]
	}
	b, err := json.Marshal(r)
	if err == nil {
		err = steady.WriteFile(RecordFile(l), b, 0o600)
	}
	return r, err
}
func ReadRecord(l dirs.Layout) (Record, error) {
	var r Record
	if err := safePath(l.ServiceLock()); err != nil {
		return r, err
	}
	b, err := readRegular(RecordFile(l))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("invalid service instance record: %w", err)
	}
	if r.PID <= 0 || r.ProcessStamp == "" || r.InstanceID == "" || !filepath.IsAbs(r.Executable) {
		return r, errors.New("invalid service instance identity")
	}
	if err := validateAddress(r.Address); err != nil {
		return r, err
	}
	return r, nil
}
func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid service address; literal loopback IP:port required")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return errors.New("service address must be a literal loopback IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid service address port")
	}
	return nil
}
func Verify(r Record) error {
	stamp, exe, err := processIdentity(r.PID)
	if err != nil {
		return fmt.Errorf("service PID identity unavailable: %w", err)
	}
	if stamp != r.ProcessStamp || !samePath(exe, r.Executable) {
		return errors.New("service PID was reused or executable identity differs; no process was changed")
	}
	return nil
}
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
func readRegular(p string) ([]byte, error) {
	if err := safePath(p); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return nil, fmt.Errorf("not a bounded regular product file: %s", p)
	}
	return os.ReadFile(p)
}

// safePath rejects links and Windows reparse points in all existing ancestors.
func safePath(p string) error {
	if !filepath.IsAbs(p) {
		return errors.New("product control path must be absolute")
	}
	for p != "" {
		fi, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (fi.Mode()&os.ModeSymlink != 0 || reparse(fi)) {
			return fmt.Errorf("product path must not cross a link or reparse point: %s", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return nil
}
func client() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func request(ctx context.Context, l dirs.Layout, r Record, action string) ([]byte, error) {
	if err := validateAddress(r.Address); err != nil {
		return nil, err
	}
	if err := Verify(r); err != nil {
		return nil, err
	}
	probe, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+r.Address+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client().Do(probe)
	if err != nil {
		return nil, err
	}
	var health struct {
		Service, Status string
		PID             int
		StartedAt       time.Time
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
	resp.Body.Close()
	if decodeErr != nil || resp.StatusCode != 200 || health.Service != "api-router" || health.Status != "ok" || health.PID != r.PID || !health.StartedAt.Equal(r.StartedAt) {
		return nil, errors.New("endpoint identity differs; management token was not sent")
	}
	token, err := readRegular(filepath.Join(l.ServiceLock(), "management-token"))
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	if action != "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+r.Address+"/api/service"+action, bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("X-API-Router-Instance", r.ProcessStamp)
	resp, err = client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("service not reachable; no process was changed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		return nil, fmt.Errorf("service control rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}
func Current(ctx context.Context, l dirs.Layout) (Status, error) {
	exe, err := os.Executable()
	if err != nil {
		return Status{}, err
	}
	s := Status{StartCommand: StartCommand(l, exe)}
	s.Autostart, err = AutostartStatus(l, exe)
	if err != nil {
		s.Autostart.Error = err.Error()
	}
	r, err := ReadRecord(l)
	if errors.Is(err, os.ErrNotExist) {
		if _, lockErr := os.Lstat(l.ServiceLock()); lockErr == nil {
			return s, errors.New("service lock has no instance record; inspect the owning process before manual recovery")
		}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.Record = r
	if !samePath(exe, r.Executable) {
		return s, errors.New("another executable owns this layout; use its own installed CLI; no instance adopted")
	}
	s.StartCommand = StartCommand(l, r.Executable)
	s.ManagementURL = "http://" + r.Address + "/"
	s.TokenFile = filepath.Join(l.ServiceLock(), "management-token")
	b, err := request(ctx, l, r, "")
	if err != nil {
		return s, err
	}
	var live Record
	if json.Unmarshal(b, &live) != nil || live.PID != r.PID || live.ProcessStamp != r.ProcessStamp || live.InstanceID != r.InstanceID || !samePath(live.Executable, r.Executable) {
		return s, errors.New("authenticated service identity differs from instance record")
	}
	s.Running = true
	return s, nil
}
func Stop(ctx context.Context, l dirs.Layout) (Status, error) {
	s, err := Current(ctx, l)
	if err != nil {
		return s, err
	}
	if !s.Running {
		return s, nil
	}
	if err = SaveOptions(l, s.ExperimentalRedaction); err != nil {
		return s, err
	}
	if _, err = request(ctx, l, s.Record, "/stop"); err != nil {
		return s, err
	}
	err = WaitStopped(ctx, l, s.Record)
	s.Running = false
	return s, err
}
func WaitStopped(ctx context.Context, l dirs.Layout, r Record) error {
	timer := time.NewTicker(40 * time.Millisecond)
	defer timer.Stop()
	for {
		_, lockErr := os.Lstat(l.ServiceLock())
		if errors.Is(lockErr, os.ErrNotExist) && Verify(r) != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service shutdown not complete; no process was killed: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

type launchOptions struct {
	ExperimentalRedaction bool `json:"experimentalRedaction"`
}

func preference(l dirs.Layout) (bool, error) {
	b, err := readRegular(filepath.Join(l.State, "service-options.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p launchOptions
	if err = json.Unmarshal(b, &p); err != nil {
		return false, fmt.Errorf("invalid saved service options: %w", err)
	}
	return p.ExperimentalRedaction, nil
}
func SaveOptions(l dirs.Layout, trial bool) error {
	if err := safePath(l.State); err != nil {
		return err
	}
	if err := os.MkdirAll(l.State, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(launchOptions{trial})
	return steady.WriteFile(filepath.Join(l.State, "service-options.json"), b, 0o600)
}

// Start preserves the last explicit trial choice when trial is nil. A running
// instance is reported, never duplicated or silently reconfigured.
func Start(ctx context.Context, l dirs.Layout, trial *bool) (result Status, failure error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	unlock, err := lockStartup(ctx, l)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	if err := archiveExitedLock(l); err != nil {
		return Status{}, err
	}
	s, err := Current(ctx, l)
	if err != nil {
		return s, err
	}
	if s.Running {
		if trial != nil && *trial != s.ExperimentalRedaction {
			return s, errors.New("running instance has a different trial choice; stop it before changing startup options")
		}
		return s, nil
	}
	selected, err := preference(l)
	if err != nil {
		return s, err
	}
	if trial != nil {
		selected = *trial
	}
	if err := safePath(l.State); err != nil {
		return s, err
	}
	if err := safePath(l.Logs); err != nil {
		return s, err
	}
	if err := os.MkdirAll(l.Logs, 0o700); err != nil {
		return s, err
	}
	exe, err := os.Executable()
	if err != nil {
		return s, err
	}
	args := []string{"serve"}
	if selected {
		args = append(args, "--experimental-redaction")
	}
	if err := safePath(filepath.Join(l.Logs, "service.log")); err != nil {
		return s, err
	}
	log, err := os.OpenFile(filepath.Join(l.Logs, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return s, err
	}
	defer log.Close()
	cmd := proc.Command(exe, args...)
	hide(cmd)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Dir = filepath.Dir(exe)
	if err = cmd.Start(); err != nil {
		return s, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stamp, actual, identityErr := processIdentity(cmd.Process.Pid)
	owned := Record{PID: cmd.Process.Pid, Executable: actual, ProcessStamp: stamp}
	ready := false
	defer func() {
		if !ready {
			if err := cleanupStart(l, owned, cmd.Process, done); err != nil {
				failure = errors.Join(failure, err)
			}
		}
	}()
	if identityErr != nil {
		return s, fmt.Errorf("started process identity unavailable: %w", identityErr)
	}
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		current, e := Current(ctx, l)
		if e == nil && current.Running {
			if !samePath(current.Executable, exe) {
				return current, errors.New("another executable owns this layout; no instance adopted")
			}
			if current.PID != owned.PID {
				if trial != nil && current.ExperimentalRedaction != selected {
					return current, errors.New("another start won with a different trial choice; no instance was reconfigured")
				}
				return current, nil // Deferred cleanup joins our losing startup only.
			}
			if e = SaveOptions(l, current.ExperimentalRedaction); e != nil {
				return current, e
			}
			ready = true
			return current, nil
		}
		select {
		case e := <-done:
			ready = true // Wait already reaped the failed child; no cleanup remains.
			if e == nil {
				e = errors.New("service exited before readiness")
			}
			return s, fmt.Errorf("start failed (%v); inspect %s for the listener or host synchronization error", e, log.Name())
		case <-ctx.Done():
			return s, fmt.Errorf("start readiness timed out; inspect status and %s: %w", log.Name(), ctx.Err())
		case <-tick.C:
		}
	}
}

// cleanupStart first requests authenticated graceful shutdown, then uses only
// the exact child process handle if that cannot complete within its budget.
func cleanupStart(l dirs.Layout, owned Record, child *os.Process, done <-chan error) error {
	return cleanupStartBudget(l, owned, child, done, 12*time.Second)
}
func cleanupStartBudget(l dirs.Layout, owned Record, child *os.Process, done <-chan error, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
graceful:
	for {
		if r, err := ReadRecord(l); err == nil && sameProcess(r, owned) {
			_, _ = request(ctx, l, r, "/stop")
		}
		select {
		case <-done:
			return cleanupExitedStart(l, owned)
		case <-ctx.Done():
			break graceful
		case <-tick.C:
		}
	}
	if Verify(owned) == nil {
		if err := child.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("cannot clean up owned startup process: %w", err)
		}
	}
	select {
	case <-done:
		return cleanupExitedStart(l, owned)
	case <-time.After(3 * time.Second):
		return errors.New("owned startup process cleanup not confirmed; inspect service status")
	}
}
func sameProcess(a, b Record) bool {
	return a.PID == b.PID && a.ProcessStamp == b.ProcessStamp && samePath(a.Executable, b.Executable)
}

// Called only after Wait confirms this exact startup child has exited.
// Unknown locks, changed identities and unrelated files are never reclaimed.
func cleanupExitedStart(l dirs.Layout, owned Record) error {
	r, err := ReadRecord(l)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameProcess(r, owned) {
		return nil
	}
	for _, p := range []string{filepath.Join(l.ServiceLock(), "management-token"), RecordFile(l)} {
		if err := safePath(p); err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(l.ServiceLock()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("owned startup exited but lock contains unknown files; inspect before manual recovery: %w", err)
	}
	return nil
}
func Restart(ctx context.Context, l dirs.Layout) (Status, error) {
	s, err := Current(ctx, l)
	if err != nil {
		return s, err
	}
	if s.Running {
		if err = SaveOptions(l, s.ExperimentalRedaction); err != nil {
			return s, err
		}
		if _, err = Stop(ctx, l); err != nil {
			return s, err
		}
	}
	return Start(ctx, l, nil)
}
func SpawnRestart(l dirs.Layout) (<-chan error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := safePath(l.Logs); err != nil {
		return nil, err
	}
	if err := safePath(filepath.Join(l.Logs, "service-restart.log")); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(l.Logs, "service-restart.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := proc.Command(exe, "service", "restart")
	hide(cmd)
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); log.Close() }()
	return done, nil
}
func entryName(l dirs.Layout) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(l.State))))
	return "API-router-" + hex.EncodeToString(sum[:8])
}
