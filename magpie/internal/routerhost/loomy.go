package routerhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

const loomyProvider = "api_router"

type LoomyAdapter struct{}

func (LoomyAdapter) Contract() AdapterContract {
	return AdapterContract{ID: "loomy", Version: 1, Files: []FileSpec{{
		Name: "opencode.json",
		Fields: []string{"model", "small_model", "provider.api_router.npm", "provider.api_router.name",
			"provider.api_router.options.baseURL", "provider.api_router.options.apiKey",
			"provider.api_router.options.useSessionAuth", "provider.api_router.models"},
	}}}
}

func (LoomyAdapter) Build(o Options, input []FilePlan) ([]FilePlan, error) {
	if o.Host != "loomy" || len(input) != 1 || input[0].Name != "opencode.json" {
		return nil, errors.New("invalid Loomy adapter input")
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	models := o.EffectiveModels()
	primary := models[0]
	for _, m := range models {
		if !loomyModelKey(m) {
			return nil, errors.New("model ID cannot be safely represented as a Loomy provider model key")
		}
		if m == "gpt-5-chat-latest" {
			return nil, errors.New("installed Loomy excludes this model key")
		}
	}
	f := input[0]
	if f.Existed && len(bytes.TrimSpace(f.Before)) == 0 {
		return nil, errors.New("empty Loomy opencode.json is invalid")
	}
	doc, err := readJSON(f.Before)
	if err != nil {
		return nil, errors.New("invalid Loomy opencode.json")
	}
	if !loomyProviderEnabled(doc) {
		return nil, errors.New("Loomy provider controls exclude api_router or have an invalid shape; reconcile them before applying")
	}
	for _, key := range []string{"model", "small_model"} {
		if v, ok := doc[key]; ok {
			if _, ok := v.(string); !ok {
				return nil, errors.New("Loomy model selections must be strings")
			}
		}
	}
	if providers, ok := doc["provider"]; ok {
		if _, ok := providers.(map[string]any); !ok {
			return nil, errors.New("Loomy provider must be an object")
		}
	}
	wanted := loomyProviderValues(strings.TrimRight(o.RouterURL, "/")+"/app/loomy/v1", models...)
	var set []edit.KV
	var staleModels []string
	if existing, ok := lookup(doc, []string{"provider", loomyProvider}); ok {
		if loomyDefaultsOnlyProvider(existing) {
			for _, path := range loomyProviderPaths(models...) {
				value, _ := lookup(wanted, path)
				set = append(set, edit.KV{Path: loomyJSONPath(append([]string{"provider", loomyProvider}, path...)), Value: value})
			}
			staleModels = loomyStaleDefaultModels(existing, models...)
		} else if !loomyCompatibleProvider(existing, wanted, models...) {
			return nil, errors.New("Loomy api_router provider conflicts; rename or reconcile it before applying")
		}
	} else {
		set = append(set, edit.KV{Path: "provider." + loomyProvider, Value: wanted})
	}
	selection := loomyProvider + "/" + primary
	for _, key := range []string{"model", "small_model"} {
		if key == "small_model" && loomyKeepSmallModel(doc) {
			continue
		}
		if old, exists := doc[key]; !exists || old != selection {
			set = append(set, edit.KV{Path: key, Value: selection})
		}
	}
	if f.After, err = edit.PatchJSON(f.Before, set, nil); err != nil {
		return nil, errors.New("cannot safely edit Loomy opencode.json")
	}
	for _, m := range staleModels {
		if f.After, err = deleteJSONField(f.After, loomyJSONPath([]string{"provider", loomyProvider, "models", m})); err != nil {
			return nil, errors.New("cannot safely edit Loomy opencode.json")
		}
	}
	return []FilePlan{f}, nil
}

func loomyPreservedSmallModel(doc map[string]any) bool {
	small, ok := doc["small_model"].(string)
	provider, model, hasSlash := strings.Cut(small, "/")
	return ok && hasSlash && provider != "" && provider != loomyProvider && loomyModelKey(model)
}

func loomyKeepSmallModel(doc map[string]any) bool {
	model, ok := doc["model"].(string)
	return ok && strings.HasPrefix(model, loomyProvider+"/") && loomyPreservedSmallModel(doc)
}

func loomyRouterSelection(v any) bool {
	s, ok := v.(string)
	model := strings.TrimPrefix(s, loomyProvider+"/")
	return ok && model != s && loomyModelKey(model)
}

func loomyManagedModelEntry(v any) bool {
	entry, ok := v.(map[string]any)
	if !ok || entry["tool_call"] != true {
		return false
	}
	name, ok := entry["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return false
	}
	for k, val := range entry {
		switch k {
		case "name", "tool_call":
		case "limit":
			limit, ok := val.(map[string]any)
			if !ok || len(limit) == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func loomyProviderValues(base string, models ...string) map[string]any {
	catalog := map[string]any{}
	for _, m := range models {
		catalog[m] = map[string]any{"name": m, "tool_call": true}
	}
	return map[string]any{
		"npm": "@ai-sdk/openai-compatible", "name": "API Router",
		"options": map[string]any{"baseURL": base, "apiKey": PlaceholderAPIKey, "useSessionAuth": false},
		"models":  catalog,
	}
}

func loomyDefaultsOnlyProvider(value any) bool {
	p, ok := value.(map[string]any)
	options, ook := p["options"].(map[string]any)
	if !ok || len(p) == 0 || !ook || len(options) == 0 {
		return false
	}
	for key, val := range p {
		switch key {
		case "options":
		case "localNote":
			if s, ok := val.(string); !ok || strings.TrimSpace(s) == "" {
				return false
			}
		case "models":
			models, ok := val.(map[string]any)
			if !ok || len(models) == 0 {
				return false
			}
			for mKey, mv := range models {
				entry, eok := mv.(map[string]any)
				limit, lok := entry["limit"].(map[string]any)
				if !loomyModelKey(mKey) || !eok || len(entry) != 1 || !lok || len(limit) == 0 {
					return false
				}
			}
		default:
			return false
		}
	}
	for key, v := range options {
		if (key != "timeout" && key != "chunkTimeout") || v != json.Number("1200000") {
			return false
		}
	}
	return true
}

func loomyStaleDefaultModels(value any, keepModels ...string) []string {
	p, _ := value.(map[string]any)
	models, _ := p["models"].(map[string]any)
	if len(models) == 0 {
		return nil
	}
	keep := map[string]bool{}
	for _, m := range keepModels {
		keep[m] = true
	}
	var stale []string
	for k := range models {
		if !keep[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	return stale
}

func loomyCompatibleProvider(value any, wanted map[string]any, models ...string) bool {
	p, ok := value.(map[string]any)
	options, ook := p["options"].(map[string]any)
	if !ok || !ook {
		return false
	}
	for _, path := range loomyProviderPaths(models...) {
		got, gok := lookup(p, path)
		want, wok := lookup(wanted, path)
		if len(path) == 3 && path[0] == "models" && path[2] == "name" {
			if name, ok := got.(string); gok && ok && strings.TrimSpace(name) != "" {
				continue
			}
		}
		if !equalValue(got, gok, want, wok) {
			return false
		}
	}
	for key, v := range options {
		if (key == "timeout" || key == "chunkTimeout") && v == json.Number("1200000") {
			continue
		}
		if key != "baseURL" && key != "apiKey" && key != "useSessionAuth" {
			return false
		}
	}
	for _, key := range []string{"api", "env", "headers", "whitelist", "blacklist"} {
		if _, exists := p[key]; exists {
			return false
		}
	}
	for _, model := range models {
		selected, _ := lookup(p, []string{"models", model})
		modelConfig, ok := selected.(map[string]any)
		if !ok {
			return false
		}
		for _, key := range []string{"id", "provider", "api", "options", "headers", "env"} {
			if _, exists := modelConfig[key]; exists {
				return false
			}
		}
		if status, exists := modelConfig["status"]; exists {
			if text, ok := status.(string); !ok || text == "alpha" || text == "deprecated" {
				return false
			}
		}
	}
	return true
}

func loomyProviderEnabled(doc map[string]any) bool {
	for _, name := range []string{"disabled_providers", "enabled_providers"} {
		value, exists := doc[name]
		if !exists {
			continue
		}
		list, ok := value.([]any)
		if !ok {
			return false
		}
		found := false
		for _, item := range list {
			id, ok := item.(string)
			if !ok {
				return false
			}
			found = found || id == loomyProvider
		}
		if (name == "disabled_providers" && found) || (name == "enabled_providers" && !found) {
			return false
		}
	}
	return true
}

func (LoomyAdapter) Restore(f FilePlan, current []byte) ([]byte, error) {
	if f.Name != "opencode.json" {
		return nil, errors.New("invalid Loomy recovery file")
	}
	if bytes.Equal(current, f.Before) {
		return current, nil
	} else if bytes.Equal(current, f.After) {
		if !f.Existed {
			return nil, nil
		}
		return f.Before, nil
	}
	before, err1 := readJSON(f.Before)
	after, err2 := readJSON(f.After)
	now, err3 := readJSON(current)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, errors.New("invalid Loomy configuration")
	}
	selection, ok := after["model"].(string)
	primary := strings.TrimPrefix(selection, loomyProvider+"/")
	if !ok || primary == selection || !loomyModelKey(primary) {
		return nil, errors.New("invalid Loomy recovery model")
	}
	models, cat := []string{primary}, func() map[string]any {
		v, _ := lookup(after, []string{"provider", loomyProvider, "models"})
		m, _ := v.(map[string]any)
		return m
	}()
	seen := map[string]bool{primary: true}
	var extra []string
	for k := range cat {
		if !seen[k] && loomyModelKey(k) {
			seen[k] = true
			extra = append(extra, k)
		}
	}
	if nowCat, ok := func() (map[string]any, bool) {
		v, ok := lookup(now, []string{"provider", loomyProvider, "models"})
		m, mok := v.(map[string]any)
		return m, ok && mok
	}(); ok {
		for k, v := range nowCat {
			if _, inBefore := lookup(before, []string{"provider", loomyProvider, "models", k}); !inBefore && !seen[k] && loomyModelKey(k) && loomyManagedModelEntry(v) {
				seen[k] = true
				extra = append(extra, k)
			}
		}
	}
	sort.Strings(extra)
	models = append(models, extra...)
	out := bytes.Clone(current)
	var err error
	for _, path := range loomyManagedPaths(models...) {
		for n := 1; n < len(path); n++ {
			if parent, exists := lookup(now, path[:n]); exists {
				if _, object := parent.(map[string]any); !object {
					return nil, fmt.Errorf("Loomy managed container conflict in %s", strings.Join(path[:n], "."))
				}
			}
		}
		b, bok := lookup(before, path)
		a, aok := lookup(after, path)
		c, cok := lookup(now, path)
		if (equalValue(b, bok, a, aok) && !loomyExtraManagedModelPath(path, bok, aok, cok)) || equalValue(b, bok, c, cok) {
			continue
		}
		if !equalValue(a, aok, c, cok) {
			if len(path) == 1 && path[0] == "small_model" && loomyPreservedSmallModel(now) {
				continue
			}
			if !loomyReconnectableConflict(path, aok, c) {
				return nil, fmt.Errorf("Loomy managed field conflict in %s", strings.Join(path, "."))
			}
		}
		if bok {
			out, err = edit.PatchJSON(out, []edit.KV{{Path: loomyJSONPath(path), Value: b}}, nil)
		} else {
			out, err = deleteJSONField(out, loomyJSONPath(path))
		}
		if err != nil {
			return nil, errors.New("cannot safely restore Loomy configuration")
		}
	}
	return loomyCleanEmptyContainers(out, before, models)
}

func loomyExtraManagedModelPath(path []string, bok, aok, cok bool) bool {
	return len(path) == 5 && path[0] == "provider" && path[1] == loomyProvider && path[2] == "models" && !bok && !aok && cok
}

func loomyReconnectableConflict(path []string, aok bool, current any) bool {
	if len(path) == 1 && (path[0] == "model" || path[0] == "small_model") {
		return loomyRouterSelection(current)
	}
	if len(path) == 5 && path[0] == "provider" && path[1] == loomyProvider && path[2] == "models" {
		switch path[4] {
		case "name":
			s, ok := current.(string)
			return ok && strings.TrimSpace(s) != ""
		case "tool_call":
			b, ok := current.(bool)
			return !aok && ok && b
		}
	}
	return false
}
