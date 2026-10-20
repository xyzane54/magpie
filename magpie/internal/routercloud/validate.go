package routercloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errInput = errors.New("unsupported or invalid model input")

// ValidateInput accepts the stateless text and function-tool subset shared by
// the gateway's Chat, Responses and Anthropic parsers. It must run before any
// outbound request. Unknown fields are rejected rather than silently discarded
// by protocol conversion. Tool schemas and argument objects are opaque data,
// not protocol content: a property named "image" is an ordinary property.
// Multimodal blocks and server-side state are outside this subset. Checked
// Responses tools, host metadata, caching and opaque reasoning can be forwarded
// natively; the codec separately rejects lossy conversion. Errors contain no
// caller-supplied data.
func ValidateInput(protocol string, body []byte) error {
	if len(body) > 8<<20 || !utf8.Valid(body) {
		return errInput
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := inputValue(d)
	if err != nil {
		return errInput
	}
	if _, err = d.Token(); err != io.EOF {
		return errInput
	}
	var valid bool
	switch protocol {
	case "chat":
		valid = chatInput(v)
	case "responses":
		valid = responsesInput(v)
	case "anthropic":
		valid = anthropicInput(v)
	}
	if !valid {
		return errInput
	}
	return nil
}

// inputValue also rejects duplicate keys: validation and the eventual parser
// must not disagree about which occurrence controls a field.
func inputValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		o := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, ok := key.(string)
			if !ok {
				return nil, errInput
			}
			if _, exists := o[k]; exists {
				return nil, errInput
			}
			o[k], err = inputValue(d)
			if err != nil {
				return nil, err
			}
		}
		_, err = d.Token()
		return o, err
	case json.Delim('['):
		var a []any
		for d.More() {
			v, err := inputValue(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	default:
		return t, nil
	}
}

type inputCheck func(any) bool

func inputObject(v any, fields map[string]inputCheck, required ...string) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range required {
		if _, ok := o[k]; !ok {
			return false
		}
	}
	for k, v := range o {
		check := fields[k]
		if check == nil || !check(v) {
			return false
		}
	}
	return true
}

func inputString(v any) bool { _, ok := v.(string); return ok }
func inputName(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}
func inputBool(v any) bool  { _, ok := v.(bool); return ok }
func inputFalse(v any) bool { return v == false }
func inputNull(v any) bool  { return v == nil }
func inputNumber(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}
func inputPositiveInt(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil && i > 0 && i <= 2147483647
}
func inputMap(v any) bool { _, ok := v.(map[string]any); return ok }
func inputStringMap(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, value := range o {
		if !inputString(value) {
			return false
		}
	}
	return true
}
func inputEnum(values ...string) inputCheck {
	return func(v any) bool {
		for _, s := range values {
			if v == s {
				return true
			}
		}
		return false
	}
}
func inputArray(check inputCheck, nonempty bool) inputCheck {
	return func(v any) bool {
		a, ok := v.([]any)
		if !ok || (nonempty && len(a) == 0) {
			return false
		}
		for _, x := range a {
			if !check(x) {
				return false
			}
		}
		return true
	}
}
func inputStrings(v any) bool    { return inputArray(inputString, false)(v) }
func inputStop(v any) bool       { return inputString(v) || inputStrings(v) }
func inputEmptyArray(v any) bool { a, ok := v.([]any); return ok && len(a) == 0 }
func inputEffort(v any) bool {
	return inputEnum("none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra")(v)
}
func inputArgs(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	// Function arguments form a second JSON boundary. Apply the outer body's
	// duplicate-key checks here too, before any parser can normalize the value.
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	a, err := inputValue(d)
	if err != nil || !inputMap(a) {
		return false
	}
	_, err = d.Token()
	return err == io.EOF
}
