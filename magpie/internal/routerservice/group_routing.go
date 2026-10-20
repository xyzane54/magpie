package routerservice

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
	"github.com/yetone/magpie/internal/routerruntime"
)

var (
	groupWeightSeq atomic.Uint64
	patienceUnit   = time.Second
)

func resolveBoundTarget(selectorModel string, target routerconfig.AppTarget, options ModelOptions) (string, bool) {
	req, p := strings.TrimSpace(selectorModel), target.Profile
	if options.store == nil || (req == "" && len(p.Models) == 0) {
		return "", false
	}
	if strings.HasPrefix(req, routerconfig.GroupPrefix) {
		return req, true
	}
	if req == "" {
		req = p.Models[0]
	}
	matched := ""
	for _, m := range p.Models {
		gid, isGrp := routerconfig.GroupIDOf(m)
		if m == req {
			matched = m
			break
		}
		if matched == "" && ((isGrp && routerconfig.GroupSlug(req) == gid) || strings.HasSuffix(m, "/"+req)) {
			matched = m
		}
	}
	if matched == "" && req != "" {
		if cfg, err := options.store.LoadConfigContext(context.Background()); err == nil {
			groups := effectiveGroups(cfg, options.LocalRuntime)
			reqSlug := routerconfig.GroupSlug(req)
			for _, m := range p.Models {
				gid, isGrp := routerconfig.GroupIDOf(m)
				if !isGrp {
					continue
				}
				if g, ok := findRuntimeGroup(cfg.Groups, groups, gid); ok && !g.Hidden {
					if (g.Family != "" && (strings.EqualFold(g.Family, req) || routerconfig.GroupSlug(g.Family) == reqSlug)) ||
						(g.Name != "" && (strings.EqualFold(g.Name, req) || routerconfig.GroupSlug(g.Name) == reqSlug)) {
						matched = m
						break
					}
				}
			}
		}
	}
	def := p.Cloud.ProviderID + "/" + p.Cloud.Model
	return matched, matched != "" && (strings.HasPrefix(matched, routerconfig.GroupPrefix) || strings.HasPrefix(matched, "local/") || target.Provider.Config.ID == "" || p.Cloud.ProviderID == "" || matched != def)
}

type groupExecOptions struct {
	group       *routerconfig.Group
	matchedRule *routerconfig.Rule
	affinityKey string
}

func executeBoundTarget(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, targetRef string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter) (routercloud.Result, error) {
	groupID, isGroup := routerconfig.GroupIDOf(targetRef)
	if !isGroup {
		return dispatchCandidates(w, prepared, protocol, sub, body, []string{targetRef}, options, emit, writer)
	}
	op := operationFor(sub)
	cfg, err := options.store.LoadConfigContext(prepared.Context())
	if err != nil {
		return routercloud.Result{Operation: op}, err
	}
	groups := effectiveGroups(cfg, options.LocalRuntime)
	g, found := findRuntimeGroup(cfg.Groups, groups, groupID)
	if !found || g.Hidden {
		return routercloud.Result{Operation: op}, &routercloud.Error{Code: "target_missing", Message: "configured routing group does not exist", Status: http.StatusConflict}
	}
	if g.Disabled {
		return routercloud.Result{Operation: op}, &routercloud.Error{Code: "group_disabled", Message: "routing group is disabled", Status: http.StatusConflict}
	}
	baseMembers := expandActiveGroupMembers(g, groups, body)
	if len(baseMembers) == 0 {
		return routercloud.Result{Operation: op}, &routercloud.Error{Code: "target_missing", Message: "routing group has no active members", Status: http.StatusConflict}
	}
	ruleMembers, matchedRule := evaluateGroupRules(g, groups, protocol, body, baseMembers)
	ruleHasTarget := matchedRule != nil && (strings.TrimSpace(matchedRule.Use) != "" || strings.TrimSpace(matchedRule.Target) != "")

	strategy := strings.ToLower(strings.TrimSpace(g.Routing))
	gOpts := &groupExecOptions{group: &g, matchedRule: matchedRule}

	if !ruleHasTarget && isSmartSplitStrategy(strategy) {
		return executeSmartSplitGroupWithOpts(w, prepared, protocol, sub, body, ruleMembers, options, emit, writer, gOpts)
	}
	candidates := orderGroupCandidates(g, ruleMembers, ruleHasTarget)
	if !ruleHasTarget || strings.EqualFold(g.Affinity, routerconfig.AffinityPin) || strings.EqualFold(g.Affinity, routerconfig.AffinityStrict) {
		candidates, gOpts.affinityKey, _ = applyGroupAffinity(g, prepared, protocol, body, candidates)
	} else {
		gOpts.affinityKey, _ = deriveGroupAffinityKey(g.ID, prepared, protocol, body)
	}
	return dispatchGroupWithPatience(w, prepared, protocol, sub, body, candidates, ruleMembers, ruleHasTarget, options, emit, writer, gOpts)
}

func isSmartSplitStrategy(strategy string) bool {
	switch strategy {
	case "", "smart_split", "smart_hybrid", "smart", "auto":
		return true
	default:
		return false
	}
}

func orderGroupCandidates(g routerconfig.Group, members []string, ruleHasTarget bool) []string {
	if len(members) == 0 {
		return nil
	}
	if ruleHasTarget && len(members) > 1 {
		rest := orderStrategyMembers(g, members[1:])
		return append([]string{members[0]}, rest...)
	}
	return orderStrategyMembers(g, members)
}

func orderStrategyMembers(g routerconfig.Group, members []string) []string {
	if len(members) == 0 {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(g.Routing)) {
	case "manual":
		chosen := members[0]
		if pick := strings.TrimSpace(g.Pick); pick != "" {
			pickBare := bareMember(pick)
			if idx := slices.IndexFunc(members, func(m string) bool { return m == pick || bareMember(m) == pickBare }); idx >= 0 {
				chosen = members[idx]
			}
		}
		return []string{chosen}
	case "weight", "weighted":
		return applySinkOrder(orderWeightedMembers(members, g.Weights), g.Sink)
	case "rotate", "round_robin", "round-robin":
		return orderRotateMembers(g.ID, members, g.Sink)
	case "usage", "least", "least_used", "least-used":
		return orderLeastUsedMembers(members, g.Sink)
	case "pace", "drain_reset", "drain-reset":
		return orderPaceMembers(members, g.Weights, g.Sink)
	default:
		return applySinkOrder(members, g.Sink)
	}
}

func findRuntimeGroup(saved, effective []routerconfig.Group, id string) (routerconfig.Group, bool) {
	match := func(g routerconfig.Group) bool {
		return strings.EqualFold(g.ID, id) || routerconfig.GroupSlug(g.ID) == routerconfig.GroupSlug(id)
	}
	if i := slices.IndexFunc(saved, func(g routerconfig.Group) bool { return g.Hidden && match(g) }); i >= 0 {
		return saved[i], true
	}
	if i := slices.IndexFunc(effective, match); i >= 0 {
		return effective[i], true
	}
	return routerconfig.Group{}, false
}

func expandActiveGroupMembers(g routerconfig.Group, all []routerconfig.Group, _ []byte) []string {
	strip := func(item routerconfig.Group) routerconfig.Group {
		isOff := func(m string) bool { return isMemberOff(item.Off, m) }
		item.Members = slices.DeleteFunc(append([]string(nil), item.Members...), isOff)
		item.Matched = slices.DeleteFunc(append([]string(nil), item.Matched...), isOff)
		return item
	}
	clean := make([]routerconfig.Group, len(all))
	for i, item := range all {
		clean[i] = strip(item)
	}
	return routerconfig.ExpandGroupMembers(strip(g), clean)
}

func orderWeightedMembers(members []string, weights map[string]int) []string {
	if len(members) <= 1 {
		return members
	}
	w, div, total := make([]int, len(members)), 0, 0
	for i, m := range members {
		w[i] = lookupMemberWeight(weights, m)
		for a, b := div, w[i]; b != 0; div = a {
			a, b = b, a%b
		}
	}
	for i := range w {
		w[i] /= max(div, 1)
		total += w[i]
	}
	pick, idx := int(groupWeightSeq.Add(1)-1)%total, 0
	for i, weight := range w {
		if pick < weight {
			idx = i
			break
		}
		pick -= weight
	}
	return append(append([]string{members[idx]}, members[idx+1:]...), members[:idx]...)
}

func executeSmartSplitGroup(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, members []string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter) (routercloud.Result, error) {
	return executeSmartSplitGroupWithOpts(w, prepared, protocol, sub, body, members, options, emit, writer, nil)
}

func executeSmartSplitGroupWithOpts(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, members []string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter, gOpts *groupExecOptions) (routercloud.Result, error) {
	var locals, clouds []string
	for _, m := range members {
		if strings.HasPrefix(m, "local/") {
			locals = append(locals, m)
		} else {
			clouds = append(clouds, m)
		}
	}
	if gOpts != nil && gOpts.group != nil {
		locals = applySinkOrder(locals, gOpts.group.Sink)
		clouds = applySinkOrder(clouds, gOpts.group.Sink)
		clouds, gOpts.affinityKey, _ = applyGroupAffinity(*gOpts.group, prepared, protocol, body, clouds)
	}
	routing := options.routingOptions()
	if len(locals) == 0 || routing == nil || routing.Judge == nil || operationFor(sub) == "count_tokens" {
		return dispatchGroupWithPatience(w, prepared, protocol, sub, body, clouds, clouds, false, options, emit, writer, gOpts)
	}
	if len(clouds) == 0 {
		return dispatchGroupWithPatience(w, prepared, protocol, sub, body, locals, locals, false, options, emit, writer, gOpts)
	}
	state, _ := routerrequest.FromContext(prepared.Context())
	normReq, err := routerrequest.Normalize(routerrequest.Input{RequestID: state.ID, AppID: state.AppID, Protocol: protocol, Operation: operationFor(sub), SessionID: state.Target.Profile.ID, Model: strings.TrimPrefix(bareMember(locals[0]), "local/"), PromptVersion: judgmentPromptVersion(routing.PromptVersion, true), OriginalBody: body, Context: prepared.Context()})
	if err != nil {
		return routercloud.Result{Operation: operationFor(sub)}, err
	}
	localCtx, cancelLocal := localWorkContext(prepared.Context(), options)
	defer cancelLocal()
	started := time.Now()
	decision, judgeErr := routing.Judge.Judge(localCtx, normReq.Judgment())
	judgeDur := time.Since(started)
	if judgeErr != nil && (errors.Is(judgeErr, routerruntime.ErrDisabled) || prepared.Context().Err() != nil) {
		return routercloud.Result{Operation: operationFor(sub), Decision: string(DecisionCloud), JudgmentDuration: judgeDur}, judgeErr
	}
	if judgeErr == nil && decision == DecisionLocal {
		res, localErr := dispatchGroupWithPatience(w, prepared, protocol, sub, body, locals, locals, false, options, emit, writer, gOpts)
		if localErr != nil && !writer.committed && prepared.Context().Err() == nil {
			res, localErr = dispatchGroupWithPatience(w, prepared, protocol, sub, body, clouds, clouds, false, options, emit, writer, gOpts)
		}
		res.Decision, res.JudgmentDuration = string(DecisionLocal), judgeDur
		return res, localErr
	}
	res, cloudErr := dispatchGroupWithPatience(w, prepared, protocol, sub, body, clouds, clouds, false, options, emit, writer, gOpts)
	res.Decision, res.JudgmentDuration = string(DecisionCloud), judgeDur
	return res, cloudErr
}

func dispatchCandidates(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, candidates []string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter) (routercloud.Result, error) {
	return dispatchGroupWithPatience(w, prepared, protocol, sub, body, candidates, candidates, false, options, emit, writer, nil)
}

func dispatchGroupWithPatience(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, candidates, baseMembers []string, ruleHasTarget bool, options ModelOptions, emit func(routercloud.Event), writer *modelWriter, gOpts *groupExecOptions) (routercloud.Result, error) {
	res, err := dispatchCandidatesOnce(w, prepared, protocol, sub, body, candidates, options, emit, writer, gOpts)
	if err == nil || writer.committed || prepared.Context().Err() != nil || gOpts == nil || gOpts.group == nil || gOpts.group.Patience <= 0 || !isRetriableGroupFailure(err) {
		return res, err
	}
	deadline := time.Now().Add(time.Duration(gOpts.group.Patience) * patienceUnit)
	totalAttempts := res.Attempts
	allDetails := append([]routercloud.Attempt(nil), res.AttemptDetails...)

	for time.Now().Before(deadline) && !writer.committed && prepared.Context().Err() == nil {
		waitDur := min(patiencePollStep, time.Until(deadline))
		if waitDur <= 0 {
			break
		}
		timer := time.NewTimer(waitDur)
		select {
		case <-prepared.Context().Done():
			timer.Stop()
			return res, err
		case <-timer.C:
		}
		if prepared.Context().Err() != nil || writer.committed {
			break
		}
		nextCandidates := candidates
		if len(baseMembers) > 0 && !strings.EqualFold(gOpts.group.Affinity, routerconfig.AffinityPin) && !strings.EqualFold(gOpts.group.Affinity, routerconfig.AffinityStrict) {
			nextCandidates = orderGroupCandidates(*gOpts.group, baseMembers, ruleHasTarget)
		}
		res, err = dispatchCandidatesOnce(w, prepared, protocol, sub, body, nextCandidates, options, emit, writer, gOpts)
		totalAttempts += res.Attempts
		allDetails = append(allDetails, res.AttemptDetails...)
		res.Attempts, res.AttemptDetails = totalAttempts, allDetails
		if err == nil || writer.committed || prepared.Context().Err() != nil || !isRetriableGroupFailure(err) {
			return res, err
		}
	}
	return res, err
}

func dispatchCandidatesOnce(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, candidates []string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter, gOpts *groupExecOptions) (routercloud.Result, error) {
	var res, lastRes routercloud.Result
	var err, lastErr error
	var total int
	var details []routercloud.Attempt
	for _, cand := range candidates {
		candBody := body
		if gOpts != nil && gOpts.group != nil {
			eff, isFast := resolveCandidateEffortAndFast(*gOpts.group, cand, gOpts.matchedRule, protocol, body)
			candBody = rewriteCandidateRequestBody(protocol, body, eff, isFast)
		}
		attemptReq := prepared
		var cancelAttempt context.CancelCauseFunc
		var ftTimer *time.Timer
		prevCommit := writer.commit
		if gOpts != nil && gOpts.group != nil && gOpts.group.FirstToken > 0 {
			var attemptCtx context.Context
			attemptCtx, cancelAttempt = context.WithCancelCause(prepared.Context())
			attemptReq = prepared.WithContext(attemptCtx)
			ftDur := time.Duration(gOpts.group.FirstToken) * firstTokenStep
			ftTimer = time.AfterFunc(ftDur, func() {
				cancelAttempt(errFirstTokenTimeout)
			})
			writer.commit = func() {
				ftTimer.Stop()
				if prevCommit != nil {
					prevCommit()
				}
			}
		}

		if localModel, ok := strings.CutPrefix(bareMember(cand), "local/"); ok {
			res, err = executeLocalCandidate(w, attemptReq, protocol, sub, candBody, localModel, options, writer)
		} else {
			res, err = executeCloudCandidate(w, attemptReq, protocol, sub, candBody, cand, options, emit, writer)
		}

		if ftTimer != nil {
			ftTimer.Stop()
			writer.commit = prevCommit
			if err != nil && errors.Is(context.Cause(attemptReq.Context()), errFirstTokenTimeout) && prepared.Context().Err() == nil {
				err = &routercloud.Error{Code: "cloud_timeout", Message: "first token timeout exceeded", Status: http.StatusGatewayTimeout}
			}
			cancelAttempt(nil)
		}

		now := time.Now()
		if err == nil {
			var usedTok int64
			if res.ReportedUsage != nil {
				if res.ReportedUsage.TotalTokens != nil {
					usedTok = *res.ReportedUsage.TotalTokens
				} else {
					if res.ReportedUsage.InputTokens != nil {
						usedTok += *res.ReportedUsage.InputTokens
					}
					if res.ReportedUsage.OutputTokens != nil {
						usedTok += *res.ReportedUsage.OutputTokens
					}
				}
			}
			recordMemberSuccess(cand, usedTok, now)
			if gOpts != nil && gOpts.group != nil && gOpts.affinityKey != "" {
				recordGroupAffinity(*gOpts.group, gOpts.affinityKey, cand, now)
			}
		} else if isRetriableGroupFailure(err) {
			recordMemberSunk(cand, now)
		}

		total, details = total+res.Attempts, append(details, res.AttemptDetails...)
		res.Attempts, res.AttemptDetails = total, details
		if err == nil || writer.committed || prepared.Context().Err() != nil {
			return res, err
		}
		lastRes, lastErr = res, err
	}
	return lastRes, lastErr
}

func executeLocalCandidate(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, localModel string, options ModelOptions, writer *modelWriter) (routercloud.Result, error) {
	op, routing, promptVer := operationFor(sub), options.routingOptions(), defaultPromptVersion
	res := routercloud.Result{Operation: op, Decision: string(DecisionLocal)}
	if op == "count_tokens" {
		return res, &routercloud.Error{Code: "unsupported_operation", Message: "model operation is not supported", Status: http.StatusNotImplemented}
	}
	var invoker LocalInvoker
	if routing != nil {
		invoker = routing.LocalInvoker
		if routing.PromptVersion != "" {
			promptVer = routing.PromptVersion
		}
	}
	if invoker == nil && options.LocalRuntime != nil && options.LocalRuntime.Manager != nil {
		invoker = options.LocalRuntime.Manager
	}
	if invoker == nil {
		return res, errors.New("local_runtime_unavailable")
	}
	if mgr, ok := invoker.(*routerruntime.Manager); ok {
		if reqErr := mgr.Require(routerruntime.Execution); reqErr != nil {
			return res, reqErr
		}
	}
	state, _ := routerrequest.FromContext(prepared.Context())
	normReq, err := routerrequest.Normalize(routerrequest.Input{RequestID: state.ID, AppID: state.AppID, Protocol: protocol, Operation: op, SessionID: state.Target.Profile.ID, Model: localModel, PromptVersion: promptVer, OriginalBody: body, Context: prepared.Context()})
	if err != nil {
		return res, err
	}
	localBody, err := localExecutionBody(protocol, normReq.ExecutionBody(), localModel)
	if err != nil {
		return res, err
	}
	localCtx, cancel := localWorkContext(prepared.Context(), options)
	defer cancel()
	if err = invoker.Invoke(localCtx, routerruntime.Execution, localBody, func(resp *http.Response) error {
		return writeLocalResponseObserved(w, resp, protocol, body, writer, &res.ReportedUsage)
	}); err == nil {
		state.Stage, state.Destination = "local", "local"
	}
	res.Attempts, res.OutputCommitted, res.Destination = 1, writer.committed, "local"
	return res, err
}

func executeCloudCandidate(w http.ResponseWriter, prepared *http.Request, protocol, sub string, body []byte, member string, options ModelOptions, emit func(routercloud.Event), writer *modelWriter) (routercloud.Result, error) {
	provID, modelID, ok := strings.Cut(member, "/")
	if !ok || provID == "" || modelID == "" {
		return routercloud.Result{Operation: operationFor(sub), Decision: string(DecisionCloud)}, &routercloud.Error{Code: "target_missing", Message: "configured provider does not exist", Status: http.StatusConflict}
	}
	modelID, _, _ = strings.Cut(modelID, ":")
	state, _ := routerrequest.FromContext(prepared.Context())
	appTarget, err := options.store.ResolveAppProviderTargetContext(prepared.Context(), state.Target.Profile, provID, modelID, state.Target.Authorization, "", options.managementToken)
	if err != nil {
		return routercloud.Result{Operation: operationFor(sub), Decision: string(DecisionCloud)}, err
	}
	state.BeginMemberDispatch()
	prev := state.Target
	state.Target = appTarget
	res, err := executeCloudModelSub(w, prepared, protocol, sub, body, options, emit, writer)
	if err != nil && !writer.committed && res.Attempts == 0 {
		state.Target = prev
	}
	res.Decision = string(DecisionCloud)
	return res, err
}
