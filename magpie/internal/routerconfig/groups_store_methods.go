package routerconfig

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

func decodeProvidersMap(b []byte) []CloudProvider {
	var m map[string]CloudProvider
	_ = json.Unmarshal(b, &m)
	keys := make([]string, 0, len(m))
	for id := range m {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	out := make([]CloudProvider, 0, len(keys))
	for _, id := range keys {
		p := m[id]
		if p.ID == "" {
			p.ID = id
		}
		out = append(out, p)
	}
	return out
}

func cloneGroup(g Group) Group {
	cp := g
	if g.Members != nil {
		cp.Members = append([]string(nil), g.Members...)
	}
	if g.Match != nil {
		cp.Match = append([]string(nil), g.Match...)
	}
	if g.Matched != nil {
		cp.Matched = append([]string(nil), g.Matched...)
	}
	if g.Off != nil {
		cp.Off = append([]string(nil), g.Off...)
	}
	if g.Levels != nil {
		cp.Levels = append([]string(nil), g.Levels...)
	}
	if g.Fast != nil {
		cp.Fast = append([]string(nil), g.Fast...)
	}
	if g.Rules != nil {
		cp.Rules = append([]Rule(nil), g.Rules...)
	}
	if g.Weights != nil {
		cp.Weights = make(map[string]int, len(g.Weights))
		for k, v := range g.Weights {
			cp.Weights[k] = v
		}
	}
	if g.MemberEfforts != nil {
		cp.MemberEfforts = make(map[string]string, len(g.MemberEfforts))
		for k, v := range g.MemberEfforts {
			cp.MemberEfforts[k] = v
		}
	}
	return cp
}

func computeMatchedMembers(g Group, candidates []string) []string {
	if len(g.Match) == 0 || len(candidates) == 0 {
		return nil
	}
	explicit := make(map[string]bool, len(g.Members))
	for _, m := range g.Members {
		explicit[m] = true
	}
	var matched []string
	for _, cand := range candidates {
		if explicit[cand] {
			continue
		}
		for _, pat := range g.Match {
			if GroupMatchesModel(pat, cand) {
				matched = append(matched, cand)
				break
			}
		}
	}
	return matched
}

// GroupMatchesModel checks whether a glob or "re:" regex pattern matches a "provider/model" reference.
func GroupMatchesModel(pattern, modelRef string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	bareModel := modelRef
	if idx := strings.IndexByte(modelRef, '/'); idx >= 0 {
		bareModel = modelRef[idx+1:]
	}
	if strings.HasPrefix(pattern, RegexpPrefix) {
		expr := strings.TrimPrefix(pattern, RegexpPrefix)
		if expr == "" {
			return false
		}
		re, err := regexp.Compile("(?i)" + expr)
		if err != nil {
			return false
		}
		if strings.Contains(expr, "/") {
			return re.MatchString(modelRef)
		}
		return re.MatchString(bareModel) || re.MatchString(modelRef)
	}
	pLower := strings.ToLower(pattern)
	if strings.Contains(pLower, "/") {
		return globMatch(pLower, strings.ToLower(modelRef))
	}
	return globMatch(pLower, strings.ToLower(bareModel)) || globMatch(pLower, strings.ToLower(modelRef))
}

func globMatch(pattern, name string) bool {
	px, nx := 0, 0
	nextPx, nextNx := 0, 0
	for px < len(pattern) || nx < len(name) {
		if px < len(pattern) {
			switch pattern[px] {
			case '*':
				nextPx = px
				nextNx = nx + 1
				px++
				continue
			case '?':
				if nx < len(name) {
					px++
					nx++
					continue
				}
			default:
				if nx < len(name) && pattern[px] == name[nx] {
					px++
					nx++
					continue
				}
			}
		}
		if 0 < nextNx && nextNx <= len(name) {
			px = nextPx + 1
			nx = nextNx
			nextNx++
			continue
		}
		return false
	}
	return true
}
