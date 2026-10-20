package cloudcodec

import (
	"bytes"
	"encoding/json"
)

// liteNamespace is the namespace Responses Lite groups Codex's own
// functions in (codex_protocol::DEFAULT_FUNCTION_NAMESPACE).
const LiteNamespace = "functions"

// liftAdditionalTools is a Responses request with its additional_tools items
// moved into its tools, or the request as it was when it has none.
func LiftAdditionalTools(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"additional_tools"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	items, ok := m["input"].([]any)
	if !ok {
		return body
	}
	tools, _ := m["tools"].([]any)
	have := ToolsByName(tools)
	kept := items[:0:0]
	lifted := false
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im["type"] != "additional_tools" {
			kept = append(kept, it)
			continue
		}
		lifted = true
		more, _ := im["tools"].([]any)
		for _, t := range more {
			tm, _ := t.(map[string]any)
			if tm == nil {
				continue
			}
			if tm["type"] == "namespace" && tm["name"] == LiteNamespace {
				nested, _ := tm["tools"].([]any)
				for _, n := range nested {
					if nm, _ := n.(map[string]any); nm != nil {
						tools = AddTool(tools, have, nm)
					}
				}
				continue
			}
			tools = AddTool(tools, have, tm)
		}
	}
	if !lifted {
		return body
	}
	// a call the model made to one of Codex's own tools, under the
	// namespace they were grouped in, is the plain call it now is
	for _, it := range kept {
		if im, _ := it.(map[string]any); im != nil && im["namespace"] == LiteNamespace {
			delete(im, "namespace")
		}
	}
	m["input"] = kept
	if len(tools) > 0 {
		m["tools"] = tools
	}
	out, err := MarshalPlain(m)
	if err != nil {
		return body
	}
	return out
}

// toolsByName is a request's tools by their names, a hosted tool without
// one (web_search) by its type.
func ToolsByName(tools []any) map[string]map[string]any {
	have := map[string]map[string]any{}
	for _, t := range tools {
		if tm, _ := t.(map[string]any); tm != nil {
			have[ToolKey(tm)] = tm
		}
	}
	return have
}

func ToolKey(tm map[string]any) string {
	if n, _ := tm["name"].(string); n != "" {
		return n
	}
	ty, _ := tm["type"].(string)
	return "#" + ty
}

// addTool is tools with tm among them: a tool added once, and a namespace
// given again adding to the one there the tools it didn't have.
func AddTool(tools []any, have map[string]map[string]any, tm map[string]any) []any {
	k := ToolKey(tm)
	old := have[k]
	if old == nil {
		have[k] = tm
		return append(tools, tm)
	}
	if old["type"] != "namespace" || tm["type"] != "namespace" {
		return tools
	}
	nested, _ := old["tools"].([]any)
	more, _ := tm["tools"].([]any)
	for _, nt := range more {
		nm, _ := nt.(map[string]any)
		if nm != nil && !ContainsNamed(nested, nm["name"]) {
			nested = append(nested, nm)
		}
	}
	old["tools"] = nested
	return tools
}

func ContainsNamed(tools []any, name any) bool {
	for _, o := range tools {
		if om, _ := o.(map[string]any); om["name"] == name {
			return true
		}
	}
	return false
}
