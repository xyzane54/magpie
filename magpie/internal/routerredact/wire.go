package routerredact

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// textDocument owns its parsed tree. Markers are private and render never
// mutates it, so a retry cannot accidentally render the caller's raw bytes.
type textDocument struct {
	root       any
	texts      []string
	identities []string
	protected  []int
	textBytes  int
	byteLimit  int
	err        error
}

type wireText struct{ index int }
type wireArguments struct{ root any }
type wireValues struct {
	root      any
	protected []int
	digest    string
}

func (d *textDocument) text(s string) any {
	return d.collect(s, "", false)
}

func (d *textDocument) collect(s, identity string, protected bool) any {
	if d.err != nil {
		return nil
	}
	limit := d.byteLimit
	if limit == 0 {
		limit = 8 << 20
	}
	if len(d.texts) >= 8192 || len(s) > limit-d.textBytes {
		d.err = fail("redaction_input_limit")
		return nil
	}
	n := &wireText{index: len(d.texts)}
	d.texts = append(d.texts, s)
	d.identities = append(d.identities, identity)
	d.textBytes += len(s)
	if protected {
		d.protected = append(d.protected, n.index)
	}
	return n
}

func (d *textDocument) field(o map[string]any, key string) error {
	if d.err != nil {
		return d.err
	}
	v, exists := o[key]
	if !exists || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fail("wire_unsupported")
	}
	o[key] = d.text(s)
	return d.err
}

func (d *textDocument) strings(v any) any {
	if d.err != nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return d.text(x)
	case []any:
		for i := range x {
			if d.err != nil {
				return v
			}
			x[i] = d.strings(x[i])
		}
	case map[string]any:
		if len(x) > 8192-len(d.texts) {
			d.err = fail("redaction_input_limit")
			return v
		}
		// A map's randomized iteration must not control entity numbering.
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if d.err != nil {
				return v
			}
			d.protectAt(key, wireIdentity("", "argument-key", key))
			x[key] = d.strings(x[key])
		}
	}
	return v
}

func (d *textDocument) values(v any) any {
	if d.err != nil {
		return v
	}
	encoded, _ := json.Marshal(v)
	start := len(d.protected)
	root := d.strings(v)
	return &wireValues{
		root:      root,
		protected: append([]int(nil), d.protected[start:]...),
		digest:    fmt.Sprintf("%x", sha256.Sum256(encoded)),
	}
}

func (d *textDocument) arguments(o map[string]any, key string) error {
	if d.err != nil {
		return d.err
	}
	s, ok := o[key].(string)
	if !ok {
		return fail("wire_invalid")
	}
	v, err := wireDecode([]byte(s), 8<<20)
	if err != nil {
		return err
	}
	if _, ok := v.(map[string]any); !ok {
		return fail("wire_invalid")
	}
	o[key] = &wireArguments{root: d.values(v)}
	return d.err
}

func wireMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func (d *textDocument) render(replacements []string) ([]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	if len(replacements) != len(d.texts) {
		return nil, fail("wire_invalid")
	}
	for _, index := range d.protected {
		if replacements[index] != d.texts[index] {
			return nil, fail("wire_unsupported")
		}
	}
	v, err := wireRender(d.root, replacements)
	if err != nil {
		return nil, err
	}
	b, err := wireMarshal(v)
	if err != nil || len(b) > 32<<20 {
		return nil, fail("wire_invalid")
	}
	return b, nil
}

func wireRender(v any, replacements []string) (any, error) {
	switch x := v.(type) {
	case *wireText:
		if x.index < 0 || x.index >= len(replacements) || !utf8.ValidString(replacements[x.index]) {
			return nil, fail("wire_invalid")
		}
		return replacements[x.index], nil
	case *wireArguments:
		a, err := wireRender(x.root, replacements)
		if err != nil {
			return nil, err
		}
		b, err := wireMarshal(a)
		if err != nil {
			return nil, fail("wire_invalid")
		}
		return string(b), nil
	case *wireValues:
		return wireRender(x.root, replacements)
	case map[string]any:
		o := make(map[string]any, len(x))
		for key, value := range x {
			r, err := wireRender(value, replacements)
			if err != nil {
				return nil, err
			}
			o[key] = r
		}
		return o, nil
	case []any:
		a := make([]any, len(x))
		for i, value := range x {
			r, err := wireRender(value, replacements)
			if err != nil {
				return nil, err
			}
			a[i] = r
		}
		return a, nil
	default:
		return v, nil
	}
}
