package routerredact

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routercloud"
	"io"
	"strconv"
)

func parseRequest(p Protocol, body []byte) (*textDocument, error) {
	if routercloud.ValidateInput(string(p), body) != nil {
		return nil, fail("wire_invalid")
	}
	v, err := wireDecode(body, 8<<20)
	if err != nil {
		return nil, err
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, fail("wire_invalid")
	}
	d := &textDocument{root: o}
	// Unknown metadata/cache contracts stay unavailable. The separately
	// evidenced Chat host fields below are protected, never exempted from scan.
	for _, key := range []string{"metadata", "client_metadata", "prompt_cache_key"} {
		if value, exists := o[key]; exists && !wireEmpty(value) {
			return nil, fail("wire_unsupported")
		}
	}
	// The installed Loomy options chain carries this maintenance comment as a
	// root Chat string. Preserve its exact metadata meaning or fail closed.
	d.protectField(o, "$comment", "host:root")
	if p == Chat {
		for _, key := range cloudcodec.ChatNativeOptionNames() {
			d.protectField(o, key, "host:root")
		}
	}
	if value, exists := o["service_tier"]; exists {
		d.protectAt(value, wireIdentity("", "control", "service_tier"))
	}
	if err := d.tools(o["tools"], p); err != nil {
		return nil, err
	}
	// Stop sequences and schema strings affect parsing/validation and cannot
	// be safely rewritten. Explicitly bound this engineering subset.
	for _, key := range []string{"stop", "stop_sequences"} {
		if value, exists := o[key]; exists && !wireEmpty(value) {
			return nil, fail("wire_unsupported")
		}
	}
	for _, key := range []string{"response_format", "text", "output_config"} {
		if err := d.formatDescriptions(o[key]); err != nil {
			return nil, err
		}
	}
	switch p {
	case Chat:
		for index, value := range o["messages"].([]any) {
			m := value.(map[string]any)
			hostPath := "host:message/" + strconv.Itoa(index) + "/" + strconv.Quote(m["role"].(string))
			// Fixed WorkBuddy source expands Agent.name and identity/provider
			// accounting into these checked message fields. All strings and
			// dynamic keys are scanned, including vendor rawUsage extensions;
			// modifying any of them would change identity or accounting.
			for _, key := range cloudcodec.ChatHostFieldNames() {
				if !cloudcodec.ChatHostTextField(key) {
					d.protectField(m, key, hostPath)
				}
			}
			if err := d.content(m, "content", p); err != nil {
				return nil, err
			}
			if err := d.field(m, "reasoning_content"); err != nil {
				return nil, err
			}
			for _, key := range cloudcodec.ChatHostFieldNames() {
				if cloudcodec.ChatHostTextField(key) {
					if err := d.field(m, key); err != nil {
						return nil, err
					}
				}
			}
			if calls, ok := m["tool_calls"].([]any); ok {
				for _, call := range calls {
					if err := d.arguments(call.(map[string]any)["function"].(map[string]any), "arguments"); err != nil {
						return nil, err
					}
				}
			}
		}
	case Responses:
		if err := d.field(o, "instructions"); err != nil {
			return nil, err
		}
		if s, ok := o["input"].(string); ok {
			o["input"] = d.text(s)
		} else {
			for _, item := range o["input"].([]any) {
				if err := d.responseItem(item.(map[string]any), true); err != nil {
					return nil, err
				}
			}
		}
	case Anthropic:
		if err := d.content(o, "system", p); err != nil {
			return nil, err
		}
		for _, value := range o["messages"].([]any) {
			m := value.(map[string]any)
			if err := d.formatDescriptions(m["output_config"]); err != nil {
				return nil, err
			}
			if err := d.content(m, "content", p); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fail("wire_unsupported")
	}
	if d.err != nil {
		return nil, d.err
	}
	d.identify(d.root, wireIdentity("", "root", string(p)))
	return d, nil
}

// Each path step retains one digest. Dynamic business keys can be large; their
// bytes must never be copied into every descendant's retained cache identity.
func wireIdentity(parent, kind, value string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, parent)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, kind)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, value)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Include text location and semantic controls in history cache identity. The
// same text moved from a user message to a tool result must be scanned again.
func (d *textDocument) identify(v any, path string) {
	switch x := v.(type) {
	case *wireText:
		d.identities[x.index] = path
	case *wireArguments:
		d.identify(x.root, wireIdentity(path, "arguments", ""))
	case *wireValues:
		path = wireIdentity(path, "values", x.digest)
		for _, index := range x.protected {
			d.identities[index] = wireIdentity(path, "protected-value", d.identities[index])
		}
		d.identify(x.root, path)
	case []any:
		for i, value := range x {
			d.identify(value, wireIdentity(path, "index", strconv.Itoa(i)))
		}
	case map[string]any:
		controls := map[string]any{}
		for _, key := range []string{"role", "type", "name", "id", "call_id", "tool_call_id", "tool_use_id"} {
			if value, exists := x[key]; exists {
				if _, ok := value.(string); ok {
					controls[key] = value
				}
			}
		}
		encoded, _ := json.Marshal(controls)
		path = wireIdentity(path, "controls", string(encoded))
		for key, value := range x {
			d.identify(value, wireIdentity(path, "property", key))
		}
	}
}

func wireEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func (d *textDocument) tools(v any, p Protocol) error {
	if d.err != nil {
		return d.err
	}
	if v == nil {
		return nil
	}
	a, ok := v.([]any)
	if !ok {
		return fail("wire_unsupported")
	}
	for _, value := range a {
		t, ok := value.(map[string]any)
		if !ok {
			return fail("wire_unsupported")
		}
		if p == Chat {
			t, ok = t["function"].(map[string]any)
			if !ok {
				return fail("wire_unsupported")
			}
		} else if p == Responses {
			switch t["type"] {
			case "function":
			case "namespace":
				if err := d.tools(t["tools"], p); err != nil {
					return err
				}
			default:
				return fail("wire_unsupported")
			}
		}
		if err := d.field(t, "description"); err != nil {
			return err
		}
		for _, key := range []string{"parameters", "input_schema"} {
			d.protect(t[key])
		}
	}
	return d.err
}

// Schema trees, including property names and const/enum values, are preserved.
// Only the protocol's description beside the schema is ordinary prompt text.
func (d *textDocument) formatDescriptions(v any) error {
	if d.err != nil {
		return d.err
	}
	if o, ok := v.(map[string]any); ok {
		if err := d.field(o, "description"); err != nil {
			return err
		}
		d.protect(o["schema"])
		for _, key := range []string{"json_schema", "format"} {
			if err := d.formatDescriptions(o[key]); err != nil {
				return err
			}
		}
	}
	return d.err
}
