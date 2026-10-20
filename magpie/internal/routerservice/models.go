package routerservice

import (
	"context"
	"errors"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
	"net/http"
	"time"
)

// ModelOptions configures the shared operation budget and safe synchronous
// observation hooks. Callbacks must return promptly and synchronize shared data.
// Cloud Observe/Complete expose no target, credential, URL or prompt. The local
// runtime hooks below are trusted in-process diagnostics, never HTTP options.
type ModelOptions struct {
	serviceControl *serviceController
	// ExperimentalRedaction is a local startup opt-in, never request input or
	// formal capability evidence. It permits only the bound fixed-model trial policy.
	ExperimentalRedaction bool
	// HostStateDir is the service-owned private recovery directory. It is never
	// accepted from an HTTP caller and is unrelated to model observations.
	HostStateDir string
	LocalRuntime *LocalModels
	// Routing is optional so a service with no enabled local model retains the
	// existing direct cloud path. It is never populated from an HTTP request.
	Routing *RoutingOptions
	// Trusted local diagnostics observe the product-created manager/process.
	// Neither callback is exposed to configuration or HTTP callers.
	LocalModelsReady    func(*LocalModels)
	LocalProcessStarted func(routerprocess.Process, routerprocess.Spec)
	purpose             string
	Budget              time.Duration
	Ledger              *routerledger.Store
	LedgerErrorCode     string
	LocalCalls          *routerobserve.Calls
	Observe             func(ModelEvent)
	Complete            func(ModelCompletion)
	LocalObserver       routerruntime.LocalCallObserver
	store               *routerconfig.Store
	managementToken     string
}
type ModelEvent struct {
	Purpose   string
	RequestID string
	AppID     string
	Event     routercloud.Event
}
type ModelCompletion struct {
	Purpose   string
	RequestID string
	AppID     string
	Stage     string
	ErrorCode string
	Result    routercloud.Result
}

// snapshotRuntimeSettings binds both the total deadline and later Manager calls
// to the settings seen at admission. Explicit shorter operation budgets and
// parent deadlines continue to win; fallback never starts another total budget.
func snapshotRuntimeSettings(ctx context.Context, options ModelOptions) (context.Context, ModelOptions) {
	settings, ok := routerruntime.RuntimeSettingsFrom(ctx)
	if !ok {
		var manager *routerruntime.Manager
		if options.LocalRuntime != nil {
			manager = options.LocalRuntime.Manager
		}
		if manager == nil && options.Routing != nil {
			manager, _ = options.Routing.LocalInvoker.(*routerruntime.Manager)
			if manager == nil {
				switch judge := options.Routing.Judge.(type) {
				case RuntimeJudge:
					manager = judge.Manager
				case *RuntimeJudge:
					if judge != nil {
						manager = judge.Manager
					}
				}
			}
		}
		if manager != nil {
			settings = manager.Settings()
		}
		ctx = routerruntime.WithRuntimeSettings(ctx, settings)
	}
	if settings != (routerconfig.RuntimeSettings{}) && modelPurpose(options) == "generation" {
		budget := time.Duration(settings.Effective().RequestSeconds) * time.Second
		if options.Budget <= 0 || options.Budget > budget {
			options.Budget = budget
		}
	}
	return ctx, options
}

// modelPurpose distinguishes user inference from explicit management checks. For a
// connection_check, OutputCommitted describes the bounded private SSE capture,
// not commitment of the management endpoint's final JSON response.
func modelPurpose(options ModelOptions) string {
	if options.purpose != "" {
		return options.purpose
	}
	return "generation"
}

func modelFailure(w http.ResponseWriter, protocol string, err error) {
	var cloud *routercloud.Error
	if !errors.As(err, &cloud) {
		safe := routerrequest.ClassifyError(err)
		cloud = &routercloud.Error{Code: safe.Code, Message: safe.Message, Status: safe.Status}
	}
	routercloud.WriteError(w, protocol, cloud)
}
