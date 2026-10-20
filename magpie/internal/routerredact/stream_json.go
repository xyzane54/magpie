package routerredact

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const streamPlaceholderLimit = 128
const streamOutputLimit = 8 << 20

// streamText retains only a possible placeholder suffix, never the full reply.
type streamText struct {
	mapping map[string]string
	pending string
	failed  bool
}

func (t *streamText) feed(fragment string, final bool) string {
	in := t.pending + fragment
	t.pending = ""
	var out strings.Builder
	for len(in) > 0 {
		i := strings.IndexByte(in, '<')
		if i < 0 {
			out.WriteString(in)
			break
		}
		out.WriteString(in[:i])
		in = in[i:]
		end := strings.IndexByte(in, '>')
		if end < 0 && len(in) < streamPlaceholderLimit && !final {
			possible := false
			for key := range t.mapping {
				if strings.HasPrefix(key, in) {
					possible = true
					break
				}
			}
			if possible {
				t.pending = in
				break
			}
		}
		if end >= 0 && end < streamPlaceholderLimit {
			if original, ok := t.mapping[in[:end+1]]; ok {
				if out.Len()+len(original) > streamOutputLimit {
					t.failed = true
					return ""
				}
				out.WriteString(original)
				in = in[end+1:]
				continue
			}
		}
		out.WriteByte(in[0])
		in = in[1:]
	}
	return out.String()
}

type streamJSONFrame struct {
	kind     byte
	want     byte
	keys     map[string]bool
	keyBytes int
}

// streamJSON is an incremental JSON object grammar. Its decoded value strings
// pass through streamText; keys, scalars, whitespace and structure keep meaning.
// A string/argument can be arbitrarily long without retaining it in memory.
type streamJSON struct {
	text                         streamText
	stack                        []streamJSONFrame
	started, done, inString, key bool
	escape, keyText, scalar      string
	keyBytes, keyCount           int
}

func (j *streamJSON) valueAllowed() bool {
	if len(j.stack) == 0 {
		return !j.started
	}
	w := j.stack[len(j.stack)-1].want
	return w == 'v' || w == 'a'
}

func (j *streamJSON) valueDone() {
	if len(j.stack) == 0 {
		j.done = true
	} else {
		j.stack[len(j.stack)-1].want = ','
	}
}

func streamQuotedFragment(s string) string {
	b, _ := wireMarshal(s)
	return string(b[1 : len(b)-1])
}

func (j *streamJSON) stringRune(s string, out *strings.Builder) error {
	if j.key {
		j.keyText += s
		if len(j.keyText) > 4096 {
			return fail("stream_limit")
		}
		out.WriteString(streamQuotedFragment(s))
	} else {
		value := streamQuotedFragment(j.text.feed(s, false))
		if j.text.failed || out.Len()+len(value) > streamOutputLimit {
			return fail("stream_limit")
		}
		out.WriteString(value)
	}
	return nil
}

func (j *streamJSON) finishScalar() error {
	if j.scalar == "" {
		return nil
	}
	if !json.Valid([]byte(j.scalar)) || j.scalar[0] == '"' || j.scalar[0] == '{' || j.scalar[0] == '[' {
		return fail("stream_invalid_json")
	}
	j.scalar = ""
	j.valueDone()
	return nil
}

func (j *streamJSON) feed(fragment string, final bool) (string, error) {
	var out strings.Builder
	for pos := 0; pos < len(fragment); {
		c := fragment[pos]
		if j.inString {
			if j.escape != "" {
				j.escape += string(c)
				pos++
				need := 2
				if len(j.escape) >= 2 && j.escape[1] == 'u' {
					need = 6
					if len(j.escape) >= 6 {
						n, e := strconv.ParseUint(j.escape[2:6], 16, 16)
						if e != nil || n >= 0xdc00 && n <= 0xdfff {
							return "", fail("stream_invalid_json")
						}
						if n >= 0xd800 && n <= 0xdbff {
							need = 12
							if len(j.escape) >= 12 {
								low, err := strconv.ParseUint(j.escape[8:12], 16, 16)
								if j.escape[6:8] != "\\u" || err != nil || low < 0xdc00 || low > 0xdfff {
									return "", fail("stream_invalid_json")
								}
							}
						}
					}
				}
				if len(j.escape) < need {
					continue
				}
				var decoded string
				if json.Unmarshal([]byte("\""+j.escape+"\""), &decoded) != nil {
					return "", fail("stream_invalid_json")
				}
				j.escape = ""
				if err := j.stringRune(decoded, &out); err != nil {
					return "", err
				}
				continue
			}
			if c == '\\' {
				j.escape = "\\"
				pos++
				continue
			}
			if c == '"' {
				pos++
				j.inString = false
				if j.key {
					frame := &j.stack[len(j.stack)-1]
					if frame.keys[j.keyText] || len(frame.keys) >= 4096 {
						return "", fail("stream_invalid_json")
					}
					frame.keys[j.keyText] = true
					frame.keyBytes += len(j.keyText)
					j.keyBytes += len(j.keyText)
					j.keyCount++
					if j.keyBytes > 65536 || j.keyCount > 4096 {
						return "", fail("stream_limit")
					}
					j.keyText = ""
					frame.want = ':'
				} else {
					out.WriteString(streamQuotedFragment(j.text.feed("", true)))
					j.valueDone()
				}
				out.WriteByte('"')
				continue
			}
			if c < 0x20 {
				return "", fail("stream_invalid_json")
			}
			r, size := utf8.DecodeRuneInString(fragment[pos:])
			if r == utf8.RuneError && size == 1 {
				return "", fail("stream_invalid_json")
			}
			if err := j.stringRune(fragment[pos:pos+size], &out); err != nil {
				return "", err
			}
			pos += size
			continue
		}
		if j.scalar != "" {
			if strings.ContainsRune(" \t\r\n,}]", rune(c)) {
				if err := j.finishScalar(); err != nil {
					return "", err
				}
				continue
			}
			j.scalar += string(c)
			if len(j.scalar) > 128 {
				return "", fail("stream_limit")
			}
			out.WriteByte(c)
			pos++
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			out.WriteByte(c)
			pos++
			continue
		}
		if j.done {
			return "", fail("stream_invalid_json")
		}
		if !j.started && c != '{' {
			return "", fail("stream_invalid_json")
		}
		if c == '"' {
			j.key = len(j.stack) > 0 && j.stack[len(j.stack)-1].kind == '{' && (j.stack[len(j.stack)-1].want == 'k' || j.stack[len(j.stack)-1].want == 'K')
			if !j.key && !j.valueAllowed() {
				return "", fail("stream_invalid_json")
			}
			j.inString = true
		} else if c == '{' || c == '[' {
			if !j.valueAllowed() || len(j.stack) >= 64 || !j.started && c != '{' {
				return "", fail("stream_invalid_json")
			}
			j.started = true
			frame := streamJSONFrame{kind: c, want: 'a'}
			if c == '{' {
				frame.want, frame.keys = 'k', make(map[string]bool)
			}
			j.stack = append(j.stack, frame)
		} else if c == '}' || c == ']' {
			if len(j.stack) == 0 {
				return "", fail("stream_invalid_json")
			}
			f := j.stack[len(j.stack)-1]
			if c == '}' && (f.kind != '{' || f.want != 'k' && f.want != ',') || c == ']' && (f.kind != '[' || f.want != 'a' && f.want != ',') {
				return "", fail("stream_invalid_json")
			}
			j.stack = j.stack[:len(j.stack)-1]
			j.keyBytes -= f.keyBytes
			j.keyCount -= len(f.keys)
			j.valueDone()
		} else if c == ':' {
			if len(j.stack) == 0 || j.stack[len(j.stack)-1].want != ':' {
				return "", fail("stream_invalid_json")
			}
			j.stack[len(j.stack)-1].want = 'v'
		} else if c == ',' {
			if len(j.stack) == 0 || j.stack[len(j.stack)-1].want != ',' {
				return "", fail("stream_invalid_json")
			}
			f := &j.stack[len(j.stack)-1]
			f.want = 'v'
			if f.kind == '{' {
				f.want = 'K'
			}
		} else {
			if !j.valueAllowed() || !j.started {
				return "", fail("stream_invalid_json")
			}
			j.scalar = string(c)
		}
		out.WriteByte(c)
		pos++
	}
	if final {
		if err := j.finishScalar(); err != nil {
			return "", err
		}
		if j.inString || len(j.stack) != 0 || !j.done {
			return "", fail("stream_invalid_json")
		}
	}
	return out.String(), nil
}
