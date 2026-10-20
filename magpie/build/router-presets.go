//go:build ignore

// Generate passive API-key presets from the checked-in Magpie source without
// importing its provider package or activating accounts/catalog/plugin code.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

func values(lit *ast.CompositeLit) map[string]string {
	out := map[string]string{}
	for _, item := range lit.Elts {
		kv, ok := item.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if value, ok := kv.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
			out[key.Name], _ = strconv.Unquote(value.Value)
		}
		if value, ok := kv.Value.(*ast.Ident); ok {
			out[key.Name] = value.Name
		}
	}
	return out
}
func stringSlice(expr ast.Expr, named map[string][]string) []string {
	switch v := expr.(type) {
	case *ast.Ident:
		if s, ok := named[v.Name]; ok {
			return append([]string(nil), s...)
		}
	case *ast.CompositeLit:
		var out []string
		for _, elt := range v.Elts {
			if lit, ok := elt.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

func fieldModels(lit *ast.CompositeLit, named map[string][]string) []string {
	for _, item := range lit.Elts {
		kv, ok := item.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Models" {
			continue
		}
		if models := stringSlice(kv.Value, named); len(models) > 0 {
			return models
		}
	}
	return nil
}

var defaultPresetModels = map[string][]string{
	"spark":        {"4.0Ultra", "generalv3.5", "max-32k", "generalv3", "pro-128k", "lite"},
	"spark-maas":   {"spark-x2.5", "spark-x2.5-4b", "spark-x2.5-1.7b"},
	"openai":       {"gpt-5.4", "gpt-5.4-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4o", "o3", "o4-mini"},
	"anthropic":    {"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5-20251001", "claude-sonnet-4-5-20250929"},
	"google":       {"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"},
	"deepseek":     {"deepseek-chat", "deepseek-reasoner"},
	"xai":          {"grok-4", "grok-3", "grok-3-mini"},
	"moonshot":     {"kimi-k2.5", "kimi-k2-thinking", "kimi-k2-turbo-preview", "moonshot-v1-auto"},
	"moonshot-cn":  {"kimi-k2.5", "kimi-k2-thinking", "kimi-k2-turbo-preview", "moonshot-v1-auto"},
	"kimi-code":    {"kimi-for-coding"},
	"kimi-code-cn": {"kimi-for-coding"},
	"zhipu":        {"glm-5", "glm-4.7", "glm-4.5-air"},
	"zai":          {"glm-5", "glm-4.7", "glm-4.5-air"},
	"minimax":      {"MiniMax-M2.7", "MiniMax-M2.5", "MiniMax-M2.1"},
	"minimax-cn":   {"MiniMax-M2.7", "MiniMax-M2.5", "MiniMax-M2.1"},
	"stepfun":      {"step-3.5-flash", "step-2-16k", "step-1-8k"},
	"stepfun-cn":   {"step-3.5-flash", "step-2-16k", "step-1-8k"},
	"xiaomi":       {"mimo-v2-pro", "mimo-v2-flash"},
	"qwen":       {"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "qwen3-max", "qwen-plus", "qwen-turbo", "qwen3-coder-plus", "qwq-plus"},
	"qwen-cn":    {"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "qwen3-max", "qwen-plus", "qwen-turbo", "qwen3-coder-plus", "qwq-plus"},
	"qianwen-ai": {"qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "qwen3-max", "qwen-plus", "qwen-turbo", "qwen3-coder-plus"},
	"mistral":      {"mistral-large-latest", "codestral-latest", "mistral-small-latest"},
	"groq":         {"llama-3.3-70b-versatile", "llama-3.1-8b-instant", "qwen/qwen3-32b"},
}

func main() {
	file, err := parser.ParseFile(token.NewFileSet(), "internal/provider/presets.go", nil, 0)
	if err != nil {
		panic(err)
	}
	namedSlices := map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Names[0].Name == "presets" {
			return true
		}
		if s := stringSlice(spec.Values[0], nil); len(s) > 0 {
			namedSlices[spec.Names[0].Name] = s
		}
		return true
	})
	result := []map[string]any{{
		"id": "spark", "name": "Spark", "icon": "spark-color", "kind": "vendor",
		"endpoints": map[string]string{"chat": "https://spark-api-open.xf-yun.com/v1"},
		"models":    defaultPresetModels["spark"],
		"website":   "https://xinghuo.xfyun.cn",
		"docsURL":   "https://www.xfyun.cn/doc/spark/HTTP调用文档.html",
	}, {
		"id": "spark-maas", "name": "Spark MaaS", "icon": "spark-color", "kind": "vendor",
		"defaultProtocol": "chat",
		"endpoints": map[string]string{
			"chat":      "https://maas-api.cn-huabei-1.xf-yun.com/v2",
			"responses": "https://maas-api.cn-huabei-1.xf-yun.com/v1",
			"anthropic": "https://maas-api.cn-huabei-1.xf-yun.com/anthropic",
		},
		"models":  defaultPresetModels["spark-maas"],
		"website": "https://maas.xfyun.cn",
		"docsURL": "https://www.xfyun.cn/doc/spark/产品使用说明.html",
	}}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "presets" {
			return true
		}
		all := spec.Values[0].(*ast.CompositeLit)
		for _, item := range all.Elts {
			lit, ok := item.(*ast.CompositeLit)
			if !ok {
				continue
			}
			p := values(lit)
			if p["ID"] == "" || (p["Kind"] != "KindVendor" && p["Kind"] != "KindRelay") || strings.Contains(p["ID"], "plan") || strings.Contains(p["ID"], "decision") {
				continue
			}
			models := fieldModels(lit, namedSlices)
			// Prefer a pay-as-you-go API region over a subscription-plan default.
			for _, field := range lit.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Regions" {
					continue
				}
				regions, ok := kv.Value.(*ast.CompositeLit)
				if !ok {
					continue
				}
				var fallbackRegionModels []string
				for _, region := range regions.Elts {
					r, ok := region.(*ast.CompositeLit)
					if !ok {
						continue
					}
					rm := fieldModels(r, namedSlices)
					if len(fallbackRegionModels) == 0 && len(rm) > 0 {
						fallbackRegionModels = rm
					}
					v := values(r)
					if v["ID"] == "api" || (p["ID"] == "tencent-cloud" && v["ID"] == "cn") {
						for _, name := range []string{"Chat", "Responses", "Anthropic"} {
							p[name] = v[name]
						}
						if len(rm) > 0 {
							models = rm
						}
						break
					}
				}
				if len(models) == 0 && len(fallbackRegionModels) > 0 {
					models = fallbackRegionModels
				}
			}
			if len(models) == 0 {
				models = defaultPresetModels[p["ID"]]
			}
			endpoints := map[string]string{}
			for name, protocol := range map[string]string{"Chat": "chat", "Responses": "responses", "Anthropic": "anthropic"} {
				if strings.HasPrefix(p[name], "https://") {
					endpoints[protocol] = p[name]
				}
			}
			if len(endpoints) == 0 {
				continue
			} // Native non-key auth and decision-only/local APIs are outside this form.
			kind := "vendor"
			if p["Kind"] == "KindRelay" {
				kind = "relay"
			}
			name := p["Name"]
			if p["ID"] == "moonshot" {
				name = "Kimi (International)"
			}
			preset := map[string]any{"id": p["ID"], "name": name, "icon": p["Icon"], "kind": kind, "endpoints": endpoints, "website": p["Website"], "keysURL": p["KeysURL"]}
			if len(models) > 0 {
				preset["models"] = models
			}
			// Stable IDs keep saved providers editable; names identify the issuing
			// platform and region, rather than implying every Qwen key works here.
			switch p["ID"] {
			case "qwen":
				preset["name"] = "Alibaba Cloud Model Studio (Singapore)"
				preset["docsURL"] = "https://help.aliyun.com/en/model-studio/base-url"
			case "qwen-cn":
				preset["name"] = "Alibaba Cloud Bailian (Beijing)"
				preset["docsURL"] = "https://help.aliyun.com/zh/model-studio/base-url"
			}
			result = append(result, preset)
			if p["ID"] == "qwen-cn" {
				result = append(result, map[string]any{
					"id": "qianwen-ai", "name": "Qianwen AI Platform", "icon": "qwen-color", "kind": "vendor",
					"endpoints": map[string]string{"chat": "https://maas.qianwenaiapi.com/compatible-mode/v1"},
					"models":    defaultPresetModels["qianwen-ai"],
					"website":   "https://platform.qianwenai.com", "keysURL": "https://platform.qianwenai.com/home/api-keys",
					"docsURL": "https://platform.qianwenai.com/docs/api-reference/preparation/api-key",
				})
			}
		}
		return false
	})
	result = append(result, map[string]any{"id": "custom", "name": "Custom provider", "kind": "custom", "endpoints": map[string]string{}})
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("internal/routerservice/assets/provider-presets.json", append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
}
