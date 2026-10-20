package cloudcodec

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

const (
	// sigMark begins the signature in an id: __ts<n>__ for the signature's
	// own bytes, __tsr<n>__ for one that isn't canonical base64, its text.
	// Ids magpie gave before #1536 have no n (__ts__, __tsr__); they are
	// still read, with nothing to check them by.
	SigMark = "__ts"
	SigRaw  = "r"
	SigEnd  = "__"
)

// signedID is a tool call's id carrying its thought signature.
func SignedID(id, sig string) string {
	if sig == "" {
		return id
	}
	if id == "" {
		id = "call_" + NewID()
	}
	kind, enc := SigRaw, base64.RawURLEncoding.EncodeToString([]byte(sig))
	if b, err := base64.StdEncoding.DecodeString(sig); err == nil && base64.StdEncoding.EncodeToString(b) == sig {
		kind, enc = "", base64.RawURLEncoding.EncodeToString(b)
	}
	return id + SigMark + kind + strconv.Itoa(len(enc)) + SigEnd + enc
}

// unsignedID splits an id signedID made into the call's own id and its
// signature; any other id, or one whose signature was cut short, comes
// back as it is, with no signature.
func UnsignedID(id string) (string, string) {
	for at := 1; at < len(id); {
		i := strings.Index(id[at:], SigMark)
		if i < 0 {
			break
		}
		i += at
		at = i + 1
		rest := id[i+len(SigMark):]
		raw := strings.HasPrefix(rest, SigRaw)
		if raw {
			rest = rest[len(SigRaw):]
		}
		d := 0
		for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		enc, ok := strings.CutPrefix(rest[d:], SigEnd)
		if !ok {
			continue // not a mark: part of the call's own id
		}
		// the first mark decides: a signature cut short is none
		if d > 0 && rest[:d] != strconv.Itoa(len(enc)) {
			return id, ""
		}
		b, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil || len(b) == 0 {
			return id, ""
		}
		if raw {
			return id[:i], string(b)
		}
		return id[:i], base64.StdEncoding.EncodeToString(b)
	}
	return id, ""
}

// unsignCalls takes the signatures off a parsed request's call ids: a tool
// call keeps its own id and has the signature as its Signature, a result
// answers that id. Messages are copied before they change.
func UnsignCalls(r *Request) {
	for i, m := range r.Messages {
		var parts []Part
		for j, p := range m.Parts {
			switch p.Kind {
			case ToolCall:
				id, sig := UnsignedID(p.ID)
				if sig == "" {
					continue
				}
				p.ID = id
				if p.Signature == "" {
					p.Signature = sig
				}
			case ToolResult:
				id, sig := UnsignedID(p.CallID)
				if sig == "" {
					continue
				}
				p.CallID = id
			default:
				continue
			}
			if parts == nil {
				parts = slices.Clone(m.Parts)
			}
			parts[j] = p
		}
		if parts != nil {
			r.Messages[i].Parts = parts
		}
	}
}

// googleSignature is the thought signature in a tool call's extra_content.
func GoogleSignature(extra json.RawMessage) string {
	if len(extra) == 0 {
		return ""
	}
	var v struct {
		Google struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
	}
	json.Unmarshal(extra, &v)
	return v.Google.ThoughtSignature
}

// googleExtra is extra_content carrying a thought signature.
func GoogleExtra(sig string) map[string]any {
	return map[string]any{"google": map[string]any{"thought_signature": sig}}
}
