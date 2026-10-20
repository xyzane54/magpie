package routerhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
)

const codexProvider = "api_router"

// BuildFiles reads only the explicitly selected host configuration directory.
// It returns a byte-level plan and never writes files or imports credentials.
func BuildFiles(o Options) ([]FilePlan, error) {
	if err := validateRoot(o.ConfigRoot); err != nil {
		return nil, err
	}
	a, contract, err := (Engine{}).adapter(o.Host)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.Model) == "" {
		return nil, errors.New("host model is required")
	}
	s, err := (Engine{}).readSnapshot(o.ConfigRoot, FilePlan{Name: contract.Files[0].Name})
	if err != nil {
		return nil, err
	}
	return a.Build(o, []FilePlan{s.FilePlan})
}

func hostBaseURL(host, raw string) (string, error) {
	if host != "codex" && host != "claude-code" {
		return "", errors.New("unsupported host; use codex or claude-code")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", errors.New("router URL must be an HTTP(S) origin without credentials, query or fragment")
	}
	path := strings.TrimRight(u.Path, "/")
	expected := "/app/" + host + "/v1"
	if path != "" && path != expected {
		return "", errors.New("router URL must be an origin or this host's /app/<host>/v1 endpoint")
	}
	u.Path = expected
	if host == "claude-code" {
		u.Path = strings.TrimSuffix(expected, "/v1")
	}
	return u.String(), nil
}

func hostFields(host string) [][]string {
	if host == "claude-code" {
		return [][]string{{"env", "ANTHROPIC_BASE_URL"}, {"env", "ANTHROPIC_AUTH_TOKEN"}, {"env", "ANTHROPIC_MODEL"}}
	}
	return [][]string{{"model"}, {"model_provider"}, {"model_providers", codexProvider, "name"}, {"model_providers", codexProvider, "base_url"}, {"model_providers", codexProvider, "wire_api"}, {"model_providers", codexProvider, "experimental_bearer_token"}, {"model_providers", codexProvider, "requires_openai_auth"}, {"model_providers", codexProvider, "supports_websockets"}}
}

func buildCodex(raw []byte, o Options, base string) ([]byte, error) {
	doc, err := readTOML(raw)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"env_key", "auth", "http_headers", "env_http_headers", "query_params"} {
		if _, ok := lookup(doc, []string{"model_providers", codexProvider, key}); ok {
			return nil, errors.New("Codex api_router provider has conflicting authentication or request overrides; rename that provider before applying")
		}
	}
	fields := hostFields("codex")
	values := []any{o.Model, codexProvider, "API Router", base, "responses", PlaceholderAPIKey, false, false}
	out := append([]byte(nil), raw...)
	for i, p := range fields {
		out, err = patchTOML(out, p, values[i], false)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func buildClaude(raw []byte, o Options, base string) ([]byte, error) {
	doc, err := readJSON(raw)
	if err != nil {
		return nil, err
	}
	if env, ok := doc["env"]; ok {
		if _, ok := env.(map[string]any); !ok {
			return nil, errors.New("Claude Code env must be an object; repair settings.json before applying")
		}
	}
	fields := hostFields("claude-code")
	values := []string{base, PlaceholderAPIKey, o.Model}
	kvs := make([]edit.KV, 0, len(fields))
	for i, p := range fields {
		if old, ok := lookup(doc, p); ok {
			if _, ok := old.(string); !ok {
				return nil, errors.New("Claude Code connection fields must be strings; repair settings.json before applying")
			}
		}
		kvs = append(kvs, edit.KV{Path: strings.Join(p, "."), Value: values[i]})
	}
	out, err := edit.PatchJSON(raw, kvs, nil)
	if err != nil {
		return nil, errors.New("cannot safely edit Claude Code settings.json; repair the configuration")
	}
	return out, nil
}

// RestoreContent restores only fields this operation actually changed. A field
// with a later user value is a conflict, even if other fields are restorable.
// nil means that a file created by the operation can safely be removed.
func RestoreContent(host string, f FilePlan, current []byte) ([]byte, error) {
	if host != "codex" && host != "claude-code" {
		return nil, errors.New("unsupported host in restore record")
	}
	if bytes.Equal(current, f.Before) {
		return current, nil
	}
	var before, after, now map[string]any
	read := readTOML
	if host == "claude-code" {
		read = readJSON
	}
	var err error
	if before, err = read(f.Before); err != nil {
		return nil, errors.New("restore record contains invalid original configuration; recover the backup record")
	}
	if after, err = read(f.After); err != nil {
		return nil, errors.New("restore record contains invalid applied configuration; recover the backup record")
	}
	if now, err = read(current); err != nil {
		return nil, errors.New("current host configuration is damaged; repair it before restoring")
	}
	if bytes.Equal(current, f.After) {
		return f.Before, nil
	}
	out := append([]byte(nil), current...)
	for _, p := range hostFields(host) {
		b, bok := lookup(before, p)
		a, aok := lookup(after, p)
		c, cok := lookup(now, p)
		if equalValue(b, bok, a, aok) || equalValue(b, bok, c, cok) {
			continue
		}
		if !equalValue(a, aok, c, cok) {
			return nil, fmt.Errorf("restore conflict in %s; preserve your value or put back the Router value before retrying", strings.Join(p, "."))
		}
		if host == "codex" {
			out, err = patchTOML(out, p, b, !bok)
		} else {
			var set []edit.KV
			var del []string
			if bok {
				set = []edit.KV{{Path: strings.Join(p, "."), Value: b}}
			} else {
				del = []string{strings.Join(p, ".")}
			}
			out, err = edit.PatchJSON(out, set, nil)
			for _, key := range del {
				if err == nil {
					out, err = deleteJSONField(out, key)
				}
			}
		}
		if err != nil {
			return nil, errors.New("cannot safely restore host fields; repair the configuration before retrying")
		}
	}
	// Remove tool-created empty containers only when they contain no comments.
	if host == "claude-code" {
		_, had := before["env"]
		doc, e := readJSON(out)
		if e != nil {
			return nil, e
		}
		env, ok := doc["env"].(map[string]any)
		if !had && ok && len(env) == 0 && !hasJSONComments(out) {
			out, err = edit.PatchJSON(out, nil, []string{"env"})
		}
	} else {
		out, err = removeEmptyCodexTable(out, before)
	}
	if err != nil {
		return nil, errors.New("cannot safely clean restored host configuration")
	}
	return out, nil
}

func lookup(doc map[string]any, path []string) (any, bool) {
	var value any = doc
	for _, p := range path {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
func equalValue(a any, aok bool, b any, bok bool) bool {
	return aok == bok && (!aok || reflect.DeepEqual(a, b))
}

func readJSON(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	clean := jsonc.ToJSON(raw)
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.UseNumber()
	value, err := uniqueJSONValue(dec)
	if err != nil {
		return nil, errors.New("invalid or ambiguous Claude Code settings.json; repair it before applying")
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, errors.New("invalid Claude Code settings.json; expected one JSON object")
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Claude Code settings.json must contain a JSON object")
	}
	return doc, nil
}

func uniqueJSONValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for dec.More() {
			k, e := dec.Token()
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("invalid key")
			}
			if _, ok = m[key]; ok {
				return nil, errors.New("duplicate key")
			}
			v, e := uniqueJSONValue(dec)
			if e != nil {
				return nil, e
			}
			m[key] = v
		}
		_, err = dec.Token()
		return m, err
	case json.Delim('['):
		var a []any
		for dec.More() {
			v, e := uniqueJSONValue(dec)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		_, err = dec.Token()
		return a, err
	default:
		return t, nil
	}
}

func hasJSONComments(raw []byte) bool {
	// jsonc replaces comment bytes with spaces without moving offsets.
	clean := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	return !bytes.Equal(raw, clean)
}

func readTOML(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("invalid Codex config.toml; repair it before applying")
	}
	if providers, ok := doc["model_providers"]; ok {
		if _, ok := providers.(map[string]any); !ok {
			return nil, errors.New("Codex model_providers must be a table")
		}
	}
	if provider, ok := lookup(doc, []string{"model_providers", codexProvider}); ok {
		if _, ok := provider.(map[string]any); !ok {
			return nil, errors.New("Codex api_router provider must be a table")
		}
	}
	for _, p := range hostFields("codex") {
		if value, ok := lookup(doc, p); ok {
			if len(p) == 3 && (p[2] == "requires_openai_auth" || p[2] == "supports_websockets") {
				if _, ok := value.(bool); !ok {
					return nil, errors.New("Codex provider protocol switches must be booleans")
				}
			} else if _, ok := value.(string); !ok {
				return nil, errors.New("Codex connection fields must be strings")
			}
		}
	}
	return doc, nil
}

// The syntax tree locates only scalar value spans. TOML is decoded for
// validation, never serialized back. All unrelated bytes stay untouched.
type tomlSpan struct{ start, end, lineStart, lineEnd int }
type tomlLayout struct {
	fields     map[string]tomlSpan
	tables     map[string]int
	firstTable int
}

func pathKey(path []string) string { return strings.Join(path, "\x00") }
func nodeKeys(n *unstable.Node) []string {
	var p []string
	for it := n.Key(); it.Next(); {
		p = append(p, string(it.Node().Data))
	}
	return p
}
func scanTOML(raw []byte) (tomlLayout, error) {
	d := tomlLayout{fields: map[string]tomlSpan{}, tables: map[string]int{}, firstTable: len(raw)}
	p := unstable.Parser{KeepComments: true}
	p.Reset(raw)
	var table []string
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = nodeKeys(n)
			if len(table) >= 2 && table[0] == "model_providers" && table[1] == codexProvider && n.Kind == unstable.ArrayTable {
				return d, errors.New("Codex api_router must not be an array table")
			}
			keys := n.Key()
			keys.Next()
			at := int(keys.Node().Raw.Offset)
			start := bytes.LastIndexByte(raw[:at], '\n') + 1
			if start < d.firstTable {
				d.firstTable = start
			}
			d.tables[pathKey(table)] = start
		case unstable.KeyValue:
			full := append(append([]string(nil), table...), nodeKeys(n)...)
			v := n.Value()
			if v.Kind != unstable.String && v.Kind != unstable.Bool {
				continue
			}
			valueRange := v.Raw
			if v.Kind == unstable.Bool {
				valueRange = p.Range(v.Data)
			}
			start, end := int(valueRange.Offset), int(valueRange.Offset+valueRange.Length)
			keys := n.Key()
			keys.Next()
			at := int(keys.Node().Raw.Offset)
			ls := bytes.LastIndexByte(raw[:at], '\n') + 1
			le := len(raw)
			if i := bytes.IndexByte(raw[end:], '\n'); i >= 0 {
				le = end + i + 1
			}
			d.fields[pathKey(full)] = tomlSpan{start, end, ls, le}
		}
	}
	if p.Error() != nil {
		return d, errors.New("cannot safely locate Codex fields")
	}
	return d, nil
}
func patchTOML(raw []byte, path []string, value any, remove bool) ([]byte, error) {
	doc, err := readTOML(raw)
	if err != nil {
		return nil, err
	}
	old, exists := lookup(doc, path)
	if remove && !exists || !remove && exists && reflect.DeepEqual(old, value) {
		return raw, nil
	}
	layout, err := scanTOML(raw)
	if err != nil {
		return nil, err
	}
	var out []byte
	if span, ok := layout.fields[pathKey(path)]; ok {
		if remove {
			// Keep later comments on a Router-created line as standalone comments.
			suffix := raw[span.end:span.lineEnd]
			if bytes.Contains(suffix, []byte("#")) {
				out = spliceBytes(raw, span.lineStart, span.end, nil)
			} else {
				out = spliceBytes(raw, span.lineStart, span.lineEnd, nil)
			}
		} else {
			out = spliceBytes(raw, span.start, span.end, []byte(tomlScalar(value)))
		}
	} else {
		if exists {
			return nil, errors.New("unsupported Codex field syntax; use ordinary scalar keys and tables")
		}
		parent := path[:len(path)-1]
		if len(parent) > 0 {
			if _, has := lookup(doc, parent); has {
				if _, header := layout.tables[pathKey(parent)]; !header {
					return nil, errors.New("unsupported inline or dotted Codex provider; use an ordinary provider table")
				}
			}
		}
		eol := "\n"
		if bytes.Contains(raw, []byte("\r\n")) {
			eol = "\r\n"
		}
		line := path[len(path)-1] + " = " + tomlScalar(value) + eol
		if len(parent) == 0 {
			if layout.firstTable > 0 && raw[layout.firstTable-1] != '\n' {
				line = eol + line
			}
			out = spliceBytes(raw, layout.firstTable, layout.firstTable, []byte(line))
		} else if at, ok := layout.tables[pathKey(parent)]; ok {
			end := len(raw)
			if i := bytes.IndexByte(raw[at:], '\n'); i >= 0 {
				end = at + i + 1
			}
			if end == len(raw) && len(raw) > 0 && raw[len(raw)-1] != '\n' {
				line = eol + line
			}
			out = spliceBytes(raw, end, end, []byte(line))
		} else {
			add := ""
			if len(raw) > 0 && raw[len(raw)-1] != '\n' {
				add += eol
			}
			if len(raw) > 0 {
				add += eol
			}
			add += "[" + strings.Join(parent, ".") + "]" + eol + line
			out = append(append([]byte(nil), raw...), []byte(add)...)
		}
	}
	if _, err = readTOML(out); err != nil {
		return nil, errors.New("cannot safely edit Codex config.toml; normalize the provider table syntax first")
	}
	return out, nil
}
func tomlScalar(v any) string {
	if b, ok := v.(bool); ok {
		return strconv.FormatBool(b)
	}
	raw, _ := json.Marshal(v.(string))
	return string(raw)
}
func spliceBytes(raw []byte, start, end int, value []byte) []byte {
	out := make([]byte, 0, len(raw)-(end-start)+len(value))
	out = append(out, raw[:start]...)
	out = append(out, value...)
	return append(out, raw[end:]...)
}
func removeEmptyCodexTable(raw []byte, before map[string]any) ([]byte, error) {
	if _, had := lookup(before, []string{"model_providers", codexProvider}); had {
		return raw, nil
	}
	doc, err := readTOML(raw)
	if err != nil {
		return nil, err
	}
	value, ok := lookup(doc, []string{"model_providers", codexProvider})
	if !ok {
		return raw, nil
	}
	m, ok := value.(map[string]any)
	if !ok || len(m) != 0 {
		return raw, nil
	}
	layout, err := scanTOML(raw)
	if err != nil {
		return nil, err
	}
	start, ok := layout.tables[pathKey([]string{"model_providers", codexProvider})]
	if !ok {
		return raw, nil
	}
	end := len(raw)
	var starts []int
	for _, at := range layout.tables {
		if at > start {
			starts = append(starts, at)
		}
	}
	sort.Ints(starts)
	if len(starts) > 0 {
		end = starts[0]
	}
	block := raw[start:end]
	if bytes.Contains(block, []byte("#")) {
		return raw, nil
	}
	return spliceBytes(raw, start, end, nil), nil
}

// deleteJSONField removes only the member syntax and one separator. In
// particular comments between the value and comma belong to the user.
func deleteJSONField(raw []byte, path string) ([]byte, error) {
	prefix := []byte(nil)
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		prefix = raw[:3]
		raw = raw[3:]
	}
	stripped := jsonc.ToJSONInPlace(append([]byte(nil), raw...))
	if !json.Valid(stripped) {
		return nil, errors.New("cannot safely remove Claude Code field")
	}
	value := gjson.GetBytes(stripped, path)
	if !value.Exists() {
		return append(append([]byte(nil), prefix...), raw...), nil
	}
	a, b := value.Index, value.Index+len(value.Raw)
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
	for a > 0 && (space(stripped[a-1]) || stripped[a-1] == ':') {
		a--
	}
	if a == 0 || stripped[a-1] != '"' {
		return nil, errors.New("cannot safely locate Claude Code field")
	}
	a--
	for a > 0 && stripped[a-1] != '"' {
		a--
	}
	if a == 0 {
		return nil, errors.New("cannot safely locate Claude Code key")
	}
	a--
	j := b
	for j < len(stripped) && space(stripped[j]) {
		j++
	}
	eol := []byte("\n")
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = []byte("\r\n")
	}
	out := raw
	if j < len(stripped) && stripped[j] == ',' {
		out = spliceBytes(out, j, j+1, nil)
		out = spliceBytes(out, a, b, commentsWithin(raw[a:b], eol))
	} else {
		out = spliceBytes(out, a, b, commentsWithin(raw[a:b], eol))
		k := a
		for k > 0 && space(stripped[k-1]) {
			k--
		}
		if k > 0 && stripped[k-1] == ',' {
			out = spliceBytes(out, k-1, k, nil)
		}
	}
	out = append(append([]byte(nil), prefix...), out...)
	if _, err := readJSON(out); err != nil {
		return nil, errors.New("cannot safely remove Claude Code field")
	}
	return out, nil
}

func commentsWithin(raw, eol []byte) []byte {
	var out []byte
	quoted := false
	for i := 0; i < len(raw); i++ {
		if quoted {
			if raw[i] == '\\' {
				i++
			} else if raw[i] == '"' {
				quoted = false
			}
			continue
		}
		if raw[i] == '"' {
			quoted = true
			continue
		}
		if raw[i] != '/' || i+1 == len(raw) {
			continue
		}
		start := i
		if raw[i+1] == '/' {
			i += 2
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
		} else if raw[i+1] == '*' {
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			i += 2
		} else {
			continue
		}
		comment := raw[start:i]
		if bytes.HasPrefix(comment, []byte("//")) {
			comment = bytes.TrimSuffix(comment, []byte("\r"))
		}
		out = append(out, comment...)
		out = append(out, eol...)
		i--
	}
	return out
}
