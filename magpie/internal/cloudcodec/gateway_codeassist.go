package cloudcodec

// skipSignature stands in for a thought signature on a function call the
// model didn't make here: Google checks the ones Gemini 3 hands out, and
// this one tells it not to.
const SkipSignature = "skip_thought_signature_validator"

// dropSecondAnswers leaves out a tool result whose call an earlier result
// already answered, with the images it carried: the first answer stands, as
// pairToolMessages and pairToolBlocks keep it for a Chat or Messages
// upstream. Google turns a call turn away when its functionResponse count
// differs from its functionCall count, so a client that answers one call
// twice (#2262) had every later turn refused. A call made again under the
// same id takes an answer of its own, and a result with no id is left as
// it is. msgs is not changed: a fallback builds from the same request.
func DropSecondAnswers(msgs []Message) []Message {
	var out []Message
	answered := map[string]bool{}
	for i, m := range msgs {
		var parts []Part
		for j, p := range m.Parts {
			switch {
			case p.Kind == ToolCall && p.ID != "":
				delete(answered, p.ID)
			case p.Kind == ToolResult && p.CallID != "" && p.Standalone == nil:
				if answered[p.CallID] {
					if parts == nil {
						parts = append(make([]Part, 0, len(m.Parts)), m.Parts[:j]...)
					}
					if out == nil {
						out = append(make([]Message, 0, len(msgs)), msgs[:i]...)
					}
					continue
				}
				answered[p.CallID] = true
			}
			if parts != nil {
				parts = append(parts, p)
			}
		}
		if out == nil {
			continue
		}
		if parts != nil {
			if len(parts) == 0 {
				continue
			}
			m.Parts = parts
		}
		out = append(out, m)
	}
	if out == nil {
		return msgs
	}
	return out
}
