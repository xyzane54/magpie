package routerservice

import (
	"context"
	"errors"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerrequest"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func appModelAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, token string, options ModelOptions) {
	acceptedAt := time.Now()
	options.store = store
	options.managementToken = token
	ctx, options := snapshotRuntimeSettings(r.Context(), options)
	r = r.WithContext(ctx)
	r, budgetCancel := routerrequest.WithBudget(r, options.Budget)
	defer budgetCancel()
	var err error
	r, err = routerrequest.WithRequestID(r)
	if err != nil {
		modelFailure(w, "", err)
		return
	}
	w.Header().Set("X-Request-ID", routerrequest.RequestID(r.Context()))
	// Infer the error envelope even when authentication or canonical-path checks
	// fail. This never selects an application or a target.
	protocol := "chat"
	if strings.Contains(r.URL.Path, "/messages") {
		protocol = "anthropic"
	}
	appID, sub, err := routerrequest.ParseAppPath(r)
	if err != nil {
		modelFailure(w, protocol, err)
		return
	}
	if sub == "messages" || sub == "messages/count_tokens" {
		protocol = "anthropic"
	} else if sub == "responses" {
		protocol = "responses"
	} else {
		protocol = "chat"
	}
	method := http.MethodPost
	if sub == "" || sub == "models" {
		method = http.MethodGet
	}
	if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
		allow := method
		if method == http.MethodGet {
			allow = "GET, HEAD"
		}
		w.Header().Set("Allow", allow)
		modelFailure(w, protocol, &routercloud.Error{Code: "method_not_allowed", Message: "method not allowed", Status: 405})
		return
	}
	var rejectedProfile *routerconfig.AppProfile
	if options.Ledger != nil && method == http.MethodPost {
		rejectedProfile = preparationSnapshot(r.Context(), store, appID)
	}
	prepared, cancel, err := routerrequest.Prepare(r, store, token, options.Budget)
	if err != nil {
		observePreparationFailure(w, r, rejectedProfile, sub, acceptedAt, options, err)
		modelFailure(w, protocol, err)
		return
	}
	defer cancel()
	state, _ := routerrequest.FromContext(prepared.Context())
	target := routercloud.Target{Provider: state.Target.Provider, Model: state.Target.Profile.Cloud.Model,
		Authorization: state.Target.Authorization, RedactionRequired: state.Target.Profile.RedactionEnabled,
		ResponseCapabilities: state.Target.Profile.Cloud.Capabilities}
	if method == http.MethodGet {
		models, seen, raw := []map[string]any{}, map[string]bool{}, routercloud.Models(target)
		p := state.Target.Profile
		if len(p.Models) != 1 || p.Cloud.ProviderID == "" || p.Models[0] != p.Cloud.ProviderID+"/"+p.Cloud.Model {
			raw = append(raw, p.Models...)
		}
		unlisted := map[string]bool{}
		var cfg routerconfig.Config
		var groups []routerconfig.Group
		if store != nil {
			if loaded, err := store.LoadConfigContext(prepared.Context()); err == nil {
				cfg = loaded
				groups = effectiveGroups(cfg, options.LocalRuntime)
				for _, g := range groups {
					if g.Disabled || g.Hidden || !g.Unlists {
						continue
					}
					for _, m := range expandActiveGroupMembers(g, groups, nil) {
						bare := bareMember(m)
						unlisted[bare] = true
						if p.Cloud.ProviderID != "" && strings.HasPrefix(bare, p.Cloud.ProviderID+"/") {
							unlisted[strings.TrimPrefix(bare, p.Cloud.ProviderID+"/")] = true
						}
					}
				}
			}
		}
		for _, id := range raw {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] || unlisted[id] {
				continue
			}
			entry := map[string]any{"id": id, "object": "model", "owned_by": "profile"}
			if gid, isGrp := routerconfig.GroupIDOf(id); isGrp && len(groups) > 0 {
				g, ok := findRuntimeGroup(cfg.Groups, groups, gid)
				if !ok || g.Disabled || g.Hidden {
					continue
				}
				if ctxWin := resolveGroupContextWindow(g, groups, options.LocalRuntime); ctxWin > 0 {
					entry["context_window"] = ctxWin
				}
			}
			seen[id] = true
			models = append(models, entry)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodHead {
			providerJSON(w, 200, map[string]any{"object": "list", "data": models})
		}
		return
	}
	result := routercloud.Result{Operation: operationFor(sub)}
	observation := beginRequestObservation(w, r, state.Target.Profile, sub, acceptedAt, options)
	prepared = prepared.WithContext(routerobserve.WithLocalObserver(prepared.Context(), options.LocalCalls, state.ID, false, observation))
	finalCode := "router_error"
	defer func() {
		if observation != nil {
			if state.Target.Profile.Cloud.ProviderID != "" || state.Target.Profile.Cloud.Model != "" {
				observation.SetCloudTarget(state.Target.Profile.Cloud.ProviderID, state.Target.Profile.Cloud.Model, nil)
			}
			var finalErr error
			if finalCode != "" {
				finalErr = &routercloud.Error{Code: finalCode}
			}
			_ = observation.Finish(result, finalErr)
		}
		stage := "completed"
		if finalCode != "" {
			stage = "failed"
		}
		if finalCode == "request_canceled" {
			stage = "canceled"
		}
		state.Stage = stage
		if options.Complete != nil {
			options.Complete(ModelCompletion{Purpose: modelPurpose(options), RequestID: state.ID, AppID: state.AppID, Stage: stage, ErrorCode: finalCode, Result: result})
		}
	}()
	fail := func(err error) {
		var cloud *routercloud.Error
		if errors.Is(err, ErrRedactionUnavailable) {
			finalCode = "redaction_unavailable"
			modelFailure(w, protocol, &routercloud.Error{Code: finalCode, Message: "required redaction is unavailable", Status: http.StatusServiceUnavailable})
			return
		}
		if errors.As(err, &cloud) {
			finalCode = cloud.Code
		} else {
			finalCode = routerrequest.ClassifyError(err).Code
		}
		if !state.OutputCommitted && !result.OutputCommitted {
			modelFailure(w, protocol, err)
		}
	}
	stripAllowedAnthropicBeta(r.Header, appID, protocol)
	if err := validateModelHeaders(r.Header, protocol); err != nil {
		fail(err)
		return
	}
	contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "application/json" {
		fail(&routercloud.Error{Code: "unsupported_media_type", Message: "application/json required", Status: 415})
		return
	}
	// Bound socket body reads by the same operation deadline. Cancellation of
	// a real client also interrupts its body; no detached reader goroutine remains.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(state.Deadline)
	body, err := io.ReadAll(http.MaxBytesReader(w, prepared.Body, 8<<20))
	if err == nil {
		_ = controller.SetReadDeadline(time.Time{})
	}
	if err != nil {
		// Do not let net/http drain a stalled unread body while writing the
		// error. Keep the expired read deadline and close this connection.
		w.Header().Set("Connection", "close")
		code, message, status := "invalid_request", "could not read model request", 400
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			code, message, status = "request_too_large", "model request exceeds size limit", 413
		}
		if state.Remaining() == 0 {
			// net/http may cancel the parent on the socket read timeout
			// before the context timer fires. This is still budget expiry.
			fail(context.DeadlineExceeded)
		} else if prepared.Context().Err() != nil {
			fail(prepared.Context().Err())
		} else {
			fail(&routercloud.Error{Code: code, Message: message, Status: status})
		}
		return
	}
	result, err = executePreparedModel(w, prepared, sub, body, observingModelOptions(options, observation))
	if err != nil {
		fail(err)
	} else {
		finalCode = ""
	}
}

func stripAllowedAnthropicBeta(header http.Header, appID, protocol string) {
	if protocol != "anthropic" || header == nil {
		return
	}
	var betaKeys []string
	for name, values := range header {
		if !strings.EqualFold(name, "anthropic-beta") {
			continue
		}
		if len(values) == 0 {
			return
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return
			}
			for _, rawToken := range strings.Split(v, ",") {
				token := strings.TrimSpace(rawToken)
				if token == "" || !isAllowedAnthropicBetaToken(appID, token) {
					return
				}
			}
		}
		betaKeys = append(betaKeys, name)
	}
	for _, name := range betaKeys {
		delete(header, name)
	}
}
