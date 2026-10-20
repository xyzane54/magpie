package routerservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerrequest"
)

var (
	firstTokenStep   = time.Second
	patiencePollStep = 250 * time.Millisecond

	errFirstTokenTimeout = errors.New("first token timeout exceeded")
)

type memberRuntimeStats struct {
	sunkAt     time.Time
	usedTokens float64
	lastUsedAt time.Time
	calls      uint64
}

type affinityEntry struct {
	member    string
	updatedAt time.Time
	expiresAt time.Time
}

var (
	groupRuntimeMu   sync.Mutex
	groupMemberStats = map[string]*memberRuntimeStats{}
	groupRotateSeq   = map[string]*atomic.Uint64{}
	groupAffinities  = map[string]affinityEntry{}
)

func resetGroupRuntimeStateForTest() {
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	groupMemberStats = map[string]*memberRuntimeStats{}
	groupRotateSeq = map[string]*atomic.Uint64{}
	groupAffinities = map[string]affinityEntry{}
	groupWeightSeq.Store(0)
}

func bareMember(ref string) string {
	ref = strings.TrimSpace(ref)
	if subID, ok := routerconfig.GroupIDOf(ref); ok {
		return routerconfig.GroupPrefix + subID
	}
	if idx := strings.IndexByte(ref, ':'); idx > 0 {
		return ref[:idx]
	}
	return ref
}

func memberInlineEffort(ref string) string {
	ref = strings.TrimSpace(ref)
	if _, ok := routerconfig.GroupIDOf(ref); ok {
		return ""
	}
	if idx := strings.IndexByte(ref, ':'); idx > 0 && idx+1 < len(ref) {
		return strings.ToLower(strings.TrimSpace(ref[idx+1:]))
	}
	return ""
}

func recordMemberSunk(member string, now time.Time) {
	key := bareMember(member)
	if key == "" {
		return
	}
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	st := groupMemberStats[key]
	if st == nil {
		st = &memberRuntimeStats{}
		groupMemberStats[key] = st
	}
	st.sunkAt = now
}

func recordMemberSuccess(member string, tokens int64, now time.Time) {
	key := bareMember(member)
	if key == "" {
		return
	}
	if tokens <= 0 {
		tokens = 100
	}
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	st := groupMemberStats[key]
	if st == nil {
		st = &memberRuntimeStats{}
		groupMemberStats[key] = st
	}
	st.sunkAt = time.Time{}
	st.usedTokens = decayedTokensLocked(st, now) + float64(tokens)
	st.lastUsedAt = now
	st.calls++
}

func decayedTokensLocked(st *memberRuntimeStats, now time.Time) float64 {
	if st == nil || st.usedTokens <= 0 {
		return 0
	}
	if st.lastUsedAt.IsZero() {
		return st.usedTokens
	}
	dt := now.Sub(st.lastUsedAt).Seconds()
	if dt <= 0 {
		return st.usedTokens
	}
	return st.usedTokens * math.Exp2(-dt/3600.0)
}

func isMemberSunk(member string) bool {
	key := bareMember(member)
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	st := groupMemberStats[key]
	return st != nil && !st.sunkAt.IsZero()
}

func applySinkOrder(members []string, sinkEnabled bool) []string {
	if !sinkEnabled || len(members) <= 1 {
		return members
	}
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	var healthy, sunk []string
	sunkTime := make(map[string]time.Time, len(members))
	for _, m := range members {
		st := groupMemberStats[bareMember(m)]
		if st != nil && !st.sunkAt.IsZero() {
			sunk = append(sunk, m)
			sunkTime[m] = st.sunkAt
		} else {
			healthy = append(healthy, m)
		}
	}
	if len(sunk) == 0 {
		return members
	}
	slices.SortStableFunc(sunk, func(a, b string) int {
		ta, tb := sunkTime[a], sunkTime[b]
		if ta.Before(tb) {
			return -1
		}
		if ta.After(tb) {
			return 1
		}
		return 0
	})
	return append(healthy, sunk...)
}

func orderRotateMembers(groupID string, members []string, sinkEnabled bool) []string {
	if len(members) <= 1 {
		return members
	}
	groupRuntimeMu.Lock()
	seq := groupRotateSeq[groupID]
	if seq == nil {
		seq = &atomic.Uint64{}
		groupRotateSeq[groupID] = seq
	}
	groupRuntimeMu.Unlock()

	idx := int((seq.Add(1) - 1) % uint64(len(members)))
	rotated := append(append([]string(nil), members[idx:]...), members[:idx]...)
	return applySinkOrder(rotated, sinkEnabled)
}

func orderLeastUsedMembers(members []string, sinkEnabled bool) []string {
	if len(members) <= 1 {
		return members
	}
	now := time.Now()
	groupRuntimeMu.Lock()
	usage := make(map[string]float64, len(members))
	calls := make(map[string]uint64, len(members))
	for _, m := range members {
		key := bareMember(m)
		st := groupMemberStats[key]
		usage[m] = decayedTokensLocked(st, now)
		if st != nil {
			calls[m] = st.calls
		}
	}
	groupRuntimeMu.Unlock()

	ordered := append([]string(nil), members...)
	slices.SortStableFunc(ordered, func(a, b string) int {
		ua, ub := usage[a], usage[b]
		if math.Abs(ua-ub) > 1e-6 {
			if ua < ub {
				return -1
			}
			return 1
		}
		ca, cb := calls[a], calls[b]
		if ca < cb {
			return -1
		}
		if ca > cb {
			return 1
		}
		return 0
	})
	return applySinkOrder(ordered, sinkEnabled)
}

func orderPaceMembers(members []string, weights map[string]int, sinkEnabled bool) []string {
	if len(members) <= 1 {
		return members
	}
	now := time.Now()
	groupRuntimeMu.Lock()
	paceScore := make(map[string]float64, len(members))
	calls := make(map[string]uint64, len(members))
	for _, m := range members {
		key := bareMember(m)
		st := groupMemberStats[key]
		w := float64(max(lookupMemberWeight(weights, m), 1))
		paceScore[m] = decayedTokensLocked(st, now) / w
		if st != nil {
			calls[m] = st.calls
		}
	}
	groupRuntimeMu.Unlock()

	ordered := append([]string(nil), members...)
	slices.SortStableFunc(ordered, func(a, b string) int {
		pa, pb := paceScore[a], paceScore[b]
		if math.Abs(pa-pb) > 1e-6 {
			if pa < pb {
				return -1
			}
			return 1
		}
		ca, cb := calls[a], calls[b]
		if ca < cb {
			return -1
		}
		if ca > cb {
			return 1
		}
		return 0
	})
	return applySinkOrder(ordered, sinkEnabled)
}

func lookupMemberWeight(weights map[string]int, member string) int {
	if len(weights) == 0 {
		return 1
	}
	if w, ok := weights[member]; ok && w > 0 {
		return w
	}
	if w, ok := weights[bareMember(member)]; ok && w > 0 {
		return w
	}
	return 1
}

// --- Rule Evaluation ---

type requestMetadata struct {
	promptText   string
	systemText   string
	modelName    string
	toolNames    []string
	hasVision    bool
	tokens       int
	isToolTurn   bool
	hasMultiTurn bool
}

func inspectRequestMetadata(protocol string, body []byte) requestMetadata {
	meta := requestMetadata{
		tokens: max(len(body)/4, 1),
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		meta.promptText = string(body)
		return meta
	}
	if m, ok := payload["model"].(string); ok {
		meta.modelName = strings.TrimSpace(m)
	}
	var sysParts []string
	if s := extractRuleText(payload["system"], &meta.hasVision); s != "" {
		sysParts = append(sysParts, s)
	}
	if s := extractRuleText(payload["instructions"], &meta.hasVision); s != "" {
		sysParts = append(sysParts, s)
	}
	if tools, ok := payload["tools"].([]any); ok {
		for _, rawTool := range tools {
			tm, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := tm["name"].(string); ok && strings.TrimSpace(name) != "" {
				meta.toolNames = append(meta.toolNames, strings.TrimSpace(name))
			} else if fn, ok := tm["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && strings.TrimSpace(name) != "" {
					meta.toolNames = append(meta.toolNames, strings.TrimSpace(name))
				}
			}
		}
	}
	if imgs, ok := payload["images"].([]any); ok && len(imgs) > 0 {
		meta.hasVision = true
	}

	rawMsgs := payload["messages"]
	if rawMsgs == nil {
		rawMsgs = payload["input"]
	}
	var userParts []string
	switch items := rawMsgs.(type) {
	case string:
		userParts = append(userParts, items)
	case []any:
		if len(items) > 1 {
			meta.hasMultiTurn = true
		}
		for i, rawItem := range items {
			isLast := i == len(items)-1
			m, ok := rawItem.(map[string]any)
			if !ok {
				if s, ok := rawItem.(string); ok {
					userParts = append(userParts, s)
				}
				continue
			}
			role, _ := m["role"].(string)
			itemType, _ := m["type"].(string)
			if role == "assistant" || role == "tool" || itemType == "function_call_output" || itemType == "custom_tool_call_output" {
				meta.hasMultiTurn = true
			}
			if isLast && (role == "tool" || itemType == "function_call_output" || itemType == "custom_tool_call_output") {
				meta.isToolTurn = true
			}
			if blocks, ok := m["content"].([]any); ok {
				for _, b := range blocks {
					if bm, ok := b.(map[string]any); ok {
						bt, _ := bm["type"].(string)
						if isLast && bt == "tool_result" {
							meta.isToolTurn = true
						}
						if bt == "image" || bt == "image_url" || bt == "input_image" || bm["image_url"] != nil {
							meta.hasVision = true
						}
					}
				}
			}
			txt := extractRuleText(m["content"], &meta.hasVision)
			if txt == "" {
				txt = extractRuleText(m["output"], &meta.hasVision)
			}
			switch role {
			case "system", "developer":
				if txt != "" {
					sysParts = append(sysParts, txt)
				}
			case "user", "":
				if txt != "" {
					userParts = append(userParts, txt)
				}
			}
		}
	}
	meta.systemText = strings.Join(sysParts, "\n")
	meta.promptText = strings.Join(userParts, "\n")
	return meta
}

func extractRuleText(v any, hasVision *bool) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		var parts []string
		for _, item := range val {
			switch block := item.(type) {
			case string:
				parts = append(parts, block)
			case map[string]any:
				bt, _ := block["type"].(string)
				if hasVision != nil && (bt == "image" || bt == "image_url" || bt == "input_image" || block["image_url"] != nil) {
					*hasVision = true
				}
				if t, ok := block["text"].(string); ok && t != "" {
					parts = append(parts, t)
				} else if t, ok := block["content"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func evaluateGroupRules(g routerconfig.Group, all []routerconfig.Group, protocol string, body []byte, members []string) ([]string, *routerconfig.Rule) {
	if len(g.Rules) == 0 {
		return members, nil
	}
	meta := inspectRequestMetadata(protocol, body)
	bodyLower := strings.ToLower(string(body))

	for i := range g.Rules {
		r := g.Rules[i]
		if !ruleMatchesRequest(r, meta, bodyLower) {
			continue
		}
		target := strings.TrimSpace(r.Use)
		if target == "" {
			target = strings.TrimSpace(r.Target)
		}
		if target == "" {
			return members, &r
		}
		if isMemberOff(g.Off, target) {
			continue
		}
		promoted := promoteRuleTarget(target, members, all, g.Off)
		return promoted, &r
	}
	return members, nil
}

func isMemberOff(off []string, target string) bool {
	targetBare := bareMember(target)
	for _, o := range off {
		o = strings.TrimSpace(o)
		if o == target || bareMember(o) == targetBare {
			return true
		}
	}
	return false
}

func promoteRuleTarget(target string, members []string, all []routerconfig.Group, off []string) []string {
	if subID, ok := routerconfig.GroupIDOf(target); ok {
		for _, subGrp := range all {
			if strings.EqualFold(subGrp.ID, subID) || routerconfig.GroupSlug(subGrp.ID) == routerconfig.GroupSlug(subID) {
				subMembers := routerconfig.ExpandGroupMembers(subGrp, all)
				var head, rest []string
				for _, sm := range subMembers {
					if !isMemberOff(off, sm) {
						head = append(head, sm)
					}
				}
				for _, m := range members {
					if !slices.Contains(head, m) {
						rest = append(rest, m)
					}
				}
				if len(head) > 0 {
					return append(head, rest...)
				}
			}
		}
	}
	targetBare := bareMember(target)
	idx := slices.IndexFunc(members, func(m string) bool {
		return m == target || bareMember(m) == targetBare
	})
	if idx == 0 {
		return members
	}
	if idx > 0 {
		chosen := members[idx]
		out := make([]string, 0, len(members))
		out = append(out, chosen)
		out = append(out, members[:idx]...)
		out = append(out, members[idx+1:]...)
		return out
	}
	return append([]string{target}, members...)
}

func ruleMatchesRequest(r routerconfig.Rule, meta requestMetadata, bodyLower string) bool {
	when := strings.ToLower(strings.TrimSpace(r.When))
	pat := strings.TrimSpace(r.Pattern)
	switch when {
	case "prompt":
		return matchRulePattern(pat, meta.promptText)
	case "system":
		return matchRulePattern(pat, meta.systemText)
	case "model":
		return pat == "" || routerconfig.GroupMatchesModel(pat, meta.modelName) || matchRulePattern(pat, meta.modelName)
	case "tools":
		if pat == "" || strings.EqualFold(pat, "true") || pat == "*" {
			return len(meta.toolNames) > 0
		}
		if strings.EqualFold(pat, "false") || strings.EqualFold(pat, "none") {
			return len(meta.toolNames) == 0
		}
		for _, tn := range meta.toolNames {
			if routerconfig.GroupMatchesModel(pat, tn) || matchRulePattern(pat, tn) {
				return true
			}
		}
		return false
	case "vision":
		if strings.EqualFold(pat, "false") || strings.EqualFold(pat, "no") || pat == "0" {
			return !meta.hasVision
		}
		return meta.hasVision
	case "tokens":
		return matchTokenCondition(pat, meta.tokens)
	default:
		if r.Images != nil && *r.Images != meta.hasVision {
			return false
		}
		if strings.TrimSpace(r.Tokens) != "" && !matchTokenCondition(r.Tokens, meta.tokens) {
			return false
		}
		if pat != "" {
			return matchRulePattern(pat, meta.promptText) || strings.Contains(bodyLower, strings.ToLower(pat))
		}
		return r.Images != nil || strings.TrimSpace(r.Tokens) != ""
	}
}

func matchRulePattern(pattern, text string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return strings.TrimSpace(text) != ""
	}
	if strings.HasPrefix(pattern, routerconfig.RegexpPrefix) {
		expr := strings.TrimPrefix(pattern, routerconfig.RegexpPrefix)
		re, err := regexp.Compile("(?i)" + expr)
		if err != nil {
			return false
		}
		return re.MatchString(text)
	}
	if strings.HasPrefix(pattern, "/") {
		if lastSlash := strings.LastIndex(pattern, "/"); lastSlash > 1 {
			expr := pattern[1:lastSlash]
			if re, err := regexp.Compile("(?i)" + expr); err == nil {
				return re.MatchString(text)
			}
		}
	}
	if re, err := regexp.Compile("(?i)" + pattern); err == nil && strings.ContainsAny(pattern, "^$|()+?[]\\") {
		if re.MatchString(text) {
			return true
		}
	}
	pLower, tLower := strings.ToLower(pattern), strings.ToLower(text)
	if strings.Contains(pLower, "*") {
		return routerconfig.GroupMatchesModel(pLower, tLower)
	}
	return strings.Contains(tLower, pLower)
}

func matchTokenCondition(expr string, tokens int) bool {
	s := strings.ToLower(strings.TrimSpace(expr))
	if s == "" {
		return true
	}
	op := ">="
	for _, prefix := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(s, prefix) {
			op = prefix
			s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
			break
		}
	}
	mult := 1.0
	if strings.HasSuffix(s, "k") {
		mult = 1000
		s = strings.TrimSpace(strings.TrimSuffix(s, "k"))
	} else if strings.HasSuffix(s, "m") {
		mult = 1000000
		s = strings.TrimSpace(strings.TrimSuffix(s, "m"))
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	threshold := int(val * mult)
	switch op {
	case ">":
		return tokens > threshold
	case ">=":
		return tokens >= threshold
	case "<":
		return tokens < threshold
	case "<=":
		return tokens <= threshold
	case "=":
		return tokens == threshold
	default:
		return tokens >= threshold
	}
}

// --- Session Affinity ---

func deriveGroupAffinityKey(groupID string, r *http.Request, protocol string, body []byte) (string, bool) {
	if r != nil {
		for _, hdr := range []string{"X-Session-Id", "Session_id", "X-Claude-Code-Session-Id", "X-Conversation-Id"} {
			if v := strings.TrimSpace(r.Header.Get(hdr)); v != "" && len(v) <= 256 {
				return groupID + "|hdr:" + v, true
			}
		}
	}
	appID := "app"
	if r != nil {
		if state, ok := routerrequest.FromContext(r.Context()); ok && state.AppID != "" {
			appID = state.AppID
		}
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil && payload != nil {
		if meta, ok := payload["metadata"].(map[string]any); ok {
			if uid := cleanSessionIdentifier(meta["user_id"]); uid != "" {
				return groupID + "|uid:" + uid, true
			}
		}
		if pck := cleanSessionIdentifier(payload["prompt_cache_key"]); pck != "" {
			return groupID + "|pck:" + pck, true
		}
	}
	if sid, _, seed := parseRedactionSessionMaterial(appID, protocol, body); sid != "" {
		return groupID + "|sid:" + sid, true
	} else if seed != "" {
		return groupID + "|seed:" + seed, false
	}
	return "", false
}

func applyGroupAffinity(g routerconfig.Group, r *http.Request, protocol string, body []byte, candidates []string) ([]string, string, bool) {
	mode := strings.ToLower(strings.TrimSpace(g.Affinity))
	if mode == routerconfig.AffinityOff || len(candidates) == 0 {
		return candidates, "", false
	}
	key, explicit := deriveGroupAffinityKey(g.ID, r, protocol, body)
	if key == "" {
		return candidates, "", false
	}
	meta := inspectRequestMetadata(protocol, body)
	if mode == "" && !explicit && !meta.hasMultiTurn {
		return candidates, key, false
	}
	now := time.Now()

	groupRuntimeMu.Lock()
	entry, exists := groupAffinities[key]
	if exists && !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
		delete(groupAffinities, key)
		exists = false
	}
	groupRuntimeMu.Unlock()

	isStrictPin := mode == routerconfig.AffinityPin || mode == routerconfig.AffinityStrict
	if mode == routerconfig.AffinityTurn && !meta.isToolTurn {
		return candidates, key, false
	}

	if exists && entry.member != "" {
		targetBare := bareMember(entry.member)
		idx := slices.IndexFunc(candidates, func(m string) bool {
			return m == entry.member || bareMember(m) == targetBare
		})
		if idx >= 0 {
			chosen := candidates[idx]
			if isStrictPin {
				return []string{chosen}, key, true
			}
			if !isMemberSunk(chosen) {
				if idx > 0 {
					out := make([]string, 0, len(candidates))
					out = append(out, chosen)
					out = append(out, candidates[:idx]...)
					out = append(out, candidates[idx+1:]...)
					return out, key, false
				}
				return candidates, key, false
			}
		}
	}

	if isStrictPin && len(candidates) > 0 {
		pinned := candidates[0]
		recordGroupAffinity(g, key, pinned, now)
		return []string{pinned}, key, true
	}
	return candidates, key, false
}

func recordGroupAffinity(g routerconfig.Group, key, member string, now time.Time) {
	if key == "" || member == "" {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(g.Affinity))
	if mode == routerconfig.AffinityOff {
		return
	}
	var ttl time.Duration
	switch mode {
	case "", "auto":
		ttl = 5 * time.Minute
	case routerconfig.AffinitySession:
		mins := g.Stickiness
		if mins <= 0 {
			mins = 30
		}
		ttl = time.Duration(mins) * time.Minute
	case routerconfig.AffinityTurn:
		ttl = 15 * time.Minute
	case routerconfig.AffinityPin, routerconfig.AffinityStrict:
		ttl = 24 * time.Hour
	default:
		ttl = 5 * time.Minute
	}
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()
	groupAffinities[key] = affinityEntry{
		member:    member,
		updatedAt: now,
		expiresAt: now.Add(ttl),
	}
}

// --- Effort, Supported Levels, and Fast Tier Rewriting ---

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func clampEffortLevel(effort string, supported []string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || len(supported) == 0 {
		return effort
	}
	cleanSup := make([]string, 0, len(supported))
	for _, s := range supported {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			cleanSup = append(cleanSup, s)
		}
	}
	if len(cleanSup) == 0 || slices.Contains(cleanSup, effort) {
		return effort
	}
	targetRank := slices.Index(effortOrder, effort)
	if targetRank < 0 {
		return cleanSup[0]
	}
	best := ""
	bestRank := -1
	lowest := cleanSup[0]
	lowestRank := 999
	for _, sup := range cleanSup {
		r := slices.Index(effortOrder, sup)
		if r < 0 {
			continue
		}
		if r < lowestRank {
			lowestRank = r
			lowest = sup
		}
		if r <= targetRank && r > bestRank {
			bestRank = r
			best = sup
		}
	}
	if best != "" {
		return best
	}
	return lowest
}

func inferAutoEffort(protocol string, body []byte) string {
	meta := inspectRequestMetadata(protocol, body)
	text := strings.TrimSpace(meta.promptText)
	runes := utf8.RuneCountInString(text)
	lower := strings.ToLower(text)
	for _, kw := range []string{
		"step by step", "architect", "refactor", "root cause", "concurrency", "deadlock",
		"prove", "optimize", "complex", "架构", "重构", "死锁", "并发", "深入分析", "一步一步",
	} {
		if strings.Contains(lower, kw) {
			return "high"
		}
	}
	if runes > 800 || meta.tokens > 2000 || len(meta.toolNames) >= 4 {
		return "high"
	}
	if runes < 120 && len(meta.toolNames) == 0 {
		return "low"
	}
	return "medium"
}

func resolveCandidateEffortAndFast(g routerconfig.Group, member string, matchedRule *routerconfig.Rule, protocol string, body []byte) (string, bool) {
	effort := ""
	if matchedRule != nil && strings.TrimSpace(matchedRule.Effort) != "" {
		effort = strings.ToLower(strings.TrimSpace(matchedRule.Effort))
	}
	if effort == "" {
		if inline := memberInlineEffort(member); inline != "" {
			effort = inline
		} else if len(g.MemberEfforts) > 0 {
			if eff, ok := g.MemberEfforts[member]; ok && strings.TrimSpace(eff) != "" {
				effort = strings.ToLower(strings.TrimSpace(eff))
			} else if eff, ok := g.MemberEfforts[bareMember(member)]; ok && strings.TrimSpace(eff) != "" {
				effort = strings.ToLower(strings.TrimSpace(eff))
			}
		}
	}
	if effort == "" {
		grpEffort := strings.ToLower(strings.TrimSpace(g.Effort))
		if grpEffort == routerconfig.EffortAuto {
			effort = inferAutoEffort(protocol, body)
		} else if grpEffort != "" {
			effort = grpEffort
		}
	}
	if effort == routerconfig.EffortAuto {
		effort = inferAutoEffort(protocol, body)
	}
	if effort == "" && len(g.Levels) > 0 {
		effort = extractRequestEffort(protocol, body)
	}
	if effort != "" && len(g.Levels) > 0 {
		effort = clampEffortLevel(effort, g.Levels)
	}

	isFast := (matchedRule != nil && matchedRule.Fast) ||
		slices.Contains(g.Fast, member) ||
		slices.Contains(g.Fast, bareMember(member))
	return effort, isFast
}

func extractRequestEffort(protocol string, body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return ""
	}
	switch protocol {
	case "chat":
		if s, ok := payload["reasoning_effort"].(string); ok {
			return strings.ToLower(strings.TrimSpace(s))
		}
	case "responses":
		if r, ok := payload["reasoning"].(map[string]any); ok {
			if s, ok := r["effort"].(string); ok {
				return strings.ToLower(strings.TrimSpace(s))
			}
		}
	case "anthropic":
		if oc, ok := payload["output_config"].(map[string]any); ok {
			if s, ok := oc["effort"].(string); ok {
				return strings.ToLower(strings.TrimSpace(s))
			}
		}
	}
	return ""
}

func rewriteCandidateRequestBody(protocol string, body []byte, effort string, isFast bool) []byte {
	if effort == "" && !isFast {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return body
	}
	changed := false
	if effort != "" {
		switch protocol {
		case "chat":
			payload["reasoning_effort"] = effort
			changed = true
		case "responses":
			r, _ := payload["reasoning"].(map[string]any)
			if r == nil {
				r = map[string]any{}
			}
			r["effort"] = effort
			payload["reasoning"] = r
			changed = true
		case "anthropic":
			oc, _ := payload["output_config"].(map[string]any)
			if oc == nil {
				oc = map[string]any{}
			}
			oc["effort"] = effort
			payload["output_config"] = oc
			changed = true
		}
	}
	if isFast && (protocol == "chat" || protocol == "responses") {
		if _, exists := payload["service_tier"]; !exists {
			payload["service_tier"] = "priority"
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// --- Patience & Error Classification ---

func isRetriableGroupFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errFirstTokenTimeout) {
		return true
	}
	var cloudErr *routercloud.Error
	if errors.As(err, &cloudErr) {
		switch cloudErr.Status {
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
			return true
		}
		switch cloudErr.Code {
		case "cloud_rate_limit", "cloud_upstream_error", "cloud_network_error", "cloud_timeout":
			return true
		}
	}
	return false
}

// --- Model List Unlisting & Context Resolution ---

func estimateMemberContextWindow(member string, local *LocalRuntime) int {
	bare := bareMember(member)
	if localID, ok := strings.CutPrefix(bare, "local/"); ok && local != nil {
		for _, m := range local.Catalog.Models {
			if strings.EqualFold(m.ID, localID) {
				if m.ContextTokens > 0 {
					return m.ContextTokens
				}
				if m.NativeContextTokens > 0 {
					return m.NativeContextTokens
				}
			}
		}
		return 32768
	}
	lower := strings.ToLower(bare)
	switch {
	case strings.Contains(lower, "gemini-1.5-pro"), strings.Contains(lower, "gemini-2.5"), strings.Contains(lower, "gemini-2.0"), strings.Contains(lower, "gpt-4.1"):
		return 1048576
	case strings.Contains(lower, "claude-"), strings.Contains(lower, "o3"), strings.Contains(lower, "o4-mini"), strings.Contains(lower, "gpt-5"):
		return 200000
	case strings.Contains(lower, "gpt-4o"), strings.Contains(lower, "deepseek-"):
		return 128000
	default:
		return 128000
	}
}

func resolveGroupContextWindow(g routerconfig.Group, all []routerconfig.Group, local *LocalRuntime) int {
	if g.Context > 0 {
		return g.Context
	}
	members := routerconfig.ExpandGroupMembers(g, all)
	if len(members) == 0 {
		return 128000
	}
	smallest, largest := 0, 0
	for _, m := range members {
		if isMemberOff(g.Off, m) {
			continue
		}
		cw := estimateMemberContextWindow(m, local)
		if cw <= 0 {
			continue
		}
		if smallest == 0 || cw < smallest {
			smallest = cw
		}
		if cw > largest {
			largest = cw
		}
	}
	if g.Context == routerconfig.ContextSmallest {
		if smallest > 0 {
			return smallest
		}
	}
	if largest > 0 {
		return largest
	}
	return 128000
}
