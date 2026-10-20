package gateway

import (
	"bytes"

	"encoding/json"

	"slices"
	"strings"
)

// ---- OpenAI Responses -------------------------------------------------------

// A custom tool takes free text, not JSON: Codex's apply_patch and, for
// the models its catalog puts in code mode (gpt-6.1-sol, gpt-6-luna…),
// exec, through which every other tool of Codex's is called (#534: offered
// none, the model said it had only the collaboration and wait tools). A
// model magpie translates for is offered it as a function taking the text
// as its one argument, and its call goes back to Codex as the
// custom_tool_call Codex runs, the text as its input.

// joinToolRuns moves an item that sits inside a run of tool calls and
// results — after a result of the run and before the last result that
// answers one of its calls — to the end of that run. An upstream that
// checks the exchange, on the chat form it turns a relayed request into or
// on the Responses input itself, reads the run as ended there and refuses
// it: Volcengine's coding plan answers 400 "An assistant message with
// 'tool_calls' must be followed by tool messages responding to each
// 'tool_call_id'", the same refusal pairToolItems mends a missing result
// for (#1341), and OpenCode Zen/Go answers "No tool output found for tool
// call <id>". An agent writes an item between one batch's results itself:
// Codex writes its <image_resize_notice> right after each result that held
// an image it resized (image_preparation.rs, prepare_response_items), so a
// batch of two calls has it between their results (#1752, #1822).
//
// An item that carries a call_id is part of an exchange and never moves,
// whatever its type. Before the run's first result, what the model wrote
// between its own calls is its step: reasoning and a hosted tool's call
// stay, and its text (an assistant message) goes ahead of the run's first
// call, with the reasoning right before it, so the step's calls stand
// together. OpenCode Go turns a relayed request into chat with each
// assistant message after a call opening a new assistant message, so
// [text, call_a, text, call_b, out_a, out_b], what Codex keeps when the
// model wrote a line before each call of a batch, reached it as two
// messages with tool_calls and the first answered by nothing: "No tool
// output found for tool call call_a" (#2100). Anything else there (a
// subagent's agent_message, a note) moves as it would between the results. A call opens the run only when a
// result after it answers it: a call nothing answers is pairToolItems'
// (#1341), and one answered before it is not waited on. An item between two
// whole exchanges stays where it is, and a request with nothing inside a
// run comes back byte for byte.
func joinToolRuns(body []byte) []byte {
	if !bytes.Contains(body, []byte(`_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return body
	}
	var input []json.RawMessage
	if decodeJSON(q["input"], &input) != nil {
		return body
	}
	type item struct {
		Type   string `json:"type"`
		Role   string `json:"role"`
		CallID string `json:"call_id"`
	}
	items := make([]item, len(input))
	for i, raw := range input {
		if decodeJSON(raw, &items[i]) != nil {
			items[i] = item{}
		}
	}
	result := func(it item) bool { return it.CallID != "" && strings.HasSuffix(it.Type, "_output") }
	// answeredLater[i]: the call at i has a result after it
	answeredLater := make([]bool, len(input))
	ahead := map[string]bool{}
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case result(it):
			ahead[it.CallID] = true
		case it.CallID != "":
			answeredLater[i] = ahead[it.CallID]
		}
	}
	modelsOwn := func(it item) bool {
		return it.Type == "reasoning" || it.Type == "message" && it.Role == "assistant" ||
			strings.HasSuffix(it.Type, "_call")
	}
	out := make([]json.RawMessage, 0, len(input))
	kinds := make([]item, 0, len(input)) // what each item of out is
	var held []json.RawMessage
	moved := false
	// open: calls of the run in progress whose result is still ahead;
	// answering: a result of the run has come; first: where in out the
	// run's first call is
	open := map[string]bool{}
	answering := false
	first := 0
	for i, raw := range input {
		it := items[i]
		switch {
		case result(it):
			if open[it.CallID] {
				delete(open, it.CallID)
				answering = true
			}
		case it.CallID != "":
			if answeredLater[i] {
				if len(open) == 0 {
					first = len(out)
				}
				open[it.CallID] = true
			}
		case len(open) > 0 && (answering || !modelsOwn(it)):
			held, moved = append(held, raw), true
			continue
		case len(open) > 0 && it.Type == "message":
			// the model's text between its calls goes ahead of the
			// first, the reasoning it follows with it
			from := len(out)
			for from > first && kinds[from-1].Type == "reasoning" {
				from--
			}
			seg := append(append([]json.RawMessage(nil), out[from:]...), raw)
			segKinds := append(append([]item(nil), kinds[from:]...), it)
			out = slices.Insert(out[:from], first, seg...)
			kinds = slices.Insert(kinds[:from], first, segKinds...)
			first += len(seg)
			moved = true
			continue
		}
		out = append(out, raw)
		kinds = append(kinds, it)
		if len(open) == 0 {
			out = append(out, held...)
			kinds = append(kinds, make([]item, len(held))...)
			held, answering = nil, false
		}
	}
	out = append(out, held...)
	if !moved {
		return body
	}
	q["input"], _ = marshalPlain(out)
	encoded, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
}

// orphanedToolOutputs turns a tool result with no call ID into a user message.
// Codex uses standalone outputs for cross-thread notifications. Backends
// requiring paired outputs still need the delivered text, without a fake call ID.
func orphanedToolOutputs(body []byte) []byte {
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return body
	}
	var input []json.RawMessage
	if decodeJSON(q["input"], &input) != nil {
		return body
	}
	changed := false
	out := make([]json.RawMessage, 0, len(input))
	var pending []json.RawMessage
	inCalls, hadResults := false, false
	flush := func() {
		out = append(out, pending...)
		pending = nil
	}
	for _, raw := range input {
		var item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if decodeJSON(raw, &item) != nil {
			flush()
			inCalls = false
			out = append(out, raw)
			continue
		}
		isOutput := item.Type == "function_call_output" || item.Type == "custom_tool_call_output"
		if !isOutput || item.CallID != "" {
			switch item.Type {
			case "function_call", "custom_tool_call", "tool_search_call":
				if hadResults {
					flush()
				}
				inCalls, hadResults = true, false
			case "function_call_output", "custom_tool_call_output", "tool_search_output":
				// Keep a run of tool results directly after its calls.
				hadResults = true
			default:
				flush()
				inCalls = false
			}
			out = append(out, raw)
			continue
		}
		text, images := toolOutput(item.Output)
		content := []map[string]any{}
		if text != "" {
			content = append(content, map[string]any{"type": "input_text", "text": text})
		}
		for _, image := range images {
			content = append(content, map[string]any{"type": "input_image", "detail": "auto", "image_url": dataURL(image)})
		}
		if len(content) == 0 {
			content = append(content, map[string]any{"type": "input_text", "text": "Tool result received."})
		}
		message, _ := marshalPlain(map[string]any{"type": "message", "role": "user", "content": content})
		if inCalls {
			// Chat rejects a user message between tool_calls and results,
			// including between the results of parallel calls.
			pending = append(pending, message)
		} else {
			out = append(out, message)
		}
		changed = true
	}
	flush()
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(out)
	encoded, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
}

// withReasoningText gives each reasoning item in a Responses request that
// carries no reasoning_text its summary as one, else noReasoning, and has
// each step that needs one lead with a reasoning item (reasonFirst), for an
// upstream that wants the reasoning back (replaysReasoning). Codex sends an
// item back as it got it: one from a turn magpie translated has only a
// summary, one the ChatGPT backend sealed has only encrypted_content, and a
// turn another model in a mixed group served may have no reasoning item at
// all. DeepSeek refuses the whole request over any of them, and over an
// empty reasoning_text, with "The `reasoning_text` in the thinking mode must
// be passed back to the API" (#1104, #1592, #1822). A body with nothing to
// give comes back byte for byte.
func withReasoningText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) && !bytes.Contains(body, []byte(`_call"`)) &&
		!bytes.Contains(body, []byte(`_output"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if decodeJSON(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if decodeJSON(q["input"], &items) != nil {
		return body
	}
	changed := false
	kinds := make([]stepKind, len(items))
	for i, raw := range items {
		var it struct {
			Type    string  `json:"type"`
			Role    string  `json:"role"`
			Summary []rText `json:"summary"`
			Content []rText `json:"content"`
		}
		if decodeJSON(raw, &it) != nil {
			continue
		}
		kinds[i] = stepKindOf(it.Type, it.Role)
		if it.Type != "reasoning" ||
			slices.ContainsFunc(it.Content, func(c rText) bool { return c.Type == "reasoning_text" && c.Text != "" }) {
			continue
		}
		var b strings.Builder
		for _, s := range it.Summary {
			b.WriteString(s.Text)
		}
		if b.Len() == 0 {
			b.WriteString(noReasoning)
		}
		var m map[string]json.RawMessage
		if decodeJSON(raw, &m) != nil {
			continue
		}
		m["content"], _ = json.Marshal([]map[string]string{{"type": "reasoning_text", "text": b.String()}})
		if nb, err := json.Marshal(m); err == nil {
			items[i], changed = nb, true
		}
	}
	none, _ := json.Marshal(noReasoningItem())
	if led, ok := reasonFirst(items, kinds, json.RawMessage(none)); ok {
		items, changed = led, true
	}
	if !changed {
		return body
	}
	q["input"], _ = json.Marshal(items)
	nb, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return nb
}
