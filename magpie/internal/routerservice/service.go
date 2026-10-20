// Package routerservice owns API-router's local HTTP lifetime. It deliberately
// does not start Magpie's CLI, gateway lifecycle, plugins or background jobs.
package routerservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routercontrol"
	"github.com/yetone/magpie/internal/routerhost"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/steady"
)

// Info reports the HTTP service separately from model readiness.
type Info struct {
	Service               string                  `json:"service"`
	Version               string                  `json:"version"`
	InstanceID            string                  `json:"instanceId"`
	Address               string                  `json:"address"`
	PreviousAddress       string                  `json:"previousAddress,omitempty"`
	StartedAt             time.Time               `json:"startedAt"`
	ModelAPI              bool                    `json:"modelAPIReady"`
	Runtime               bool                    `json:"runtimeReady"`
	Running               bool                    `json:"running"`
	PID                   int                     `json:"pid"`
	Executable            string                  `json:"executable"`
	ProcessStamp          string                  `json:"processStamp"`
	ExperimentalRedaction bool                    `json:"experimentalRedaction"`
	Autostart             routercontrol.Autostart `json:"autostart"`
	StartCommand          string                  `json:"startCommand"`
}

func TokenFile(l dirs.Layout) string { return filepath.Join(l.ServiceLock(), "management-token") }

// Address accepts literal loopback IPs only: no DNS, wildcard, LAN or port zero.
// This check also applies to existing configs, independent of unknown settings.
func Address(d routerconfig.Document) (string, error) {
	var listen struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(d["listen"], &listen); err != nil {
		return "", fmt.Errorf("invalid listen: %w", err)
	}
	host, port, err := net.SplitHostPort(listen.Address)
	if err != nil {
		return "", fmt.Errorf("listen.address must be a loopback IP:port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return "", fmt.Errorf("listen.address must use a literal loopback IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("listen.address port must be between 1 and 65535")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(n)), nil
}

// Run initializes only missing config, acquires the per-user service lock and
// binds before reporting ready. Cancellation drains HTTP for up to five seconds.
// No model is enabled, downloaded or loaded by service startup.
func Run(ctx context.Context, l dirs.Layout, version string, ready func(Info) error) error {
	return RunWithModelOptions(ctx, l, version, ready, ModelOptions{})
}

// RunWithModelOptions exposes safe model observations without enabling any
// additional lifecycle. Options are immutable for the server lifetime.
func RunWithModelOptions(ctx context.Context, l dirs.Layout, version string, ready func(Info) error, options ModelOptions) error {
	ctx, stopService := context.WithCancel(ctx)
	defer stopService()
	if err := ctx.Err(); err != nil {
		return err
	}
	store := routerconfig.New(l)
	d, err := store.LoadContext(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		d, err = store.InitializeContext(ctx)
	}
	if err != nil {
		return err
	}
	address, err := Address(d)
	if err != nil {
		return err
	}
	if err := l.Ensure(); err != nil {
		return err
	}
	if err := os.Mkdir(l.ServiceLock(), 0o700); err != nil {
		return fmt.Errorf("service lock (running instance or leftover lock; check before removing %s): %w", l.ServiceLock(), err)
	}
	defer os.Remove(l.ServiceLock())
	if err := routerhost.RecoverPortSync(ctx, filepath.Join(l.State, "host-config")); err != nil {
		return fmt.Errorf("pending host port synchronization: %w", err)
	}
	originalAddress := address
	ln, err := listenAvailable(address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", address, err)
	}
	defer ln.Close()
	address = ln.Addr().String()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(secret[:])
	if err := steady.WriteFile(TokenFile(l), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(TokenFile(l))
	// Record ownership before optional startup work, including HTTP readiness.
	var instanceID string
	if err := json.Unmarshal(d["instanceId"], &instanceID); err != nil {
		return err
	}
	record, err := routercontrol.LaunchRecord(l, version, instanceID, address, options.ExperimentalRedaction, originalAddress)
	if err != nil {
		return err
	}
	defer os.Remove(routercontrol.RecordFile(l))
	if options.LocalRuntime == nil {
		options.LocalRuntime, err = newLocalModelsWithTrial(ctx, l, store, options.ExperimentalRedaction, options.LocalProcessStarted)
		if err != nil {
			return fmt.Errorf("local runtime configuration: %w", err)
		}
	}
	if options.LocalRuntime != nil && options.LocalRuntime.Manager != nil && options.LocalObserver != nil {
		options.LocalRuntime.Observer = options.LocalObserver
		options.LocalRuntime.Manager.SetLocalObserver(redactionOnlyObserver{target: options.LocalObserver})
	}
	if options.LocalModelsReady != nil {
		options.LocalModelsReady(options.LocalRuntime)
	}
	// The service owns one redaction adapter for the same Manager used by W4.
	// It is created even while capability evidence is unknown so the management
	// surface can report the real integration state without enabling requests.
	var productionRedactor *runtimeRedactor
	if options.LocalRuntime != nil && options.LocalRuntime.Manager != nil {
		productionRedactor = newRuntimeRedactor(options.LocalRuntime, options.ExperimentalRedaction)
		if options.Routing == nil {
			options.Routing = &RoutingOptions{
				Judge:         RuntimeJudge{Manager: options.LocalRuntime.Manager, PromptVersion: defaultPromptVersion},
				LocalInvoker:  options.LocalRuntime.Manager,
				Redactor:      productionRedactor,
				PromptVersion: defaultPromptVersion,
			}
		} else if options.Routing.Redactor == nil {
			options.Routing.Redactor = productionRedactor
		}
	}
	if productionRedactor != nil {
		defer productionRedactor.close()
	}
	var ownedLedger *routerledger.Store
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = options.LocalRuntime.Manager.Close(stop)
		if options.LocalCalls != nil {
			options.LocalCalls.Close()
		}
		if ownedLedger != nil {
			_ = ownedLedger.Close()
		}
	}()
	idleCtx, idleCancel := context.WithCancel(context.Background())
	idleDone := make(chan struct{})
	go localIdleLoop(idleCtx, options.LocalRuntime, idleDone)
	defer func() { idleCancel(); <-idleDone }()
	// The service lock excludes concurrent ledger owners. An incompatible or
	// damaged ledger remains untouched and is exposed as unavailable over the
	// management API; cloud execution can still serve without inventing history.
	ledger, ledgerErr := routerledger.Open(filepath.Join(l.State, "requests.sqlite"), routerledger.Options{})
	options.Ledger = nil
	if ledgerErr == nil {
		options.Ledger = ledger
		ownedLedger = ledger
		options.LocalCalls = routerobserve.NewCalls(ledger, 256)
		// C owns persistence; wire the single consumer only after the ledger is
		// open. A's manager observer is redaction-only because judgment and
		// execution are emitted through the request-bound observer in models.go.
		options.LocalObserver = options.LocalCalls
		if options.LocalRuntime != nil {
			options.LocalRuntime.Observer = options.LocalCalls
			if options.LocalRuntime.Manager != nil {
				options.LocalRuntime.Manager.SetLocalObserver(redactionOnlyObserver{target: options.LocalCalls})
			}
		}
	} else {
		options.LedgerErrorCode = "ledger_unavailable"
		if errors.Is(ledgerErr, routerledger.ErrCorrupt) {
			options.LedgerErrorCode = "ledger_corrupt"
		}
		if errors.Is(ledgerErr, routerledger.ErrSchema) {
			options.LedgerErrorCode = "ledger_schema_incompatible"
		}
	}
	info := Info{Service: "api-router", Version: version, InstanceID: instanceID, Address: address, StartedAt: time.Now().UTC(), ModelAPI: true}
	info.PreviousAddress = record.PreviousAddress
	info.PID, info.Executable, info.ProcessStamp = record.PID, record.Executable, record.ProcessStamp
	info.StartedAt = record.StartedAt
	info.Running, info.ExperimentalRedaction = true, record.ExperimentalRedaction
	info.StartCommand = routercontrol.StartCommand(l, record.Executable)
	options.serviceControl = &serviceController{layout: l, stop: stopService}
	options.HostStateDir = filepath.Join(l.State, "host-config")
	srv := &http.Server{
		Handler: handlerWithModelOptions(info, token, store, options), ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	// The watcher always joins, including bind/report/Serve failures. No goroutine
	// is left waiting for a context that belongs to a caller that already returned.
	done := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				_ = srv.Close()
			}
		case <-done:
		}
	}()
	defer func() { close(done); <-drained }()
	if err := synchronizeStartupPort(ctx, store, options.HostStateDir, d, originalAddress, address); err != nil {
		return err
	}
	if ready != nil {
		if err := ready(info); err != nil {
			return err
		}
	}
	if err := routercontrol.SaveOptions(l, options.ExperimentalRedaction); err != nil {
		return fmt.Errorf("save explicit startup choice: %w", err)
	}
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Healthcheck performs one explicit local request, bypasses proxy environment
// variables and refuses redirects. It never initializes or repairs config.
func Healthcheck(ctx context.Context, l dirs.Layout) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d, err := routerconfig.New(l).LoadContext(ctx)
	if err != nil {
		return err
	}
	address, err := Address(d)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	var health struct{ Service, Status string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4096)).Decode(&health) != nil || health.Service != "api-router" || health.Status != "ok" {
		return errors.New("healthcheck: endpoint is not a healthy api-router service")
	}
	return nil
}
