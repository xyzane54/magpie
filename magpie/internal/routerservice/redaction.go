package routerservice

import (
	"context"
	_ "embed"
	"errors"
	"net/http"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerredact"
)

//go:embed assets/redaction.js
var redactionScript []byte

type redactionIntent struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Enabled  bool   `json:"enabled"`
	Active   bool   `json:"active"`
}

// redactionAPI deliberately has no runtime-ready or enable-override input.
// Capability and readiness are read from the service-owned Manager; clients
// can only edit their saved intent.
func redactionAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, stats func() routerredact.Stats, stateFns ...func() redactionRuntimeState) {
	fail := func(status int, code string) {
		providerJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
	}
	if r.URL.RawQuery != "" {
		fail(400, "invalid_query")
		return
	}
	if store == nil {
		fail(503, "configuration_unavailable")
		return
	}
	capability := routerconfig.RedactionUnavailable
	if len(stateFns) > 0 && stateFns[0] != nil {
		state := stateFns[0]()
		capability = state.Capability
		if !state.RecognitionReady && !state.TrialReady {
			capability = routerconfig.RedactionUnknown
		}
	}
	if r.Method == http.MethodPost {
		var input struct {
			Edits []struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
				Enabled  *bool  `json:"enabled"`
			} `json:"edits"`
		}
		if !providerInput(w, r, &input) {
			return
		}
		edits := make([]routerconfig.RedactionIntentEdit, 0, len(input.Edits))
		for _, edit := range input.Edits {
			if edit.Enabled == nil {
				fail(400, "invalid_redaction_selection")
				return
			}
			edits = append(edits, routerconfig.RedactionIntentEdit{ID: edit.ID, Revision: edit.Revision, Enabled: *edit.Enabled})
		}
		_, err := store.SetRedactionIntentsContext(r.Context(), edits, capability)
		if err != nil {
			switch {
			case errors.Is(err, routerconfig.ErrRedactionUnavailable):
				fail(409, "redaction_unavailable")
			case errors.Is(err, routerconfig.ErrAppConflict):
				fail(409, "profile_conflict")
			case errors.Is(err, routerconfig.ErrAppNotFound):
				fail(404, "app_not_registered")
			case errors.Is(err, context.Canceled):
				fail(499, "request_canceled")
			case errors.Is(err, context.DeadlineExceeded):
				fail(504, "budget_exhausted")
			default:
				_, status := routerconfig.ProviderError(err)
				if status == 400 {
					fail(400, "invalid_redaction_selection")
				} else {
					fail(503, "configuration_unavailable")
				}
			}
			return
		}
	}
	apps, err := store.AppsContext(r.Context())
	if err != nil {
		fail(503, "configuration_unavailable")
		return
	}
	projection := redactionRuntimeState{Capability: routerconfig.RedactionUnavailable, Reason: "recognition_acceptance_pending"}
	if len(stateFns) > 0 && stateFns[0] != nil {
		projection = stateFns[0]()
	}
	intents := make([]redactionIntent, 0, len(apps))
	for _, p := range apps {
		active := false
		active = p.RedactionEnabled && projection.CloudActive
		intents = append(intents, redactionIntent{ID: p.ID, Revision: p.Revision, Enabled: p.RedactionEnabled, Active: active})
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	var statistics any
	if stats != nil {
		v := stats()
		failures := map[string]uint64{}
		for code, count := range v.Failures {
			failures[routerredact.SafeFailureCode(code)] += count
		}
		statistics = map[string]any{"processed": v.Processed, "entities": v.Entities, "cacheHits": v.CacheHits, "blocked": v.Blocked, "durationMs": v.DurationMS, "failures": failures}
	}
	if !projection.Integrated {
		projection.Statistics = stats != nil
	}
	providerJSON(w, 200, map[string]any{
		"schemaVersion": 1, "capability": projection.Capability,
		"reason": projection.Reason, "productionIntegrated": projection.Integrated,
		"modelReady": projection.ModelReady, "recognitionReady": projection.RecognitionReady,
		"experimental": projection.Experimental, "trialReady": projection.TrialReady,
		"budgetMs": projection.BudgetMS, "formalQualityAccepted": projection.RecognitionReady,
		"trialSuite": projection.TrialSuite, "trialConfigVersion": projection.TrialConfig,
		"cloudRedactionActive": projection.CloudActive, "localExecutionRedacted": false, "intents": intents,
		"statsAvailable": projection.Statistics && stats != nil, "statistics": statistics,
	})
}
