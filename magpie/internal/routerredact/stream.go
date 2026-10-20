package routerredact

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

const (
	streamEventLimit   = 1 << 20
	streamTailLimit    = 4 << 20
	streamChannelLimit = 256
)

type streamChannel struct {
	text     streamText
	tool     *streamJSON
	template map[string]any
	field    map[string]any
	name     string
	closed   bool
}

type streamRestore struct {
	protocol Protocol
	mapping  map[string]string
	channels map[string]*streamChannel
	order    []string
	sequence int
	bindings map[string]map[string]string
}

func (r *streamRestore) bind(key string, values map[string]string) error {
	if r.bindings == nil {
		r.bindings = make(map[string]map[string]string)
	}
	old := r.bindings[key]
	if old == nil {
		if len(r.bindings) >= streamChannelLimit*8 {
			return fail("stream_limit")
		}
		old = make(map[string]string)
		r.bindings[key] = old
	}
	for name, value := range values {
		if value == "" {
			continue
		}
		if previous := old[name]; previous != "" && previous != value {
			return fail("stream_invalid_channel")
		}
		old[name] = value
	}
	return nil
}

func streamMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func streamList(v any) []any         { a, _ := v.([]any); return a }
func streamString(v any) string      { s, _ := v.(string); return s }
func streamIndex(v any) string {
	if n, ok := v.(json.Number); ok {
		if i, err := strconv.ParseInt(string(n), 10, 32); err == nil && i >= 0 {
			return string(n)
		}
	}
	return ""
}

func streamDecode(data []byte) (map[string]any, error) {
	v, err := wireDecode(data, streamEventLimit)
	value, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, fail("stream_invalid_event")
	}
	return value, nil
}

func (r *streamRestore) feed(key string, tool bool, value string, template, field map[string]any, name string) (string, error) {
	c := r.channels[key]
	if c == nil {
		if len(r.channels) >= streamChannelLimit {
			return "", fail("stream_limit")
		}
		c = &streamChannel{text: streamText{mapping: r.mapping}}
		if tool {
			c.tool = &streamJSON{text: streamText{mapping: r.mapping}}
		}
		r.channels[key] = c
		r.order = append(r.order, key)
	}
	if c.closed || (c.tool != nil) != tool {
		return "", fail("stream_invalid_channel")
	}
	c.template, c.field, c.name = template, field, name
	if tool {
		return c.tool.feed(value, false)
	}
	text := c.text.feed(value, false)
	if c.text.failed {
		return "", fail("stream_limit")
	}
	return text, nil
}

func (r *streamRestore) finish(prefix string, exact ...bool) ([]map[string]any, error) {
	var output []map[string]any
	for _, key := range r.order {
		c := r.channels[key]
		if c.closed || !strings.HasPrefix(key, prefix) || len(exact) > 0 && exact[0] && key != prefix {
			continue
		}
		var text string
		var err error
		if c.tool != nil {
			text, err = c.tool.feed("", true)
		} else {
			text = c.text.feed("", true)
			if c.text.failed {
				err = fail("stream_limit")
			}
		}
		if err != nil {
			return nil, err
		}
		c.closed = true
		if text != "" {
			c.field[c.name] = text
			output = append(output, c.template)
		}
		c.template, c.field = nil, nil
	}
	return output, nil
}

func (r *streamRestore) restoreFull(text string, tool bool) (string, error) {
	if !tool {
		t := &streamText{mapping: r.mapping}
		value := t.feed(text, true)
		if t.failed {
			return "", fail("stream_limit")
		}
		return value, nil
	}
	return (&streamJSON{text: streamText{mapping: r.mapping}}).feed(text, true)
}

func (r *streamRestore) restoreField(m map[string]any, name string) error {
	if text, ok := m[name].(string); ok {
		value, err := r.restoreFull(text, false)
		if err != nil {
			return err
		}
		m[name] = value
	}
	return nil
}

func (r *streamRestore) responseItem(item map[string]any) error {
	if item == nil {
		return nil
	}
	if id := streamString(item["id"]); id != "" {
		if err := r.bind("response-id:"+id, map[string]string{"call_id": streamString(item["call_id"]), "name": streamString(item["name"]), "type": streamString(item["type"])}); err != nil {
			return err
		}
	}
	if typ := streamString(item["type"]); typ == "function_call" {
		if text, ok := item["arguments"].(string); ok && text != "" {
			v, err := r.restoreFull(text, true)
			if err != nil {
				return err
			}
			item["arguments"] = v
		}
	} else if typ == "message" || item["type"] == nil {
		for _, raw := range streamList(item["content"]) {
			part := streamMap(raw)
			for _, key := range []string{"text", "refusal"} {
				if err := r.restoreField(part, key); err != nil {
					return err
				}
			}
		}
	} else if typ == "custom_tool_call" {
		if err := r.restoreField(item, "input"); err != nil {
			return err
		}
	} else if typ == "reasoning" {
		if encrypted := item["encrypted_content"]; encrypted != nil && encrypted != "" {
			return fail("stream_opaque_content")
		}
		for _, name := range []string{"summary", "content"} {
			for _, raw := range streamList(item[name]) {
				if err := r.restoreField(streamMap(raw), "text"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// transform returns flush events, whether this is a success terminal, and
// whether the bounded completion tail must start before writing this event.
func (r *streamRestore) transform(m map[string]any) ([]map[string]any, bool, bool, error) {
	var before []map[string]any
	appendFinish := func(prefix string) error {
		events, err := r.finish(prefix)
		before = append(before, events...)
		return err
	}
	switch r.protocol {
	case Chat:
		if err := r.bind("chat", map[string]string{"id": streamString(m["id"])}); err != nil {
			return nil, false, false, err
		}
		finishing := false
		for _, raw := range streamList(m["choices"]) {
			choice := streamMap(raw)
			index := streamIndex(choice["index"])
			if index == "" {
				return nil, false, false, fail("stream_invalid_channel")
			}
			prefix := "c:" + index + ":"
			delta := streamMap(choice["delta"])
			if delta["function_call"] != nil {
				return nil, false, false, fail("stream_unsupported")
			}
			if v := delta["content"]; v != nil {
				if _, ok := v.(string); !ok {
					return nil, false, false, fail("stream_unsupported")
				}
			}
			for _, name := range []string{"content", "refusal", "reasoning_content"} {
				if text, ok := delta[name].(string); ok {
					field := map[string]any{name: ""}
					template := map[string]any{"choices": []any{map[string]any{"index": choice["index"], "delta": field, "finish_reason": nil}}}
					for _, meta := range []string{"id", "object", "created", "model", "system_fingerprint"} {
						if v, ok := m[meta]; ok {
							template[meta] = v
						}
					}
					value, err := r.feed(prefix+name, false, text, template, field, name)
					if err != nil {
						return nil, false, false, err
					}
					delta[name] = value
				}
			}
			for _, rawTool := range streamList(delta["tool_calls"]) {
				tool := streamMap(rawTool)
				toolIndex := streamIndex(tool["index"])
				if toolIndex == "" {
					return nil, false, false, fail("stream_invalid_channel")
				}
				function := streamMap(tool["function"])
				if err := r.bind(prefix+"tool:"+toolIndex, map[string]string{"id": streamString(tool["id"]), "type": streamString(tool["type"])}); err != nil {
					return nil, false, false, err
				}
				if id := streamString(tool["id"]); id != "" {
					if err := r.bind("chat-id:"+id, map[string]string{"index": prefix + toolIndex}); err != nil {
						return nil, false, false, err
					}
				}
				if args, ok := function["arguments"].(string); ok {
					field := map[string]any{"arguments": ""}
					template := map[string]any{"choices": []any{map[string]any{"index": choice["index"], "delta": map[string]any{"tool_calls": []any{map[string]any{"index": tool["index"], "function": field}}}, "finish_reason": nil}}}
					for _, meta := range []string{"id", "object", "created", "model"} {
						if v, ok := m[meta]; ok {
							template[meta] = v
						}
					}
					value, err := r.feed(prefix+"tool:"+toolIndex, true, args, template, field, "arguments")
					if err != nil {
						return nil, false, false, err
					}
					function["arguments"] = value
				}
			}
			if choice["finish_reason"] != nil {
				finishing = true
				if err := appendFinish(prefix); err != nil {
					return nil, false, false, err
				}
			}
		}
		return before, false, finishing, nil
	case Responses:
		typ := streamString(m["type"])
		itemID := streamString(m["item_id"])
		outputIndex := streamIndex(m["output_index"])
		if item := streamMap(m["item"]); item != nil {
			itemID = streamString(item["id"])
			if itemID != "" {
				if err := r.bind("response-id:"+itemID, map[string]string{"call_id": streamString(item["call_id"]), "name": streamString(item["name"]), "type": streamString(item["type"])}); err != nil {
					return nil, false, false, err
				}
			}
		}
		if itemID != "" && outputIndex != "" {
			if err := r.bind("response-id:"+itemID, map[string]string{"index": outputIndex}); err != nil {
				return nil, false, false, err
			}
			if err := r.bind("response-index:"+outputIndex, map[string]string{"id": itemID}); err != nil {
				return nil, false, false, err
			}
		}
		if typ == "error" || typ == "response.failed" {
			return nil, false, false, fail("stream_upstream_error")
		}
		if typ == "response.completed" || typ == "response.incomplete" {
			if err := appendFinish(""); err != nil {
				return nil, false, false, err
			}
			response := streamMap(m["response"])
			if err := r.restoreField(response, "output_text"); err != nil {
				return nil, false, false, err
			}
			for index, raw := range streamList(response["output"]) {
				if id := streamString(streamMap(raw)["id"]); id != "" {
					if err := r.bind("response-id:"+id, map[string]string{"index": strconv.Itoa(index)}); err != nil {
						return nil, false, false, err
					}
					if err := r.bind("response-index:"+strconv.Itoa(index), map[string]string{"id": id}); err != nil {
						return nil, false, false, err
					}
				}
				if err := r.responseItem(streamMap(raw)); err != nil {
					return nil, false, false, err
				}
			}
			return before, true, true, nil
		}
		if typ == "response.output_item.added" || typ == "response.output_item.done" {
			if err := r.responseItem(streamMap(m["item"])); err != nil {
				return nil, false, false, err
			}
		}
		isTool := strings.HasPrefix(typ, "response.function_call_arguments.")
		isText := strings.HasPrefix(typ, "response.output_text.") || strings.HasPrefix(typ, "response.refusal.") || strings.HasPrefix(typ, "response.custom_tool_call_input.") || strings.HasPrefix(typ, "response.reasoning_text.") || strings.HasPrefix(typ, "response.reasoning_summary_text.")
		if strings.HasSuffix(typ, ".delta") && !isText && !isTool {
			return nil, false, false, fail("stream_unsupported")
		}
		if typ == "response.content_part.added" || typ == "response.content_part.done" || typ == "response.reasoning_summary_part.added" || typ == "response.reasoning_summary_part.done" {
			part := streamMap(m["part"])
			for _, name := range []string{"text", "refusal"} {
				if err := r.restoreField(part, name); err != nil {
					return nil, false, false, err
				}
			}
		}
		if isTool || isText {
			id := streamString(m["item_id"])
			if id == "" {
				id = streamIndex(m["output_index"])
			}
			if id == "" {
				return nil, false, false, fail("stream_invalid_channel")
			}
			kind := strings.TrimSuffix(strings.TrimSuffix(typ, ".delta"), ".done")
			key := "r:" + id + ":" + kind + ":" + streamIndex(m["content_index"])
			if strings.HasSuffix(typ, ".delta") {
				text, ok := m["delta"].(string)
				if !ok {
					return nil, false, false, fail("stream_invalid_event")
				}
				template := map[string]any{"type": typ, "delta": ""}
				for _, name := range []string{"item_id", "output_index", "content_index", "sequence_number"} {
					if v, ok := m[name]; ok {
						template[name] = v
					}
				}
				value, err := r.feed(key, isTool, text, template, template, "delta")
				if err != nil {
					return nil, false, false, err
				}
				m["delta"] = value
			} else if strings.HasSuffix(typ, ".done") {
				events, err := r.finish(key, true)
				before = append(before, events...)
				if err != nil {
					return nil, false, false, err
				}
				for _, name := range []string{"text", "refusal", "arguments", "input"} {
					if text, ok := m[name].(string); ok {
						value, err := r.restoreFull(text, isTool)
						if err != nil {
							return nil, false, false, err
						}
						m[name] = value
					}
				}
			}
		}
		return before, false, false, nil
	case Anthropic:
		typ := streamString(m["type"])
		if typ == "error" {
			return nil, false, false, fail("stream_upstream_error")
		}
		if typ == "message_stop" {
			if err := appendFinish(""); err != nil {
				return nil, false, false, err
			}
			return before, true, true, nil
		}
		if typ == "content_block_delta" {
			index := streamIndex(m["index"])
			if index == "" {
				return nil, false, false, fail("stream_invalid_channel")
			}
			delta := streamMap(m["delta"])
			kind := streamString(delta["type"])
			name, tool := "text", false
			if kind == "input_json_delta" {
				name, tool = "partial_json", true
			} else if kind != "text_delta" {
				return nil, false, false, fail("stream_opaque_content")
			}
			blockType := "text"
			if tool {
				blockType = "tool_use"
			}
			if err := r.bind("anthropic:"+index, map[string]string{"type": blockType}); err != nil {
				return nil, false, false, err
			}
			text, ok := delta[name].(string)
			if !ok {
				return nil, false, false, fail("stream_invalid_event")
			}
			field := map[string]any{"type": kind, name: ""}
			template := map[string]any{"type": typ, "index": m["index"], "delta": field}
			value, err := r.feed("a:"+index+":"+kind, tool, text, template, field, name)
			if err != nil {
				return nil, false, false, err
			}
			delta[name] = value
		} else if typ == "content_block_stop" {
			index := streamIndex(m["index"])
			if index == "" {
				return nil, false, false, fail("stream_invalid_channel")
			}
			if err := appendFinish("a:" + index + ":"); err != nil {
				return nil, false, false, err
			}
		} else if typ == "content_block_start" {
			block := streamMap(m["content_block"])
			index := streamIndex(m["index"])
			if index == "" {
				return nil, false, false, fail("stream_invalid_channel")
			}
			if err := r.bind("anthropic:"+index, map[string]string{"type": streamString(block["type"]), "id": streamString(block["id"]), "name": streamString(block["name"])}); err != nil {
				return nil, false, false, err
			}
			if id := streamString(block["id"]); id != "" {
				if err := r.bind("anthropic-id:"+id, map[string]string{"index": index}); err != nil {
					return nil, false, false, err
				}
			}
			if text, ok := block["text"].(string); ok {
				field := map[string]any{"type": "text_delta", "text": ""}
				template := map[string]any{"type": "content_block_delta", "index": m["index"], "delta": field}
				value, err := r.feed("a:"+index+":text_delta", false, text, template, field, "text")
				if err != nil {
					return nil, false, false, err
				}
				block["text"] = value
			}
			if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
				return nil, false, false, fail("stream_opaque_content")
			}
			if input, ok := block["input"]; ok {
				raw, err := wireMarshal(input)
				if err != nil {
					return nil, false, false, fail("stream_invalid_json")
				}
				fixed, err := r.restoreFull(string(raw), true)
				if err != nil {
					return nil, false, false, err
				}
				d := json.NewDecoder(strings.NewReader(fixed))
				d.UseNumber()
				var value any
				if d.Decode(&value) != nil {
					return nil, false, false, fail("stream_invalid_json")
				}
				block["input"] = value
				if inputMap := streamMap(input); inputMap != nil && len(inputMap) > 0 {
					field := map[string]any{"type": "input_json_delta", "partial_json": ""}
					template := map[string]any{"type": "content_block_delta", "index": m["index"], "delta": field}
					if _, err := r.feed("a:"+index+":input_json_delta", true, string(raw), template, field, "partial_json"); err != nil {
						return nil, false, false, err
					}
					if _, err := r.finish("a:"+index+":input_json_delta", true); err != nil {
						return nil, false, false, err
					}
				}
			}
		} else if typ == "message_start" {
			if content := streamList(streamMap(m["message"])["content"]); len(content) > 0 {
				return nil, false, false, fail("stream_unsupported")
			}
		}
		return before, false, typ == "message_delta" && streamMap(m["delta"])["stop_reason"] != nil, nil
	}
	return nil, false, false, fail("unsupported_protocol")
}

// RestoreSSE incrementally restores protocol text and JSON argument values.
// The caller owns Snapshot.Close and the output. A cancellable input must be an
// io.ReadCloser (as HTTP response bodies are); cancellation closes that input.
// Success terminators are withheld until a clean EOF and valid channel closure.
func (s *Snapshot) RestoreSSE(ctx context.Context, protocol Protocol, input io.Reader, output io.Writer) error {
	if s == nil {
		return fail("redaction_released")
	}
	if protocol != Chat && protocol != Responses && protocol != Anthropic {
		return fail("unsupported_protocol")
	}
	mapping, err := s.mappingCopy()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fail("stream_canceled")
	}
	stop := make(chan struct{})
	if closer, ok := input.(io.ReadCloser); ok {
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-stop:
			}
		}()
		defer func() { close(stop); <-joined }()
	}
	r := &streamRestore{protocol: protocol, mapping: mapping, channels: make(map[string]*streamChannel)}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), streamEventLimit)
	var lines []string
	var eventBytes int
	var tail bytes.Buffer
	tailing, terminal := false, false
	write := func(data []byte) error {
		if ctx.Err() != nil {
			return fail("stream_canceled")
		}
		if tailing {
			if tail.Len()+len(data) > streamTailLimit {
				return fail("stream_limit")
			}
			_, _ = tail.Write(data)
			return nil
		}
		if n, err := output.Write(data); err != nil || n != len(data) {
			return fail("stream_write_failed")
		}
		if f, ok := output.(interface{ Flush() }); ok {
			f.Flush()
		}
		return nil
	}
	encode := func(m map[string]any, extra []string) ([]byte, error) {
		if protocol == Responses {
			if _, ok := m["sequence_number"]; ok {
				m["sequence_number"] = r.sequence
				r.sequence++
			}
		}
		data, err := wireMarshal(m)
		if err != nil || len(data) > streamEventLimit*8 {
			return nil, fail("stream_limit")
		}
		var b bytes.Buffer
		hasEvent := false
		for _, line := range extra {
			if !strings.HasPrefix(line, "data:") {
				if strings.HasPrefix(line, "event:") {
					hasEvent = true
				}
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		if !hasEvent && protocol != Chat {
			if typ := streamString(m["type"]); typ != "" {
				b.WriteString("event: ")
				b.WriteString(typ)
				b.WriteByte('\n')
			}
		}
		b.WriteString("data: ")
		b.Write(data)
		b.WriteString("\n\n")
		return b.Bytes(), nil
	}
	process := func() error {
		var data []string
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				value := strings.TrimPrefix(line, "data:")
				value = strings.TrimPrefix(value, " ")
				data = append(data, value)
			}
		}
		if len(data) == 0 {
			if terminal {
				return nil
			}
			return write([]byte(strings.Join(lines, "\n") + "\n\n"))
		}
		if terminal {
			return fail("stream_after_terminal")
		}
		joined := strings.Join(data, "\n")
		if protocol == Chat && joined == "[DONE]" {
			before, err := r.finish("")
			if err != nil {
				return err
			}
			for _, m := range before {
				b, err := encode(m, nil)
				if err != nil {
					return err
				}
				if err = write(b); err != nil {
					return err
				}
			}
			tailing, terminal = true, true
			return write([]byte("data: [DONE]\n\n"))
		}
		m, err := streamDecode([]byte(joined))
		if err != nil {
			return err
		}
		if m["error"] != nil {
			return fail("stream_upstream_error")
		}
		before, done, startTail, err := r.transform(m)
		if err != nil {
			return err
		}
		for _, event := range before {
			b, err := encode(event, nil)
			if err != nil {
				return err
			}
			if err = write(b); err != nil {
				return err
			}
		}
		if startTail {
			tailing = true
		}
		b, err := encode(m, lines)
		if err != nil {
			return err
		}
		if err = write(b); err != nil {
			return err
		}
		terminal = done
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return fail("stream_canceled")
		}
		line := scanner.Text()
		if line == "" {
			if len(lines) > 0 {
				if err := process(); err != nil {
					return err
				}
			}
			lines = nil
			eventBytes = 0
			continue
		}
		eventBytes += len(line) + 1
		if eventBytes > streamEventLimit {
			return fail("stream_limit")
		}
		lines = append(lines, line)
	}
	if ctx.Err() != nil {
		return fail("stream_canceled")
	}
	if scanner.Err() != nil {
		return fail("stream_read_failed")
	}
	if len(lines) != 0 || !terminal {
		return fail("stream_truncated")
	}
	if n, err := output.Write(tail.Bytes()); err != nil || n != tail.Len() {
		return fail("stream_write_failed")
	}
	if f, ok := output.(interface{ Flush() }); ok {
		f.Flush()
	}
	return nil
}
