package routerservice

import (
	"context"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerledger"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerrequest"
)

// Compose the ledger with the existing safe hooks without coupling the shared
// executor to admission or adding another retry/response owner.
func observingModelOptions(options ModelOptions, observation *routerobserve.Request) ModelOptions {
	previous := options.Observe
	options.Observe = func(event ModelEvent) {
		if observation != nil {
			observation.Observe(event.Event)
		}
		if previous != nil {
			previous(event)
		}
	}
	return options
}

// Bind only a validated Profile snapshot, never Target, URLs or incoming headers.
// Storage faults do not change an already committed model response. The ledger
// reports its own availability to management readers; this header covers Start.
func beginRequestObservation(w http.ResponseWriter, r *http.Request, profile routerconfig.AppProfile, sub string, acceptedAt time.Time, options ModelOptions) *routerobserve.Request {
	// Only the trusted server purpose classifies an explicit management check.
	// Client headers never turn a business generation into a diagnostic.
	if modelPurpose(options) != "generation" {
		w.Header().Set("X-Router-Ledger", "diagnostic")
		return nil
	}
	if options.Ledger == nil {
		w.Header().Set("X-Router-Ledger", "unavailable")
		return nil
	}
	var price *routerledger.ReferencePrice
	if p := profile.ReferencePrice; p != nil {
		price = &routerledger.ReferencePrice{Currency: p.Currency, Unit: p.Unit, Input: p.Input, Output: p.Output, Version: p.Version, Source: p.Source}
		if !routerledger.ReferencePriceSafe(price) {
			price = nil
			options.Ledger.ReportObservationFailure()
		}
	}
	observation, err := routerobserve.Begin(options.Ledger, routerledger.Metadata{
		AcceptedAt: acceptedAt,
		RequestID:  routerrequest.RequestID(r.Context()), AppID: profile.ID, AppName: profile.Name,
		Operation: operationFor(sub), Path: "/v1/" + sub,
		ProviderID: profile.Cloud.ProviderID, Model: profile.Cloud.Model,
		ReferencePrice: price,
	})
	if err != nil {
		w.Header().Set("X-Router-Ledger", "unavailable")
		return nil
	}
	w.Header().Set("X-Router-Ledger", "started")
	return observation
}

// The rejection snapshot precedes Prepare. Do not re-read a later Profile after
// rejection: a concurrent edit must not change that request's attribution.
func preparationSnapshot(ctx context.Context, store *routerconfig.Store, appID string) *routerconfig.AppProfile {
	if store == nil {
		return nil
	}
	apps, loadErr := store.AppsContext(ctx)
	if loadErr != nil {
		return nil
	}
	for _, profile := range apps {
		if profile.ID != appID {
			continue
		}
		return &profile
	}
	return nil
}

// Admission excludes non-model operations, malformed identities and unknown
// apps. A registered app's preparation rejection is a zero-attempt request.
func observePreparationFailure(w http.ResponseWriter, r *http.Request, profile *routerconfig.AppProfile, sub string, acceptedAt time.Time, options ModelOptions, err error) {
	if profile == nil {
		return
	}
	if observation := beginRequestObservation(w, r, *profile, sub, acceptedAt, options); observation != nil {
		_ = observation.Reject(routerrequest.ClassifyError(err))
	}
}
