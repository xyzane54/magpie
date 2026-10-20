package cloudcodec

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeJSON is json.Unmarshal(data, dst), made for a request body: one
// field of a body of several megabytes is read without decoding the rest.
// encoding/json checks the whole input before decoding any of it, and
// steps through every byte of every string a byte at a time, so each read
// of a field of a long conversation cost as much as decoding all of it,
// and a request was read so a dozen times or more on its way (#1532,
// #1654: 0.1–0.3s of CPU at ~300k tokens).
//
// Here data is checked once (jsonValid, the same grammar as json.Valid),
// split at its members and elements without decoding them, and only the
// values dst has a place for are decoded: strings and booleans here,
// json.RawMessage as their bytes, structs, pointers, slices and maps of
// strings by their parts, and every other value by encoding/json itself.
// dst gets what json.Unmarshal gives it. Whatever this doesn't take on as
// json.Unmarshal would — data that isn't valid JSON, a type that decodes
// itself, an embedded field, a ",string" option, a key given twice, a value
// of the wrong type — goes to json.Unmarshal, dst zeroed first, so its
// error, and what it leaves in dst, are encoding/json's own. So does a dst
// that already holds something, which encoding/json decodes into in ways
// of its own (a slice's old elements are decoded over, not replaced).
func DecodeJSON(data []byte, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() || !v.Elem().IsZero() || !JsonValid(data) {
		return json.Unmarshal(data, dst)
	}
	start := SkipJSONSpace(data, 0)
	if close := Misfit(data[start], v.Type().Elem()); close != 0 {
		// An array or object where dst can't take one, as a caller trying
		// a string first and then a list of parts: encoding/json would
		// read all of it to say so. It says the same, at the same offset,
		// of the value's opening and its close.
		return json.Unmarshal(append(data[:start+1:start+1], close), dst)
	}
	if err := FillJSON(TrimJSON(data), v.Elem()); err != nil {
		v.Elem().SetZero()
		return json.Unmarshal(data, dst)
	}
	return nil
}

// misfit is the closing bracket of the array or object that open starts
// when a value of type t can't hold it, or 0.
func Misfit(open byte, t reflect.Type) byte {
	for {
		if t == RawMessageType || t.Implements(JsonUnmarshalerType) || t.Implements(TextUnmarshalerType) ||
			reflect.PointerTo(t).Implements(JsonUnmarshalerType) || reflect.PointerTo(t).Implements(TextUnmarshalerType) {
			return 0
		}
		if t.Kind() != reflect.Pointer {
			break
		}
		t = t.Elem()
	}
	switch k := t.Kind(); {
	case k == reflect.Interface:
	case open == '[' && k != reflect.Slice && k != reflect.Array:
		return ']'
	case open == '{' && k != reflect.Struct && k != reflect.Map:
		return '}'
	}
	return 0
}

// errSlowJSON sends a value to json.Unmarshal (decodeJSON).
var ErrSlowJSON = errors.New("decoded by encoding/json")

var (
	RawMessageType      = reflect.TypeFor[json.RawMessage]()
	JsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	TextUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	StringType          = reflect.TypeFor[string]()
	FastFieldsByType    sync.Map // reflect.Type → *fastFields
	ErrFastFieldsUnfit  = errors.New("struct not decoded field by field")
	JsonNull            = []byte("null")
	JsonTrue, JsonFalse = []byte("true"), []byte("false")
)

// fastFields are the fields of a struct as encoding/json names them.
type FastFields struct {
	names []string
	index []int
	err   error
}

func JsonFieldsOf(t reflect.Type) *FastFields {
	if f, ok := FastFieldsByType.Load(t); ok {
		return f.(*FastFields)
	}
	f := &FastFields{}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.Anonymous {
			f.err = ErrFastFieldsUnfit // promoted fields: encoding/json's
			break
		}
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := bytes.Cut([]byte(tag), []byte(","))
		for o := range bytes.SplitSeq(opts, []byte(",")) {
			if string(o) == "string" {
				f.err = ErrFastFieldsUnfit
			}
		}
		if !ValidTagName(string(name)) {
			name = []byte(sf.Name)
		}
		if slices.Contains(f.names, string(name)) {
			f.err = ErrFastFieldsUnfit // encoding/json picks one, or neither
		}
		f.names = append(f.names, string(name))
		f.index = append(f.index, i)
	}
	FastFieldsByType.Store(t, f)
	return f
}

// validTagName is encoding/json's isValidTag.
func ValidTagName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case bytes.ContainsRune([]byte("!#$%&()*+-./:;<=>?@[]^_{|}~ "), c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

// field is the field of f that key names, as encoding/json picks it: its
// exact name, or else the first whose name matches without case.
func (f *FastFields) field(key []byte) int {
	for i, n := range f.names {
		if n == string(key) {
			return i
		}
	}
	for i, n := range f.names {
		if bytes.EqualFold([]byte(n), key) {
			return i
		}
	}
	return -1
}

// fillJSON decodes raw, a whole valid JSON value, into v; errSlowJSON or
// another error leaves it to encoding/json.
func FillJSON(raw []byte, v reflect.Value) error {
	t := v.Type()
	if t == RawMessageType {
		v.SetBytes(bytes.Clone(raw)) // a copy, as encoding/json makes
		return nil
	}
	if t.Kind() != reflect.Pointer && (t.Implements(JsonUnmarshalerType) || t.Implements(TextUnmarshalerType) ||
		reflect.PointerTo(t).Implements(JsonUnmarshalerType) || reflect.PointerTo(t).Implements(TextUnmarshalerType)) {
		return json.Unmarshal(raw, v.Addr().Interface()) // it decodes itself
	}
	null := bytes.Equal(raw, JsonNull)
	switch t.Kind() {
	case reflect.Pointer:
		if null {
			v.SetZero()
			return nil
		}
		if t.Implements(JsonUnmarshalerType) || t.Implements(TextUnmarshalerType) {
			return json.Unmarshal(raw, v.Addr().Interface())
		}
		if v.IsNil() {
			v.Set(reflect.New(t.Elem()))
		}
		return FillJSON(raw, v.Elem())
	case reflect.String:
		if null {
			return nil
		}
		if raw[0] != '"' {
			return ErrSlowJSON
		}
		s, ok := UnquoteJSON(raw)
		if !ok {
			return ErrSlowJSON
		}
		v.SetString(s)
		return nil
	case reflect.Bool:
		switch {
		case null:
		case bytes.Equal(raw, JsonTrue):
			v.SetBool(true)
		case bytes.Equal(raw, JsonFalse):
			v.SetBool(false)
		default:
			return ErrSlowJSON
		}
		return nil
	case reflect.Struct:
		if null {
			return nil
		}
		if raw[0] != '{' {
			return ErrSlowJSON
		}
		f := JsonFieldsOf(t)
		if f.err != nil {
			return f.err
		}
		var seen []bool
		var err error
		EachMember(raw, func(key, val []byte) bool {
			name, ok := UnquoteJSON(key)
			if !ok {
				err = ErrSlowJSON
				return false
			}
			i := f.field([]byte(name))
			if i < 0 {
				return true
			}
			if seen == nil {
				seen = make([]bool, len(f.names))
			}
			if seen[i] {
				err = ErrSlowJSON // a key twice: encoding/json merges them
				return false
			}
			seen[i] = true
			err = FillJSON(val, v.Field(f.index[i]))
			return err == nil
		})
		return err
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return ErrSlowJSON // base64, or bytes of a type of their own
		}
		if null {
			v.SetZero()
			return nil
		}
		if raw[0] != '[' {
			return ErrSlowJSON
		}
		var elems [][]byte
		EachElement(raw, func(el []byte) bool {
			elems = append(elems, el)
			return true
		})
		s := reflect.MakeSlice(t, len(elems), len(elems))
		for i, el := range elems {
			if err := FillJSON(el, s.Index(i)); err != nil {
				return err
			}
		}
		v.Set(s)
		return nil
	case reflect.Map:
		if t.Key() != StringType {
			return ErrSlowJSON
		}
		if null {
			v.SetZero()
			return nil
		}
		if raw[0] != '{' {
			return ErrSlowJSON
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		var err error
		EachMember(raw, func(key, val []byte) bool {
			name, ok := UnquoteJSON(key)
			if !ok {
				err = ErrSlowJSON
				return false
			}
			el := reflect.New(t.Elem()).Elem()
			if err = FillJSON(val, el); err != nil {
				return false
			}
			v.SetMapIndex(reflect.ValueOf(name), el)
			return true
		})
		return err
	}
	// numbers, interfaces and the rest
	return json.Unmarshal(raw, v.Addr().Interface())
}

func IsJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func SkipJSONSpace(data []byte, i int) int {
	const spaces = 0x2020202020202020 // a padded body's runs of spaces, 8 at a time
	for i+8 <= len(data) && binary.LittleEndian.Uint64(data[i:]) == spaces {
		i += 8
	}
	for i < len(data) && IsJSONSpace(data[i]) {
		i++
	}
	return i
}

func TrimJSON(data []byte) []byte {
	i := SkipJSONSpace(data, 0)
	j := len(data)
	for j-8 >= i && binary.LittleEndian.Uint64(data[j-8:]) == 0x2020202020202020 {
		j -= 8
	}
	for j > i && IsJSONSpace(data[j-1]) {
		j--
	}
	return data[i:j]
}

// eachMember calls f with each member of obj, a valid JSON object, its key
// still quoted, until f returns false.
func EachMember(obj []byte, f func(key, val []byte) bool) {
	i := SkipJSONSpace(obj, 1)
	if obj[i] == '}' {
		return
	}
	for {
		k := i
		i = SkipJSONString(obj, i)
		key := obj[k:i]
		i = SkipJSONSpace(obj, i)
		i = SkipJSONSpace(obj, i+1) // ':'
		s := i
		i = SkipJSONValue(obj, i)
		if !f(key, obj[s:i:i]) {
			return
		}
		i = SkipJSONSpace(obj, i)
		if obj[i] != ',' {
			return
		}
		i = SkipJSONSpace(obj, i+1)
	}
}

// eachElement calls f with each element of arr, a valid JSON array, until
// f returns false.
func EachElement(arr []byte, f func(el []byte) bool) {
	i := SkipJSONSpace(arr, 1)
	if arr[i] == ']' {
		return
	}
	for {
		s := i
		i = SkipJSONValue(arr, i)
		if !f(arr[s:i:i]) {
			return
		}
		i = SkipJSONSpace(arr, i)
		if arr[i] != ',' {
			return
		}
		i = SkipJSONSpace(arr, i+1)
	}
}

// skipJSONString is the end of the valid JSON string at i.
func SkipJSONString(data []byte, i int) int {
	i++
	for {
		j := bytes.IndexByte(data[i:], '"')
		if j < 0 {
			return len(data)
		}
		i += j
		n := 0
		for k := i - 1; data[k] == '\\'; k-- {
			n++
		}
		i++
		if n%2 == 0 {
			return i
		}
	}
}

// skipJSONValue is the end of the valid JSON value at i.
func SkipJSONValue(data []byte, i int) int {
	switch data[i] {
	case '"':
		return SkipJSONString(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				i = SkipJSONString(data, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	}
	for i < len(data) {
		switch c := data[i]; {
		case c == ',' || c == '}' || c == ']' || IsJSONSpace(c):
			return i
		}
		i++
	}
	return i
}

// unquoteJSON is the string a valid JSON string literal holds, as
// encoding/json decodes it (unquoteBytes): invalid UTF-8 and lone
// surrogates become U+FFFD.
func UnquoteJSON(q []byte) (string, bool) {
	if len(q) < 2 || q[0] != '"' || q[len(q)-1] != '"' {
		return "", false
	}
	s := q[1 : len(q)-1]
	r := 0
	for r < len(s) {
		c := s[r]
		if c == '\\' || c == '"' || c < ' ' {
			break
		}
		if c < utf8.RuneSelf {
			r++
			continue
		}
		rr, size := utf8.DecodeRune(s[r:])
		if rr == utf8.RuneError && size == 1 {
			break
		}
		r += size
	}
	if r == len(s) {
		return string(s), true
	}
	b := make([]byte, 0, len(s)+2*utf8.UTFMax)
	b = append(b, s[:r]...)
	for r < len(s) {
		switch c := s[r]; {
		case c == '\\':
			r++
			if r >= len(s) {
				return "", false
			}
			switch s[r] {
			default:
				return "", false
			case '"', '\\', '/', '\'':
				b = append(b, s[r])
				r++
			case 'b':
				b = append(b, '\b')
				r++
			case 'f':
				b = append(b, '\f')
				r++
			case 'n':
				b = append(b, '\n')
				r++
			case 'r':
				b = append(b, '\r')
				r++
			case 't':
				b = append(b, '\t')
				r++
			case 'u':
				r--
				rr := Hex4(s[r:])
				if rr < 0 {
					return "", false
				}
				r += 6
				if utf16.IsSurrogate(rr) {
					if dec := utf16.DecodeRune(rr, Hex4(s[r:])); dec != unicode.ReplacementChar {
						r += 6
						b = utf8.AppendRune(b, dec)
						break
					}
					rr = unicode.ReplacementChar
				}
				b = utf8.AppendRune(b, rr)
			}
		case c == '"', c < ' ':
			return "", false
		case c < utf8.RuneSelf:
			b = append(b, c)
			r++
		default:
			rr, size := utf8.DecodeRune(s[r:])
			r += size
			b = utf8.AppendRune(b, rr)
		}
	}
	return string(b), true
}

// hex4 is encoding/json's getu4: the rune of the \uXXXX at s, or -1.
func Hex4(s []byte) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range s[2:6] {
		switch {
		case '0' <= c && c <= '9':
			c = c - '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}

// jsonMaxDepth is encoding/json's maxNestingDepth.
const JsonMaxDepth = 10000

// jsonValid is json.Valid, without stepping through each byte by a call:
// the grammar, and the depth past which encoding/json gives up, are its.
func JsonValid(data []byte) bool {
	i := SkipJSONSpace(data, 0)
	i, ok := ValidJSONValue(data, i, 0)
	return ok && SkipJSONSpace(data, i) == len(data)
}

// jsonStringByte marks the bytes that end a run of a string's plain bytes.
var JsonStringByte = func() (t [256]bool) {
	for c := range 0x20 {
		t[c] = true
	}
	t['"'], t['\\'] = true, true
	return t
}()

func ValidJSONValue(data []byte, i, depth int) (int, bool) {
	if i >= len(data) {
		return i, false
	}
	switch c := data[i]; {
	case c == '"':
		return ValidJSONString(data, i)
	case c == '{':
		if depth+1 > JsonMaxDepth {
			return i, false
		}
		i = SkipJSONSpace(data, i+1)
		if i < len(data) && data[i] == '}' {
			return i + 1, true
		}
		for {
			if i >= len(data) || data[i] != '"' {
				return i, false
			}
			var ok bool
			if i, ok = ValidJSONString(data, i); !ok {
				return i, false
			}
			i = SkipJSONSpace(data, i)
			if i >= len(data) || data[i] != ':' {
				return i, false
			}
			i = SkipJSONSpace(data, i+1)
			if i, ok = ValidJSONValue(data, i, depth+1); !ok {
				return i, false
			}
			i = SkipJSONSpace(data, i)
			if i >= len(data) {
				return i, false
			}
			switch data[i] {
			case '}':
				return i + 1, true
			case ',':
				i = SkipJSONSpace(data, i+1)
			default:
				return i, false
			}
		}
	case c == '[':
		if depth+1 > JsonMaxDepth {
			return i, false
		}
		i = SkipJSONSpace(data, i+1)
		if i < len(data) && data[i] == ']' {
			return i + 1, true
		}
		for {
			var ok bool
			if i, ok = ValidJSONValue(data, i, depth+1); !ok {
				return i, false
			}
			i = SkipJSONSpace(data, i)
			if i >= len(data) {
				return i, false
			}
			switch data[i] {
			case ']':
				return i + 1, true
			case ',':
				i = SkipJSONSpace(data, i+1)
			default:
				return i, false
			}
		}
	case c == 't':
		return i + 4, bytes.HasPrefix(data[i:], JsonTrue)
	case c == 'f':
		return i + 5, bytes.HasPrefix(data[i:], JsonFalse)
	case c == 'n':
		return i + 4, bytes.HasPrefix(data[i:], JsonNull)
	case c == '-' || '0' <= c && c <= '9':
		return ValidJSONNumber(data, i)
	}
	return i, false
}

// stopsString says whether any of the 8 bytes of x ends a run of a
// string's plain bytes: a control byte, a quote or a backslash. It may
// say so of a plain byte after one that does, never miss one.
func StopsString(x uint64) bool {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	quote, slash := x^(ones*'"'), x^(ones*'\\')
	return ((x-ones*0x20)&^x|(quote-ones)&^quote|(slash-ones)&^slash)&highs != 0
}

func ValidJSONString(data []byte, i int) (int, bool) {
	i++
	for i < len(data) {
		for i+8 <= len(data) && !StopsString(binary.LittleEndian.Uint64(data[i:])) {
			i += 8
		}
		if i >= len(data) {
			break
		}
		c := data[i]
		if !JsonStringByte[c] {
			i++
			continue
		}
		switch {
		case c == '"':
			return i + 1, true
		case c < 0x20:
			return i, false
		}
		// a backslash
		if i+1 >= len(data) {
			return i, false
		}
		switch data[i+1] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			i += 2
		case 'u':
			if i+6 > len(data) {
				return i, false
			}
			for _, h := range data[i+2 : i+6] {
				if !('0' <= h && h <= '9' || 'a' <= h && h <= 'f' || 'A' <= h && h <= 'F') {
					return i, false
				}
			}
			i += 6
		default:
			return i, false
		}
	}
	return i, false
}

func IsDigit(c byte) bool { return '0' <= c && c <= '9' }

func ValidJSONNumber(data []byte, i int) (int, bool) {
	if data[i] == '-' {
		i++
	}
	switch {
	case i >= len(data):
		return i, false
	case data[i] == '0':
		i++
	case IsDigit(data[i]):
		for i < len(data) && IsDigit(data[i]) {
			i++
		}
	default:
		return i, false
	}
	if i < len(data) && data[i] == '.' {
		i++
		if i >= len(data) || !IsDigit(data[i]) {
			return i, false
		}
		for i < len(data) && IsDigit(data[i]) {
			i++
		}
	}
	if i < len(data) && (data[i] == 'e' || data[i] == 'E') {
		i++
		if i < len(data) && (data[i] == '+' || data[i] == '-') {
			i++
		}
		if i >= len(data) || !IsDigit(data[i]) {
			return i, false
		}
		for i < len(data) && IsDigit(data[i]) {
			i++
		}
	}
	return i, true
}
