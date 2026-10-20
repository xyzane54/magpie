package routerredact

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Schema keys and values are scanned but cannot be rewritten without changing
// validation semantics. A detected entity there makes this request unsupported.
func (d *textDocument) protect(v any) {
	if d.err != nil {
		return
	}
	encoded, _ := json.Marshal(v)
	d.protectAt(v, wireIdentity("", "schema", string(encoded)))
}

// Include the complete field digest in cache identity. Accounting numbers or
// vendor-object shapes can change while their string leaves remain identical.
func (d *textDocument) protectField(o map[string]any, key, path string) {
	if d.err != nil {
		return
	}
	if value, exists := o[key]; exists {
		encoded, _ := json.Marshal(value)
		d.protectAt(value, wireIdentity(wireIdentity("", "host-field", path), key, string(encoded)))
	}
}

func (d *textDocument) protectAt(v any, path string) {
	if d.err != nil {
		return
	}
	switch x := v.(type) {
	case string:
		d.collect(x, path, true)
	case []any:
		for i, value := range x {
			if d.err != nil {
				return
			}
			d.protectAt(value, wireIdentity(path, "index", strconv.Itoa(i)))
		}
	case map[string]any:
		if len(x) > 8192-len(d.texts) {
			d.err = fail("redaction_input_limit")
			return
		}
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if d.err != nil {
				return
			}
			d.protectAt(key, wireIdentity(path, "key", key))
			d.protectAt(x[key], wireIdentity(path, "property", key))
		}
	}
}

func (d *textDocument) content(o map[string]any, key string, p Protocol) error {
	if d.err != nil {
		return d.err
	}
	v, exists := o[key]
	if !exists || v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		o[key] = d.text(s)
		return d.err
	}
	a, ok := v.([]any)
	if !ok {
		return fail("wire_unsupported")
	}
	for _, value := range a {
		if d.err != nil {
			return d.err
		}
		block, ok := value.(map[string]any)
		if !ok {
			return fail("wire_unsupported")
		}
		if p == Chat {
			d.protectField(block, "extra_fields", "host:content")
		}
		switch block["type"] {
		case "text", "input_text", "output_text", "summary_text", "reasoning_text":
			if annotation, exists := block["annotations"]; exists && !wireEmpty(annotation) {
				return fail("wire_unsupported")
			}
			if err := d.field(block, "text"); err != nil {
				return err
			}
		case "refusal":
			if err := d.field(block, "refusal"); err != nil {
				return err
			}
		case "tool_use":
			if p != Anthropic {
				return fail("wire_unsupported")
			}
			if _, ok := block["input"].(map[string]any); !ok {
				return fail("wire_invalid")
			}
			block["input"] = d.values(block["input"])
		case "tool_result":
			if p != Anthropic {
				return fail("wire_unsupported")
			}
			if err := d.content(block, "content", p); err != nil {
				return err
			}
		default:
			return fail("wire_unsupported")
		}
	}
	return d.err
}

func (d *textDocument) responseItem(o map[string]any, request bool) error {
	switch o["type"] {
	case nil, "message":
		return d.content(o, "content", Responses)
	case "function_call":
		return d.arguments(o, "arguments")
	case "function_call_output", "custom_tool_call_output":
		return d.content(o, "output", Responses)
	case "custom_tool_call":
		if request {
			return fail("wire_unsupported")
		}
		return d.field(o, "input")
	case "additional_tools":
		return d.tools(o["tools"], Responses)
	case "reasoning":
		if encrypted, exists := o["encrypted_content"]; exists && !wireEmpty(encrypted) {
			return fail("wire_unsupported")
		}
		if err := d.content(o, "summary", Responses); err != nil {
			return err
		}
		return d.content(o, "content", Responses)
	default:
		return fail("wire_unsupported")
	}
}

// wireDecode bounds both nesting and bytes, retains exact JSON numbers, and
// rejects duplicate keys at every boundary, including stringified arguments.
func wireDecode(body []byte, limit int) (any, error) {
	if len(body) > limit || !utf8.Valid(body) || !wireUnicode(body) {
		return nil, fail("wire_invalid")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := wireValue(d, 0)
	if err != nil {
		return nil, fail("wire_invalid")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fail("wire_invalid")
	}
	return v, nil
}

func wireValue(d *json.Decoder, depth int) (any, error) {
	if depth > 128 {
		return nil, fail("wire_invalid")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		o := map[string]any{}
		for d.More() {
			k, err := d.Token()
			key, ok := k.(string)
			if err != nil || !ok {
				return nil, fail("wire_invalid")
			}
			if _, exists := o[key]; exists {
				return nil, fail("wire_invalid")
			}
			o[key], err = wireValue(d, depth+1)
			if err != nil {
				return nil, err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fail("wire_invalid")
		}
		return o, nil
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := wireValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fail("wire_invalid")
		}
		return a, nil
	case json.Delim('}'), json.Delim(']'):
		return nil, fail("wire_invalid")
	default:
		return t, nil
	}
}

// encoding/json replaces unpaired UTF-16 surrogates. Reject them so byte spans
// can never refer to text different from the caller's Unicode value.
func wireUnicode(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) || b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			lo, err := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if err != nil || lo < 0xdc00 || lo > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
