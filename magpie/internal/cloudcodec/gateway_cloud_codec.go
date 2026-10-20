package cloudcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// CloudCodec reuses only the wire codecs. It never constructs a Server or
// resolves providers, runs tools, retries, or starts the gateway lifecycle.
// The caller owns transport cancellation and must close a blocked reply body.
type CloudCodec struct {
	protocol Protocol
	body     []byte
	request  *Request
}

// CloudReply describes bytes actually committed and usage actually reported.
// UsageKnown is false when the upstream omitted token counts; zero is not an
// estimate. OutputCommitted stays true on a partial reply or a failed write.
type CloudReply struct {
	Usage           Usage
	UsageKnown      bool
	ReportedUsage   *ReportedUsage
	OutputCommitted bool
}

// ReportedUsage preserves the upstream's counts before IR normalization. A nil
// field means the upstream did not report that count; totals are never derived.
// Input is the protocol's input_tokens or Chat's prompt_tokens, including any
// cache tokens that protocol includes. It must not be added to cache subtotals.
type ReportedUsage struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
}

var ErrCloudReply = errors.New("cloud response invalid or interrupted")

const CloudReplyLimit = 32 << 20

func CloudProtocol(protocol string) (Protocol, error) {
	switch p := Protocol(protocol); p {
	case Chat, Responses, Anthropic:
		return p, nil
	}
	return "", errors.New("unsupported cloud protocol")
}

// NewCloudCodec retains the complete original request for native forwarding.
func NewCloudCodec(protocol string, body []byte) (*CloudCodec, error) {
	p, err := CloudProtocol(protocol)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if DecodeRequest(body, &object) != nil {
		return nil, errors.New("invalid cloud request")
	}
	r, err := cloudParse(p, body)
	if err != nil {
		return nil, errors.New("invalid cloud request")
	}
	return &CloudCodec{protocol: p, body: bytes.Clone(body), request: r}, nil
}

// Request changes only model on a native request. Translated requests use the
// existing IR builders and retain the client's streaming preference.
func (c *CloudCodec) Request(protocol, model, host string) ([]byte, error) {
	p, err := CloudProtocol(protocol)
	if err != nil {
		return nil, err
	}
	if p == c.protocol {
		var object map[string]json.RawMessage
		if json.Unmarshal(c.body, &object) != nil {
			return nil, errors.New("invalid cloud request")
		}
		object["model"], _ = json.Marshal(model)
		return json.Marshal(object)
	}
	if err := c.translationCapability(); err != nil {
		return nil, err
	}
	if !c.portableTo(p) {
		return nil, &CapabilityError{Code: "capability_protocol_conversion", Message: "the configured Profile protocol cannot preserve these request options or tool history; use the source protocol or a proven portable subset"}
	}
	return cloudBuild(p, c.request, model, host, false)
}

// portableTo rejects options whose meaning the existing destination builder
// cannot preserve. Native forwarding retains these fields untouched.
func (c *CloudCodec) portableTo(p Protocol) bool {
	r := c.request
	var rawInput Obj
	json.Unmarshal(c.body, &rawInput)
	// The IR has one output cap. Choosing one of two explicitly supplied Chat
	// caps would silently discard a control; native requests retain both.
	if rawInput["max_tokens"] != nil && rawInput["max_completion_tokens"] != nil {
		return false
	}
	var messages []Obj
	messageInput := rawInput["messages"]
	if c.protocol == Responses {
		messageInput = rawInput["input"]
	}
	json.Unmarshal(messageInput, &messages)
	for _, message := range messages {
		role := Str(message, "role")
		if (role == "user" || role == "assistant") && bytes.Equal(bytes.TrimSpace(message["content"]), []byte(`""`)) && message["tool_calls"] == nil {
			// Responses merges adjacent turns before returning its IR, which
			// can already hide an empty message. Inspect the original as well.
			return false
		}
		if (role == "system" || role == "developer") && StringOrText(message["content"]) == "" {
			// The builders omit an empty System field, losing this message.
			return false
		}
	}
	for _, key := range []string{"reasoning_effort", "thinking", "reasoning"} {
		if _, present := rawInput[key]; present {
			return false
		}
	}
	// Native requests keep these host fields byte-for-byte in JSON values.
	// Their presence must be checked in the original body: the shared parser
	// does not retain all metadata, cache hints or per-message extensions.
	for _, key := range []string{"client_metadata", "include", "metadata", "context_management", "cache_control"} {
		if _, present := rawInput[key]; present {
			return false
		}
	}
	var textOptions Obj
	if json.Unmarshal(rawInput["text"], &textOptions) == nil {
		if _, present := textOptions["verbosity"]; present {
			return false
		}
	}
	if c.protocol == Anthropic && !PortableAnthropicHostFields(rawInput) {
		return false
	}
	if c.protocol == Responses {
		if previous, present := rawInput["previous_response_id"]; present && !bytes.Equal(bytes.TrimSpace(previous), []byte("null")) {
			return false
		}
		var items []Obj
		json.Unmarshal(rawInput["input"], &items)
		for _, item := range items {
			if Str(item, "type") == "reasoning" {
				return false
			}
		}
	}
	var outputConfig Obj
	if json.Unmarshal(rawInput["output_config"], &outputConfig) == nil {
		if _, present := outputConfig["effort"]; present {
			return false
		}
	}
	if p == Responses && len(r.Stop) > 0 || r.Tier != "" || r.Fast || p == Anthropic && (r.CacheKey != "" || r.Temp != nil && r.TopP != nil) {
		return false
	}
	if len(r.Tools) == 0 {
		for _, key := range []string{"parallel_tool_calls", "tool_choice"} {
			if _, present := rawInput[key]; present {
				return false
			}
		}
	}
	for _, tool := range r.Tools {
		if tool.Strict && p != Responses {
			return false
		}
		if p == Anthropic {
			var schema Obj
			json.Unmarshal(tool.Schema, &schema)
			for _, key := range []string{"anyOf", "oneOf", "allOf"} {
				if _, present := schema[key]; present {
					// The legacy Anthropic builder flattens root composition.
					// Business properties and nested schemas remain opaque.
					return false
				}
			}
		}
	}
	if p == Anthropic && r.Format != nil && r.Format.Type == "json_object" {
		// Asking in a system prompt is not the requested API JSON-mode control.
		return false
	}
	seenCalls, pending := map[string]bool{}, map[string]bool{}
	for _, message := range r.Messages {
		if len(message.Parts) == 0 {
			return false
		}
		for _, part := range message.Parts {
			if p == Anthropic && part.Kind == Text && strings.TrimSpace(part.Text) == "" {
				return false
			}
			if part.Kind == Thinking || part.IsError && p != Anthropic {
				return false
			}
			switch part.Kind {
			case ToolCall:
				if part.ID == "" || seenCalls[part.ID] {
					return false
				}
				seenCalls[part.ID], pending[part.ID] = true, true
			case ToolResult:
				if !pending[part.CallID] {
					return false
				}
				delete(pending, part.CallID)
			}
		}
	}
	if len(pending) > 0 {
		return false
	}
	var original struct {
		Tools []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	json.Unmarshal(c.body, &original)
	for _, tool := range original.Tools {
		if c.protocol == Anthropic && tool.Name == "DeferredToolPlaceholder" || c.protocol == Responses && !bytes.Equal(Unsealed(tool.Parameters), tool.Parameters) {
			return false
		}
	}

	return true
}

// Inspect only protocol containers. A tool's input/schema may legitimately
// contain business properties named cache_control, role or output_config.
func PortableAnthropicHostFields(body Obj) bool {
	var blocks []Obj
	if json.Unmarshal(body["system"], &blocks) == nil {
		for _, block := range blocks {
			if _, present := block["cache_control"]; present {
				return false
			}
		}
	}
	var tools []Obj
	json.Unmarshal(body["tools"], &tools)
	for _, tool := range tools {
		if _, present := tool["cache_control"]; present {
			return false
		}
	}
	var messages []Obj
	json.Unmarshal(body["messages"], &messages)
	for _, message := range messages {
		if Str(message, "role") == "system" {
			return false
		}
		if _, present := message["output_config"]; present {
			return false
		}
		var blocks []Obj
		json.Unmarshal(message["content"], &blocks)
		for _, block := range blocks {
			if _, present := block["cache_control"]; present {
				return false
			}
			if Str(block, "type") == "thinking" || Str(block, "type") == "redacted_thinking" {
				return false
			}
			if Str(block, "type") == "tool_result" {
				var results []Obj
				json.Unmarshal(block["content"], &results)
				for _, result := range results {
					if _, present := result["cache_control"]; present {
						return false
					}
				}
			}
		}
	}
	return true
}

type CloudWriter struct {
	http.ResponseWriter
	committed bool
	err       error
}

func (w *CloudWriter) WriteHeader(status int) {
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *CloudWriter) Write(b []byte) (int, error) {
	w.committed = true
	n, err := w.ResponseWriter.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = ErrCloudReply
	}
	return n, err
}

func (w *CloudWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Reply accepts a complete JSON response or SSE, independently of the client's
// output mode. Errors never contain upstream data, URLs, or transport errors.
// On a partial stream it emits a safe error event, without a success terminator.
func (c *CloudCodec) Reply(w http.ResponseWriter, upstreamProtocol, contentType string, body io.Reader) (reply CloudReply, err error) {
	p, err := CloudProtocol(upstreamProtocol)
	if err != nil {
		return reply, err
	}
	out := &CloudWriter{ResponseWriter: w}
	defer func() {
		reply.OutputCommitted = out.committed
		if out.err != nil {
			err = ErrCloudReply
		}
	}()
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil {
		return reply, ErrCloudReply
	}
	var col Collector
	var usageFields uint8
	var reported ReportedUsage
	defer func() {
		if reported.InputTokens != nil || reported.OutputTokens != nil || reported.TotalTokens != nil {
			reply.ReportedUsage = &reported
		}
	}()
	var enc StreamEncoder
	nativeResponses := p == Responses && c.protocol == Responses
	if c.request.Stream && !nativeResponses {
		enc = cloudEncoder(c.protocol, NewSSEWriter(out), c.request)
	}
	failed, stopped, started := false, false, false
	emit := func(ev Event) {
		if ev.Kind == KError {
			failed = true
			return
		}
		if ev.Kind == KStart {
			started = true
		}
		if ev.Kind == KStop {
			stopped = true
		}
		col.Add(ev)
		if enc != nil && !(p == c.protocol && mediaType == "text/event-stream") {
			enc.Event(ev)
		}
	}
	decode := cloudDecoder(p)
	decodeData := func(data string) error {
		if !CloudFrameShape(p, data) {
			return ErrCloudReply
		}
		if p != Chat && cloudReplyEnds(p, data, UpstreamStop([]byte(data))) && !started {
			return ErrCloudReply
		}
		if _, ok := CarriedError(data); ok {
			return ErrCloudReply
		}
		if decode(data, emit) != nil || failed || out.err != nil {
			return ErrCloudReply
		}
		return nil
	}
	if mediaType == "text/event-stream" {
		// A native stream keeps its vendor fields. Delay terminal frames until
		// EOF has been read successfully, so an I/O failure cannot look done.
		native := c.request.Stream && p == c.protocol
		sw := NewSSEWriter(out)
		type frame struct{ name, data string }
		var tail []frame
		tailBytes, streamBytes := 0, 0
		finishing, ended := false, false
		tools := CloudToolBuffer{}
		nativeCollector := nativeResponseCollector{}
		limited := &io.LimitedReader{R: body, N: CloudReplyLimit + 1}
		err = ReadSSE(limited, func(name, data string) error {
			streamBytes += len(name) + len(data)
			if streamBytes > CloudReplyLimit {
				return ErrCloudReply
			}
			if ended || data == "[DONE]" && p != Chat {
				return ErrCloudReply
			}
			if !CloudFrameShape(p, data) {
				return ErrCloudReply
			}
			usageFields |= CloudUsageFields(p, []byte(data))
			reported.read(p, []byte(data))
			if nativeResponses {
				if err := nativeCollector.add(data); err != nil {
					return err
				}
			}
			frames := []string{data}
			if !native && !nativeResponses {
				var e error
				frames, e = tools.frames(p, data)
				if e != nil {
					return e
				}
			}
			for _, frame := range frames {
				if err := decodeData(frame); err != nil {
					return err
				}
			}
			stop := UpstreamStop([]byte(data))
			finishing = finishing || stop != ""
			switch p {
			case Chat:
				ended = strings.TrimSpace(data) == "[DONE]"
			case Anthropic:
				ended = EventType(data) == "message_stop"
			case Responses:
				ended = EventType(data) == "response.completed" || EventType(data) == "response.incomplete"
			}
			finishing = finishing || ended
			if native {
				if finishing {
					tailBytes += len(name) + len(data)
					if tailBytes > CloudReplyLimit {
						return ErrCloudReply
					}
					tail = append(tail, frame{name, data})
				} else {
					sw.Event(name, data)
				}
			}
			return out.err
		})
		if err == nil && (limited.N == 0 || !ended || !stopped) {
			err = ErrCloudReply
		}
		if err == nil && native {
			for _, f := range tail {
				sw.Event(f.name, f.data)
				if out.err != nil {
					break
				}
			}
		}
		if err == nil && nativeResponses && !c.request.Stream {
			var whole []byte
			whole, err = nativeCollector.whole()
			if err == nil {
				out.Header().Set("Content-Type", "application/json")
				_, err = out.Write(whole)
			}
		}
	} else if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		var b []byte
		b, err = io.ReadAll(io.LimitReader(body, CloudReplyLimit+1))
		if err == nil && len(b) <= CloudReplyLimit && CloudWholeComplete(p, b) {
			usageFields |= CloudUsageFields(p, b)
			reported.read(p, b)
			if nativeResponses {
				return c.nativeResponsesJSON(out, b)
			}
			events := WholeEvents(p, b)
			if events == nil {
				err = ErrCloudReply
			}
			tools := CloudToolBuffer{}
			for _, event := range events {
				data, ok := event.Data.(string)
				if !ok {
					encoded, e := json.Marshal(event.Data)
					if e != nil {
						err = ErrCloudReply
						break
					}
					data = string(encoded)
				}
				var frames []string
				frames, err = tools.frames(p, data)
				if err != nil {
					break
				}
				for _, frame := range frames {
					if err = decodeData(frame); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			if err == nil && !stopped {
				err = ErrCloudReply
			}
			if err == nil && !c.request.Stream && p == c.protocol {
				out.Header().Set("Content-Type", "application/json")
				_, err = out.Write(b)
			}
		} else {
			err = ErrCloudReply
		}
	} else {
		err = ErrCloudReply
	}
	res := col.Finish()
	reply.Usage = res.Usage
	reply.UsageKnown = usageFields == 3
	if err != nil || out.err != nil {
		if out.committed && out.err == nil && c.request.Stream {
			CloudStreamError(c.protocol, out)
		}
		return reply, ErrCloudReply
	}
	if enc != nil {
		if !(p == c.protocol && mediaType == "text/event-stream") {
			enc.Finish()
		}
	} else if !out.committed {
		out.Header().Set("Content-Type", "application/json")
		_, err = out.Write(cloudRender(c.protocol, res, c.request))
	}
	return reply, err
}

func CloudStreamError(p Protocol, w http.ResponseWriter) {
	sw := NewSSEWriter(w)
	sw.Begun = true
	failure := map[string]any{"type": "api_error", "message": ErrCloudReply.Error()}
	switch p {
	case Anthropic:
		sw.Event("error", map[string]any{"type": "error", "error": failure})
	case Responses:
		sw.Event("error", map[string]any{"type": "error", "code": "server_error", "message": ErrCloudReply.Error()})
	default:
		sw.Event("", map[string]any{"error": failure})
	}
}

func CloudWholeComplete(p Protocol, b []byte) bool {
	if !CloudWholeShape(p, b) {
		return false
	}
	var v Obj
	if json.Unmarshal(b, &v) != nil || v == nil {
		return false
	}
	if e := v["error"]; len(e) > 0 && string(e) != "null" {
		return false
	}
	switch p {
	case Chat:
		var choices []Obj
		if json.Unmarshal(v["choices"], &choices) != nil || len(choices) == 0 {
			return false
		}
		for _, c := range choices {
			if Str(c, "finish_reason") == "" {
				return false
			}
		}
		return true
	case Responses:
		return Str(v, "object") == "response" && (Str(v, "status") == "completed" || Str(v, "status") == "incomplete")
	case Anthropic:
		return Str(v, "type") == "message" && Str(v, "stop_reason") != ""
	}
	return false
}

func CloudUsageFields(p Protocol, b []byte) uint8 {
	var v Obj
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	var fields uint8
	var candidates []json.RawMessage
	candidates = append(candidates, v["usage"])
	for _, key := range []string{"message", "response"} {
		var nested Obj
		if json.Unmarshal(v[key], &nested) == nil {
			candidates = append(candidates, nested["usage"])
		}
	}
	for _, candidate := range candidates {
		var u Obj
		if json.Unmarshal(candidate, &u) != nil {
			continue
		}
		keys := []string{"input_tokens", "output_tokens"}
		if p == Chat {
			keys = []string{"prompt_tokens", "completion_tokens"}
		}
		for index, key := range keys {
			var count int
			if raw := u[key]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &count) == nil && count >= 0 {
				fields |= 1 << index
			}
		}
	}
	return fields
}

func (u *ReportedUsage) read(p Protocol, b []byte) {
	var frame Obj
	if json.Unmarshal(b, &frame) != nil {
		return
	}
	// Usage may occur at the root or in the protocol's message/response
	// envelope. Never inspect content blocks, tool arguments or other objects.
	candidates := []json.RawMessage{frame["usage"]}
	container := ""
	if p == Anthropic {
		container = "message"
	} else if p == Responses {
		container = "response"
	}
	if container != "" {
		var nested Obj
		if json.Unmarshal(frame[container], &nested) == nil {
			candidates = append(candidates, nested["usage"])
		}
	}
	keys := []string{"input_tokens", "output_tokens", "total_tokens"}
	if p == Chat {
		keys[0], keys[1] = "prompt_tokens", "completion_tokens"
	}
	fields := []**int64{&u.InputTokens, &u.OutputTokens, &u.TotalTokens}
	for _, candidate := range candidates {
		var counts Obj
		if json.Unmarshal(candidate, &counts) != nil {
			continue
		}
		for index, key := range keys {
			var count int64
			raw := bytes.TrimSpace(counts[key])
			if len(raw) != 0 && !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, &count) == nil && count >= 0 {
				value := count
				*fields[index] = &value
			}
		}
	}
}

// The shared IR carries one open call at a time. Collect interleaved wire
// fragments per call, then replay complete calls contiguously before the stop.
// Text can continue streaming while tool fragments await their complete JSON.
type CloudToolBuffer struct {
	calls   []*CloudToolCall
	byKey   map[string]*CloudToolCall
	bytes   int
	flushed bool
	choice  string // first readable Chat choice, using chatChoiceIndex
}

type CloudToolCall struct {
	id, name string
	extra    json.RawMessage
	args     strings.Builder
	full     string
	item     Obj
	choice   json.RawMessage
	index    int
}

func CloudJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (t *CloudToolBuffer) call(key string) *CloudToolCall {
	if t.byKey == nil {
		t.byKey = map[string]*CloudToolCall{}
	}
	if call := t.byKey[key]; call != nil {
		return call
	}
	call := &CloudToolCall{}
	t.calls = append(t.calls, call)
	t.byKey[key] = call
	return call
}

func (t *CloudToolBuffer) frames(p Protocol, data string) ([]string, error) {
	frameBytes := len(data)
	if data != "[DONE]" && !json.Valid([]byte(data)) {
		return nil, ErrCloudReply
	}
	var v Obj
	json.Unmarshal([]byte(data), &v)
	if _, ok := CarriedError(data); ok {
		return nil, ErrCloudReply
	}
	terminal := data == "[DONE]" || cloudReplyEnds(p, data, UpstreamStop([]byte(data)))
	var out []string
	toolFrame := false
	switch p {
	case Chat:
		terminal = data == "[DONE]"
		var choices []Obj
		if raw := v["choices"]; len(raw) > 0 && json.Unmarshal(raw, &choices) != nil {
			return nil, ErrCloudReply
		}
		for _, choice := range choices {
			choiceIndex, indexed := ChatChoiceIndex(choice["index"])
			if indexed {
				if t.choice == "" {
					t.choice = choiceIndex
				} else if t.choice != choiceIndex {
					continue
				}
			}
			terminal = terminal || Str(choice, "finish_reason") != ""
			var delta Obj
			if raw := choice["delta"]; len(raw) > 0 && json.Unmarshal(raw, &delta) != nil {
				return nil, ErrCloudReply
			}
			var calls []CToolCall
			if raw := delta["tool_calls"]; len(raw) > 0 && json.Unmarshal(raw, &calls) != nil {
				return nil, ErrCloudReply
			}
			for i, fragment := range calls {
				toolFrame = true
				index := i
				if fragment.Index != nil {
					index = *fragment.Index
				}
				call := t.call(CloudJSON(index))
				call.index = index
				if indexed {
					call.choice = json.RawMessage(choiceIndex)
				}
				if fragment.ID != "" {
					if call.id != "" && call.id != fragment.ID {
						return nil, ErrCloudReply
					}
					call.id = fragment.ID
				}
				if name := fragment.Function.Name; name != "" && name != call.name {
					call.name += name
				}
				if len(fragment.ExtraContent) > 0 {
					call.extra = fragment.ExtraContent
				}
				call.args.WriteString(fragment.Function.Arguments)
			}
			delete(delta, "tool_calls")
			if delta != nil {
				choice["delta"] = Raw(delta)
			}
		}
		if toolFrame {
			v["choices"] = Raw(choices)
			data = CloudJSON(v)
		}
	case Anthropic:
		var block, delta Obj
		json.Unmarshal(v["content_block"], &block)
		json.Unmarshal(v["delta"], &delta)
		key := string(v["index"])
		switch Str(v, "type") {
		case "content_block_start":
			if Str(block, "type") == "tool_use" {
				toolFrame = true
				call := t.call(key)
				call.id, call.name, call.item = Str(block, "id"), Str(block, "name"), block
				if FullInput(block["input"]) {
					call.full = string(block["input"])
				}
				json.Unmarshal(v["index"], &call.index)
			}
		case "content_block_delta":
			if Str(delta, "type") == "input_json_delta" {
				call := t.byKey[key]
				if call == nil {
					return nil, ErrCloudReply
				}
				call.args.WriteString(Str(delta, "partial_json"))
				toolFrame = true
			}
		case "content_block_stop":
			toolFrame = t.byKey[key] != nil
		}
		if toolFrame {
			data = ""
		}
	case Responses:
		typ := Str(v, "type")
		var item Obj
		json.Unmarshal(v["item"], &item)
		indexKey := "index:" + string(v["output_index"])
		idKey := "id:" + Str(v, "item_id")
		if id := Str(item, "id"); id != "" {
			idKey = "id:" + id
		} else if idKey == "id:" {
			idKey = indexKey
		}
		var call *CloudToolCall
		if typ == "response.output_item.added" && Str(item, "type") == "function_call" || typ == "response.output_item.done" && Str(item, "type") == "function_call" {
			call = t.byKey[idKey]
			if call == nil && len(v["output_index"]) > 0 {
				call = t.byKey[indexKey]
			}
			if call == nil {
				call = t.call(idKey)
			}
			call.id, call.name, call.item = Str(item, "call_id"), Str(item, "name"), item
			if args := Str(item, "arguments"); args != "" {
				call.full = PickArgs(call.full, args)
			}
		} else if strings.HasPrefix(typ, "response.function_call_arguments.") {
			call = t.byKey[idKey]
			if call == nil && len(v["output_index"]) > 0 {
				call = t.byKey[indexKey]
			}
			if call == nil {
				return nil, ErrCloudReply
			}
			if typ == "response.function_call_arguments.delta" {
				call.args.WriteString(Str(v, "delta"))
			} else if args := Str(v, "arguments"); args != "" {
				call.full = PickArgs(call.full, args)
			}
		}
		if call != nil {
			t.byKey[idKey] = call
			if len(v["output_index"]) > 0 {
				t.byKey[indexKey] = call
			}
			toolFrame, data = true, ""
		}
	}
	if toolFrame {
		if t.flushed {
			return nil, ErrCloudReply
		}
		// Count original bytes as well as retained values conservatively.
		t.bytes += frameBytes
		if t.bytes > CloudReplyLimit {
			return nil, ErrCloudReply
		}
	}
	if terminal && !t.flushed {
		var err error
		out, err = t.flush(p)
		if err != nil {
			return nil, err
		}
		t.flushed = true
	}
	if data != "" {
		out = append(out, data)
	}
	return out, nil
}

func (t *CloudToolBuffer) flush(p Protocol) ([]string, error) {
	var frames []string
	// Validate all calls before replaying any one of them.
	for _, call := range t.calls {
		args := call.full
		if p == Responses {
			args = PickArgs(call.args.String(), call.full)
		} else if args == "" {
			args = call.args.String()
		}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		trim := strings.TrimSpace(args)
		if call.id == "" || call.name == "" || !strings.HasPrefix(trim, "{") || !json.Valid([]byte(args)) {
			return nil, ErrCloudReply
		}
		call.full = args
	}
	for _, call := range t.calls {
		switch p {
		case Chat:
			wireCall := map[string]any{"index": call.index, "id": call.id, "type": "function", "function": map[string]any{"name": call.name, "arguments": call.full}}
			if len(call.extra) > 0 {
				wireCall["extra_content"] = call.extra
			}
			choice := Obj{"delta": Raw(map[string]any{"tool_calls": []any{wireCall}})}
			if len(call.choice) > 0 {
				choice["index"] = call.choice
			}
			frames = append(frames, CloudJSON(map[string]any{"choices": []Obj{choice}}))
		case Anthropic:
			block := call.item
			block["input"] = Raw(Obj{})
			frames = append(frames,
				CloudJSON(map[string]any{"type": "content_block_start", "index": call.index, "content_block": block}),
				CloudJSON(map[string]any{"type": "content_block_delta", "index": call.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.full}}),
				CloudJSON(map[string]any{"type": "content_block_stop", "index": call.index}))
		case Responses:
			item := call.item
			item["arguments"] = Raw("")
			frames = append(frames,
				CloudJSON(map[string]any{"type": "response.output_item.added", "item": item}),
				CloudJSON(map[string]any{"type": "response.function_call_arguments.delta", "delta": call.full}))
			item["arguments"] = Raw(call.full)
			frames = append(frames, CloudJSON(map[string]any{"type": "response.output_item.done", "item": item}))
		}
	}
	return frames, nil
}

// The legacy decoders deliberately ignore ill-typed vendor events. A cloud
// caller needs those to be failures, because ignoring text or arguments would
// turn a corrupt reply into a completed empty or different answer. Unknown
// vendor fields remain unrestricted.
func CloudFrameShape(p Protocol, data string) bool {
	if data == "[DONE]" {
		return p == Chat
	}
	var object Obj
	if json.Unmarshal([]byte(data), &object) != nil || object == nil {
		return false
	}
	switch p {
	case Chat:
		var event struct {
			ID, Model string
			Choices   []struct {
				Delta struct {
					Content          json.RawMessage `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
					ToolCalls        []CToolCall     `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *CUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return false
		}
		if _, choices := object["choices"]; !choices && event.Usage == nil {
			return false
		}
		var rawChoices []Obj
		json.Unmarshal(object["choices"], &rawChoices)
		for _, choice := range rawChoices {
			var delta Obj
			if json.Unmarshal(choice["delta"], &delta) != nil || delta == nil {
				return false
			}
		}
		for _, choice := range event.Choices {
			if !CloudTextShape(choice.Delta.Content) {
				return false
			}
		}
	case Anthropic:
		var event struct {
			Type    string
			Index   int
			Usage   *AUsage
			Message *struct {
				ID, Model string
				Usage     *AUsage
			}
			Block ABlock `json:"content_block"`
			Delta struct {
				Type, Text, Thinking, Signature string
				PartialJSON                     string `json:"partial_json"`
				StopReason                      string `json:"stop_reason"`
			}
		}
		if json.Unmarshal([]byte(data), &event) != nil || event.Type == "" {
			return false
		}
		if event.Type == "message_start" && (event.Message == nil || event.Message.ID == "") {
			return false
		}
		if event.Type == "content_block_start" && event.Block.Type == "tool_use" && !CloudObjectShape(event.Block.Input) {
			return false
		}
	case Responses:
		var event struct {
			Type, Delta, Arguments string
			Item                   RItem
			Response               *struct {
				ID, Model, Status string
				Usage             *RUsage
				Output            []RItem
			}
		}
		if json.Unmarshal([]byte(data), &event) != nil || event.Type == "" {
			return false
		}
		if event.Type == "response.created" && (event.Response == nil || event.Response.ID == "") {
			return false
		}
		if event.Type == "response.completed" || event.Type == "response.incomplete" {
			if event.Response == nil || event.Response.Status != "completed" && event.Response.Status != "incomplete" {
				return false
			}
		}
		if event.Item.Type == "function_call" {
			var item Obj
			json.Unmarshal(object["item"], &item)
			if !CloudStringShape(item["arguments"]) {
				return false
			}
		}
	}
	return true
}

func CloudStringShape(b []byte) bool {
	if len(b) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return true
	}
	var s string
	return json.Unmarshal(b, &s) == nil
}

func CloudTextShape(b []byte) bool {
	if CloudStringShape(b) {
		return true
	}
	var parts []struct{ Type, Text, Thinking string }
	return json.Unmarshal(b, &parts) == nil
}

func CloudObjectShape(b []byte) bool {
	if len(b) == 0 {
		return true // some streams omit the initial empty input
	}
	var object Obj
	return json.Unmarshal(b, &object) == nil && object != nil
}

func CloudWholeShape(p Protocol, b []byte) bool {
	switch p {
	case Chat:
		var reply struct {
			ID, Model string
			Usage     *CUsage
			Choices   []struct {
				Message struct {
					Role             string
					Content          json.RawMessage
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string
					Calls            []CToolCall `json:"tool_calls"`
				}
				FinishReason string `json:"finish_reason"`
			}
		}
		if json.Unmarshal(b, &reply) != nil {
			return false
		}
		for _, choice := range reply.Choices {
			if !CloudTextShape(choice.Message.Content) {
				return false
			}
		}
	case Anthropic:
		var reply struct {
			ID, Model, Type string
			Usage           *AUsage
			Content         []ABlock
			StopReason      string `json:"stop_reason"`
		}
		if json.Unmarshal(b, &reply) != nil {
			return false
		}
		for _, block := range reply.Content {
			if block.Type == "tool_use" && !CloudObjectShape(block.Input) {
				return false
			}
		}
	case Responses:
		var reply struct {
			ID, Model, Object, Status string
			Usage                     *RUsage
			Output                    []struct {
				ID, CallID, Name, Type string
				Arguments              json.RawMessage
				Content                []struct{ Type, Text, Refusal string }
			}
		}
		if json.Unmarshal(b, &reply) != nil {
			return false
		}
		for _, item := range reply.Output {
			if item.Type == "function_call" && !CloudStringShape(item.Arguments) {
				return false
			}
		}
	}
	return true
}
