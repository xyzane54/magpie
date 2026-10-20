package cloudcodec

import (
	"encoding/json"
	"maps"
)

// wholeEvents are the events of proto's stream that say the whole reply
// b, or nil for a body that isn't one.
func WholeEvents(proto Protocol, b []byte) []WholeEvent {
	switch proto {
	case Chat:
		return ChatWholeEvents(b)
	case Responses:
		return ResponsesWholeEvents(b)
	case Anthropic:
		return AnthropicWholeEvents(b)
	}
	return nil
}

type WholeEvent struct {
	Name string
	Data any
}

// obj is a JSON object read with its values as the vendor wrote them.
type Obj = map[string]json.RawMessage

func Raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func Str(m Obj, k string) string {
	var s string
	json.Unmarshal(m[k], &s)
	return s
}

// chatWholeEvents streams a chat.completion as its chunks: each choice's
// message as one delta, its tool calls numbered as a stream numbers them,
// then its finish reason with the usage, then [DONE].
func ChatWholeEvents(b []byte) []WholeEvent {
	var v Obj
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var choices []struct {
		Index        json.RawMessage `json:"index"`
		Message      Obj             `json:"message"`
		FinishReason json.RawMessage `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	}
	if json.Unmarshal(v["choices"], &choices) != nil || len(choices) == 0 {
		return nil
	}
	chunk := func(cs []Obj, usage json.RawMessage) Obj {
		c := Obj{"object": Raw("chat.completion.chunk"), "choices": Raw(cs)}
		for _, k := range []string{"id", "created", "model", "system_fingerprint", "service_tier"} {
			if x, ok := v[k]; ok {
				c[k] = x
			}
		}
		if usage != nil {
			c["usage"] = usage
		}
		return c
	}
	var deltas, ends []Obj
	for i, c := range choices {
		if c.Message == nil {
			return nil
		}
		index := c.Index
		if index == nil {
			index = Raw(i)
		}
		delta := maps.Clone(c.Message)
		var calls []Obj
		if json.Unmarshal(delta["tool_calls"], &calls) == nil && len(calls) > 0 {
			for n, call := range calls {
				if _, ok := call["index"]; !ok {
					call["index"] = Raw(n)
				}
			}
			delta["tool_calls"] = Raw(calls)
		}
		d := Obj{"index": index, "delta": Raw(delta), "finish_reason": Raw(nil)}
		if c.Logprobs != nil {
			d["logprobs"] = c.Logprobs
		}
		deltas = append(deltas, d)
		finish := c.FinishReason
		if finish == nil {
			finish = Raw(nil)
		}
		ends = append(ends, Obj{"index": index, "delta": Raw(Obj{}), "finish_reason": finish})
	}
	return []WholeEvent{{"", chunk(deltas, nil)}, {"", chunk(ends, v["usage"])}, {"", "[DONE]"}}
}

// responsesWholeEvents streams a Responses response object: created and
// in progress with no output, each output item added, its text (with its
// citations), refusal, reasoning summary or arguments as deltas and their
// ends, the item done
// as the vendor wrote it, then the response completed (or incomplete, or
// failed) whole.
func ResponsesWholeEvents(b []byte) []WholeEvent {
	var events []WholeEvent
	if ResponsesWholeEventsEach(b, func(event WholeEvent) error { events = append(events, event); return nil }) != nil {
		return nil
	}
	return events
}

// ResponsesWholeEventsEach emits one event at a time, so native cloud output
// can preflight its expanded wire size without retaining a second full stream.
func ResponsesWholeEventsEach(b []byte, yield func(WholeEvent) error) error {
	var v Obj
	if json.Unmarshal(b, &v) != nil || Str(v, "object") != "response" {
		return ErrCloudReply
	}
	var output []Obj
	if json.Unmarshal(v["output"], &output) != nil {
		return ErrCloudReply
	}
	var emitErr error
	seq := 0
	send := func(typ string, fields Obj) {
		if emitErr != nil {
			return
		}
		fields["type"], fields["sequence_number"] = Raw(typ), Raw(seq)
		seq++
		emitErr = yield(WholeEvent{typ, fields})
	}
	begun := maps.Clone(v)
	begun["status"], begun["output"] = Raw("in_progress"), Raw([]any{})
	for _, k := range []string{"usage", "incomplete_details", "error", "completed_at"} {
		delete(begun, k)
	}
	send("response.created", Obj{"response": Raw(begun)})
	send("response.in_progress", Obj{"response": Raw(begun)})
	for i, item := range output {
		if emitErr != nil {
			return emitErr
		}
		at, id := Raw(i), item["id"]
		added := maps.Clone(item)
		if _, ok := added["status"]; ok {
			added["status"] = Raw("in_progress")
		}
		var parts []Obj
		switch Str(item, "type") {
		case "message":
			json.Unmarshal(item["content"], &parts)
			added["content"] = Raw([]any{})
			send("response.output_item.added", Obj{"output_index": at, "item": Raw(added)})
			for j, part := range parts {
				if emitErr != nil {
					return emitErr
				}
				ids := func(o Obj) Obj {
					o["item_id"], o["output_index"], o["content_index"] = id, at, Raw(j)
					return o
				}
				switch Str(part, "type") {
				case "output_text":
					text := Str(part, "text")
					send("response.content_part.added", ids(Obj{"part": Raw(Obj{"type": Raw("output_text"), "text": Raw(""), "annotations": Raw([]any{})})}))
					send("response.output_text.delta", ids(Obj{"delta": Raw(text), "logprobs": Raw([]any{})}))
					// its citations, which a client takes from these events
					// alone (ai-sdk's sources)
					var notes []json.RawMessage
					json.Unmarshal(part["annotations"], &notes)
					for k, a := range notes {
						send("response.output_text.annotation.added", ids(Obj{"annotation_index": Raw(k), "annotation": a}))
					}
					send("response.output_text.done", ids(Obj{"text": Raw(text), "logprobs": Raw([]any{})}))
				case "refusal":
					refusal := Str(part, "refusal")
					send("response.content_part.added", ids(Obj{"part": Raw(Obj{"type": Raw("refusal"), "refusal": Raw("")})}))
					send("response.refusal.delta", ids(Obj{"delta": Raw(refusal)}))
					send("response.refusal.done", ids(Obj{"refusal": Raw(refusal)}))
				default:
					send("response.content_part.added", ids(Obj{"part": Raw(part)}))
				}
				send("response.content_part.done", ids(Obj{"part": Raw(part)}))
			}
		case "reasoning":
			json.Unmarshal(item["summary"], &parts)
			added["summary"] = Raw([]any{})
			send("response.output_item.added", Obj{"output_index": at, "item": Raw(added)})
			for j, part := range parts {
				if emitErr != nil {
					return emitErr
				}
				ids := func(o Obj) Obj {
					o["item_id"], o["output_index"], o["summary_index"] = id, at, Raw(j)
					return o
				}
				text := Str(part, "text")
				send("response.reasoning_summary_part.added", ids(Obj{"part": Raw(Obj{"type": Raw("summary_text"), "text": Raw("")})}))
				send("response.reasoning_summary_text.delta", ids(Obj{"delta": Raw(text)}))
				send("response.reasoning_summary_text.done", ids(Obj{"text": Raw(text)}))
				send("response.reasoning_summary_part.done", ids(Obj{"part": Raw(part)}))
			}
		case "function_call":
			if Str(item, "id") == "" {
				// with no id, no event can name it as its item: whole
				send("response.output_item.added", Obj{"output_index": at, "item": Raw(item)})
				break
			}
			args := Str(item, "arguments")
			added["arguments"] = Raw("")
			send("response.output_item.added", Obj{"output_index": at, "item": Raw(added)})
			send("response.function_call_arguments.delta", Obj{"item_id": id, "output_index": at, "delta": Raw(args)})
			send("response.function_call_arguments.done", Obj{"item_id": id, "output_index": at, "arguments": Raw(args)})
		default:
			// a custom tool's call, a web search, an image…: whole as it is
			send("response.output_item.added", Obj{"output_index": at, "item": Raw(item)})
		}
		send("response.output_item.done", Obj{"output_index": at, "item": Raw(item)})
	}
	end := "response.completed"
	switch Str(v, "status") {
	case "incomplete":
		end = "response.incomplete"
	case "failed":
		end = "response.failed"
	}
	send(end, Obj{"response": Raw(v)})
	return emitErr
}

// anthropicWholeEvents streams an Anthropic message: started with no
// content, each block started empty with what it holds as its deltas — a
// text's citations and text, a thinking's thought and signature, a tool
// call's input — or whole for a block that streams none, then the stop
// reason with the usage.
func AnthropicWholeEvents(b []byte) []WholeEvent {
	var v Obj
	if json.Unmarshal(b, &v) != nil || Str(v, "type") != "message" {
		return nil
	}
	var content []Obj
	if json.Unmarshal(v["content"], &content) != nil {
		return nil
	}
	var events []WholeEvent
	send := func(typ string, fields Obj) {
		fields["type"] = Raw(typ)
		events = append(events, WholeEvent{typ, fields})
	}
	start := maps.Clone(v)
	start["content"], start["stop_reason"], start["stop_sequence"] = Raw([]any{}), Raw(nil), Raw(nil)
	// auto mode's review of the reply's calls is the last thing a stream
	// says, in its message_delta, where Claude Code reads it (automode.go)
	delete(start, "safeguard_results")
	var usage Obj
	if json.Unmarshal(v["usage"], &usage) == nil && usage != nil {
		// the output is counted as it is said, in message_delta
		u := maps.Clone(usage)
		u["output_tokens"] = Raw(0)
		start["usage"] = Raw(u)
	}
	send("message_start", Obj{"message": Raw(start)})
	for i, block := range content {
		at := Raw(i)
		delta := func(d Obj) { send("content_block_delta", Obj{"index": at, "delta": Raw(d)}) }
		empty := maps.Clone(block)
		switch Str(block, "type") {
		case "text":
			var cites []json.RawMessage
			json.Unmarshal(block["citations"], &cites)
			empty["text"] = Raw("")
			if _, ok := block["citations"]; ok {
				empty["citations"] = Raw([]any{})
			}
			send("content_block_start", Obj{"index": at, "content_block": Raw(empty)})
			for _, c := range cites {
				delta(Obj{"type": Raw("citations_delta"), "citation": c})
			}
			delta(Obj{"type": Raw("text_delta"), "text": Raw(Str(block, "text"))})
		case "thinking":
			empty["thinking"], empty["signature"] = Raw(""), Raw("")
			send("content_block_start", Obj{"index": at, "content_block": Raw(empty)})
			delta(Obj{"type": Raw("thinking_delta"), "thinking": Raw(Str(block, "thinking"))})
			if sig := Str(block, "signature"); sig != "" {
				delta(Obj{"type": Raw("signature_delta"), "signature": Raw(sig)})
			}
		case "tool_use", "server_tool_use", "mcp_tool_use":
			input := block["input"]
			if input == nil {
				input = Raw(Obj{})
			}
			empty["input"] = Raw(Obj{})
			send("content_block_start", Obj{"index": at, "content_block": Raw(empty)})
			delta(Obj{"type": Raw("input_json_delta"), "partial_json": Raw(string(input))})
		default:
			// redacted thinking, a search's results…: whole as it is
			send("content_block_start", Obj{"index": at, "content_block": Raw(block)})
		}
		send("content_block_stop", Obj{"index": at})
	}
	// the stop's details too (a refusal's category), which a client takes
	// from this event, null or not, over message_start's
	stop := Obj{"stop_reason": OrNull(v["stop_reason"]), "stop_sequence": OrNull(v["stop_sequence"]), "stop_details": OrNull(v["stop_details"])}
	if c, ok := v["container"]; ok {
		stop["container"] = c
	}
	if r, ok := v["safeguard_results"]; ok {
		stop["safeguard_results"] = r
	}
	end := Obj{"delta": Raw(stop)}
	if usage != nil {
		end["usage"] = v["usage"]
	}
	send("message_delta", end)
	send("message_stop", Obj{})
	return events
}

func OrNull(r json.RawMessage) json.RawMessage {
	if r == nil {
		return Raw(nil)
	}
	return r
}
