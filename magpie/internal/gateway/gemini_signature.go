package gateway

import (
	"bytes"

	"encoding/json"
)

// Gemini's thought signatures on tool calls (#687). Gemini 3 signs the
// first function call of each step it thinks through, and turns the next
// request away without the signature back on that call: 400 "Function call
// is missing a thought_signature in functionCall parts". Its
// OpenAI-compatible API gives the signature beside the call, in
// tool_calls[].extra_content.google.thought_signature, and takes it back
// there. A Chat client knows no such field (Pi's openai-completions drops
// it, as the AI SDK does), but every client sends a call's id back as it
// was given. So the signature rides in the id the client is given,
// <id>__ts<n>__<signature, base64url>, and comes off it on the way back:
// into extra_content for Gemini, and left out for any other upstream,
// which gets the id the call had.
//
// n is how many characters the signature has in the id. Some clients
// shorten an id they were given (one caps them at 40 characters, #1536),
// and what is left of a signature often still decodes as base64, so only
// n tells a shortened signature from a whole one. A signature that isn't
// whole is none: Gemini answers a partial one with 400 "Corrupted thought
// signature".
//
// A call with no signature to give back — made by another model, or an
// id a client shortened — goes to Gemini with the value Google documents
// for history it didn't sign, skip_thought_signature_validator, as the
// Code Assist path sends every call.

// chatCallSignatures is a Chat request relayed as it is with the
// signatures off its call ids: for Gemini (gemini) each call has its own
// in extra_content, or skip_thought_signature_validator in a turn with
// none; for another upstream the ids are only the calls' own again.
func chatCallSignatures(body []byte, gemini bool) []byte {
	signed := bytes.Contains(body, []byte(sigMark))
	if !signed && !(gemini && bytes.Contains(body, []byte(`"tool_calls"`))) {
		return body
	}
	return editMessages(body, []string{"tool_calls", "tool_call_id"}, func(m map[string]json.RawMessage) bool {
		changed := false
		if raw, ok := m["tool_call_id"]; ok {
			var id string
			if json.Unmarshal(raw, &id) == nil {
				if own, sig := unsignedID(id); sig != "" {
					m["tool_call_id"], _ = marshalPlain(own)
					changed = true
				}
			}
		}
		var calls []map[string]json.RawMessage
		if json.Unmarshal(m["tool_calls"], &calls) != nil || len(calls) == 0 {
			return changed
		}
		sigs := make([]string, len(calls))
		signedStep := false
		for i, c := range calls {
			var id string
			json.Unmarshal(c["id"], &id)
			own, sig := unsignedID(id)
			if sig != "" {
				c["id"], _ = marshalPlain(own)
				changed = true
			}
			if s := googleSignature(c["extra_content"]); s != "" {
				sig = s
			}
			sigs[i] = sig
			signedStep = signedStep || sig != ""
		}
		if gemini {
			for i, c := range calls {
				sig := sigs[i]
				if sig == "" && signedStep {
					continue // a later call of a signed step: Gemini signs the first
				}
				if sig == "" {
					sig = skipSignature
				}
				if googleSignature(c["extra_content"]) == sig {
					continue
				}
				c["extra_content"], _ = marshalPlain(googleExtra(sig))
				changed = true
			}
		}
		if changed {
			m["tool_calls"], _ = marshalPlain(calls)
		}
		return changed
	})
}

// sigTidy gives, in a Chat reply from Gemini relayed as it is, each tool
// call that came with a thought signature an id carrying it (signedID).
// Lines, or a whole reply, pass as they came unless one has a signature.
type sigTidy struct {
	sse bool
	buf []byte
}

var thoughtSignatureField = []byte(`"thought_signature"`)

func (t *sigTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *sigTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := signReply(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *sigTidy) lines(b []byte) []byte {
	if !bytes.Contains(b, thoughtSignatureField) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !bytes.Contains(data, thoughtSignatureField) {
			out = append(out, line...)
			continue
		}
		nb, ok := signReply(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// signReply is a chunk, or a whole reply, with the signed calls' ids
// carrying their signatures, and whether there was one.
func signReply(data []byte) ([]byte, bool) {
	if !bytes.Contains(data, thoughtSignatureField) {
		return nil, false
	}
	var reply map[string]json.RawMessage
	if json.Unmarshal(data, &reply) != nil {
		return nil, false
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(reply["choices"], &choices) != nil {
		return nil, false
	}
	changed := false
	for _, c := range choices {
		for _, k := range []string{"delta", "message"} {
			var msg map[string]json.RawMessage
			if json.Unmarshal(c[k], &msg) != nil {
				continue
			}
			var calls []map[string]json.RawMessage
			if json.Unmarshal(msg["tool_calls"], &calls) != nil {
				continue
			}
			signed := false
			for _, call := range calls {
				sig := googleSignature(call["extra_content"])
				if sig == "" {
					continue
				}
				var id string
				json.Unmarshal(call["id"], &id)
				if _, had := unsignedID(id); had != "" {
					continue
				}
				call["id"], _ = marshalPlain(signedID(id, sig))
				signed = true
			}
			if signed {
				msg["tool_calls"], _ = marshalPlain(calls)
				c[k], _ = marshalPlain(msg)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	reply["choices"], _ = marshalPlain(choices)
	out, err := marshalPlain(reply)
	if err != nil {
		return nil, false
	}
	return out, true
}
