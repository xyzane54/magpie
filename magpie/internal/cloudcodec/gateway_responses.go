package cloudcodec

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type RItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	// function_call / function_call_output
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments RArgs           `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"` // custom_tool_call's
	Output    json.RawMessage `json:"output,omitempty"`
	Status    string          `json:"status,omitempty"`
	// tool_search_output: the tools Codex's search found, which the model
	// may call from then on
	Tools []RTool `json:"tools,omitempty"`
	// reasoning
	Summary          []RText `json:"summary,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	// web_search_call
	Action *struct {
		Query   string `json:"query"`
		Sources []struct {
			URL string `json:"url"`
		} `json:"sources"`
	} `json:"action,omitempty"`
}

// rArgs is a call's arguments: a JSON string on a function_call, an object
// on Codex's tool_search_call.
type RArgs string

func (a *RArgs) UnmarshalJSON(b []byte) error {
	var s string
	if DecodeJSON(b, &s) == nil {
		*a = RArgs(s)
	} else if string(b) != "null" {
		*a = RArgs(b)
	}
	return nil
}

type RText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type RTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Tools       []RTool         `json:"tools,omitempty"` // a namespace's
	Execution   string          `json:"execution,omitempty"`
	Format      *RFormat        `json:"format,omitempty"` // a custom tool's
}

// rFormat is what a custom (freeform) tool's input is: "text", or a
// "grammar" in a syntax ("lark", "regex") with its definition.
type RFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

// customSchema is the arguments a custom tool is offered as taking.
var CustomSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"The tool's free-form input, the whole of it, exactly as it is to be run."}},"required":["input"],"additionalProperties":false}`)

// customDescription is a custom tool's description as a function's: its
// own, what its input is, and the grammar the input follows.
func CustomDescription(t RTool) string {
	d := strings.TrimSpace(t.Description)
	if d != "" {
		d += "\n\n"
	}
	d += "Put the tool's whole free-form input, as it is to be run, in the `input` string; it is passed on as written, so it is not itself JSON."
	if f := t.Format; f != nil && f.Type == "grammar" && strings.TrimSpace(f.Definition) != "" {
		syntax := f.Syntax
		if syntax == "" {
			syntax = "the"
		}
		d += "\n\nThe input follows this " + syntax + " grammar:\n" + strings.TrimSpace(f.Definition)
	}
	return d
}

// customInput is the free-form input of a call the model made to a custom
// tool offered as a function: its input argument, else what it gave.
func CustomInput(args string) string {
	var a struct {
		Input *string `json:"input"`
	}
	if DecodeJSON([]byte(args), &a) == nil && a.Input != nil {
		return *a.Input
	}
	var str string
	if DecodeJSON([]byte(args), &str) == nil {
		return str
	}
	return args
}

// toolSearch is Codex's tool search. With it Codex names its MCP tools (the
// ChatGPT apps' among them) and its sub-agent tools only in the search's
// description, and hands the model the ones it searched for in a
// tool_search_output, instead of every schema on every request (#258).
// Codex runs the search itself ("execution": "client"): a model magpie
// translates for is offered it as the function it is, and its call goes
// back to Codex as the tool_search_call Codex runs.
const ToolSearch = "tool_search"

// searchFound is what a model reads of a tool search's result: the tools it
// may call now, by the names it is offered them under.
func SearchFound(tools []RTool) string {
	var names []string
	for _, t := range tools {
		switch t.Type {
		case "function":
			names = append(names, t.Name)
		case "namespace":
			for _, nt := range t.Tools {
				if nt.Type == "function" {
					names = append(names, ProtocolFlatName(t.Name, nt.Name))
				}
			}
		}
	}
	if len(names) == 0 {
		return "No tools matched the search."
	}
	return "These tools are now available to call: " + strings.Join(names, ", ")
}

// flatName is the name a namespaced tool is offered to a model under,
// namespace__name, the same a Grok subscription is offered it under.
// unsealed is a tool's parameters without the "encrypted" marks Codex puts
// on some (spawn_agent's message): a translated request's calls go back to
// Codex as unsealed (callTo's encrypted_function_args), so an upstream that
// honours the mark must not seal them (#613).
func Unsealed(schema json.RawMessage) json.RawMessage {
	if !bytes.Contains(schema, []byte(`"encrypted"`)) {
		return schema
	}
	var v any
	if DecodeJSON(schema, &v) != nil {
		return schema
	}
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["encrypted"].(bool); ok {
				delete(x, "encrypted")
			}
			for _, c := range x {
				strip(c)
			}
		case []any:
			for _, c := range x {
				strip(c)
			}
		}
	}
	strip(v)
	out, err := MarshalPlain(v)
	if err != nil {
		return schema
	}
	return out
}

func ProtocolFlatName(namespace, name string) string {
	return FlatName(namespace, name)
}

type RRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             json.RawMessage `json:"input"`
	Tools             []RTool         `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	PromptCacheKey    string          `json:"prompt_cache_key,omitempty"`
	Include           []string        `json:"include,omitempty"`
	ClientMetadata    json.RawMessage `json:"client_metadata,omitempty"`
	Text              json.RawMessage `json:"text,omitempty"`
	Reasoning         *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
}

// textFormat splits a Responses client's text into the rest of it and
// the format it asks the answer in (text.format), which is asked for again
// in each upstream's own words. A format of plain text stays where it was.
func TextFormat(text json.RawMessage) (json.RawMessage, *Format) {
	var fields map[string]json.RawMessage
	if DecodeJSON(text, &fields) != nil {
		return text, nil
	}
	f := OpenAIFormat(fields["format"])
	if f == nil {
		return text, nil
	}
	delete(fields, "format")
	if len(fields) == 0 {
		return nil, f
	}
	rest, _ := json.Marshal(fields)
	return rest, f
}

// sentRaw is a raw field the client sent, unless it sent null.
func SentRaw(v json.RawMessage) json.RawMessage {
	if t := strings.TrimSpace(string(v)); t == "" || t == "null" {
		return nil
	}
	return v
}

func ParseResponses(body []byte) (*Request, error) {
	// Responses Lite's tools, sent as the first input item (#350)
	body = LiftAdditionalTools(body)
	var q RRequest
	if err := DecodeJSON(body, &q); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: q.Model, System: q.Instructions, MaxTokens: q.MaxOutputTokens, Temp: q.Temperature,
		TopP: q.TopP, Stream: q.Stream, Parallel: q.ParallelToolCalls, Fast: q.ServiceTier == "priority" || q.ServiceTier == "ultrafast", Ultrafast: q.ServiceTier == "ultrafast", Tier: q.ServiceTier, CacheKey: q.PromptCacheKey, Include: q.Include,
		ClientMetadata: SentRaw(q.ClientMetadata)}
	r.Text, r.Format = TextFormat(SentRaw(q.Text))
	if q.Reasoning != nil {
		r.Effort = EffortOf(q.Reasoning.Effort)
		r.Thinking = true
		r.ThinkOff = strings.EqualFold(strings.TrimSpace(q.Reasoning.Effort), "none")
	}
	var found []RTool // what Codex's tool searches found
	var s string
	if DecodeJSON(q.Input, &s) == nil {
		r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}})
	} else {
		var items []RItem
		if err := DecodeJSON(q.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input: %v", err)
		}
		// replied: the conversation has had a turn answered. Codex adds a
		// developer message where its context changed (world state, settings,
		// a model switch) and keeps it there; lifted into the system prompt it
		// changed the prompt's very start, so the whole conversation was
		// written to the vendor's cache again (Anthropic's system comes
		// first; a Claude subscription's waiting run is found by it) (#502)
		replied := false
		var rawItems []json.RawMessage // decoded only for native standalone outputs and sealed reasoning
		for i, it := range items {
			if it.Role == "assistant" || strings.HasPrefix(it.Type, "function_call") || strings.HasPrefix(it.Type, "custom_tool_call") || strings.HasPrefix(it.Type, "tool_search") || it.Type == "reasoning" {
				replied = true
			}
			switch {
			case it.Type == "message" || (it.Type == "" && it.Role != ""):
				role := "user"
				if it.Role == "assistant" {
					role = "assistant"
				}
				parts := ResponsesParts(it.Content)
				if (it.Role == "system" || it.Role == "developer") && replied {
					// in place, told as the system's, not the user's words
					if t := ProtocolText(parts); t != "" {
						r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: "<system-reminder>\n" + t + "\n</system-reminder>"}}})
					}
					continue
				}
				if it.Role == "system" || it.Role == "developer" {
					if t := ProtocolText(parts); t != "" {
						if r.System != "" {
							r.System += "\n\n"
						}
						r.System += t
					}
					continue
				}
				r.Messages = append(r.Messages, Message{Role: role, Parts: parts})
			case it.Type == "agent_message":
				// MultiAgentV2 hands a subagent its task, and agents their
				// messages to each other, as this item: what it says is the
				// user's turn for the agent that receives it.
				if parts := ResponsesParts(it.Content); len(parts) > 0 {
					r.Messages = append(r.Messages, Message{Role: "user", Parts: parts})
				}
			case it.Type == "function_call":
				name := it.Name
				if it.Namespace != "" {
					name = ProtocolFlatName(it.Namespace, it.Name)
				}
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: name, Args: ParseArgs(string(it.Arguments))}}})
			case it.Type == "custom_tool_call":
				name := it.Name
				if it.Namespace != "" {
					name = ProtocolFlatName(it.Namespace, it.Name)
				}
				args, _ := json.Marshal(map[string]string{"input": it.Input})
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: name, Args: args}}})
			case it.Type == "tool_search_call":
				r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: ToolSearch, Args: ParseArgs(string(it.Arguments))}}})
			case it.Type == "tool_search_output":
				found = append(found, it.Tools...)
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: it.CallID, Text: SearchFound(it.Tools)}}})
			case it.Type == "function_call_output" || it.Type == "custom_tool_call_output":
				out, images := ToolOutput(it.Output)
				part := Part{Kind: ToolResult, CallID: it.CallID, Text: out, Images: images}
				if it.CallID == "" {
					// Native account requests may still need translation to
					// collect a streamed response for a non-streaming client.
					// Retain the original notification, including absent IDs.
					if rawItems == nil {
						_ = DecodeJSON(q.Input, &rawItems)
					}
					_ = DecodeJSON(rawItems[i], &part.Standalone)
				}
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{part}})
			case it.Type == "reasoning":
				// the reasoning itself when the item carries it, else its
				// summary (all magpie gives a client of a translated reply)
				var b strings.Builder
				var content []RText
				_ = DecodeJSON(it.Content, &content)
				for _, c := range content {
					if c.Type == "reasoning_text" {
						b.WriteString(c.Text)
					}
				}
				if b.Len() == 0 {
					for _, s := range it.Summary {
						b.WriteString(s.Text)
					}
				}
				part := Part{Kind: Thinking, Text: b.String()}
				if it.EncryptedContent != "" {
					// sealed: the item goes back to a Responses API as it
					// came, id, summary and all (#1445)
					if rawItems == nil {
						_ = DecodeJSON(q.Input, &rawItems)
					}
					if i < len(rawItems) {
						part.Sealed, part.SealedBy = rawItems[i], SealResponses
					}
				}
				if b.Len() > 0 || part.Sealed != nil {
					r.Messages = append(r.Messages, Message{Role: "assistant", Parts: []Part{part}})
				}
			}
		}
	}
	r.Messages = MergeTurns(r.Messages)
	sent := len(q.Tools)
	offer := func(i int, t Tool, ns NsTool) {
		// a tool searched for twice, or sent as well, is offered once
		if i >= sent && slices.ContainsFunc(r.Tools, func(o Tool) bool { return o.Name == t.Name }) {
			return
		}
		if ns != (NsTool{}) {
			if r.Namespaced == nil {
				r.Namespaced = map[string]NsTool{}
			}
			r.Namespaced[t.Name] = ns
		}
		r.Tools = append(r.Tools, t)
	}
	// the tools Codex's searches found are offered after those it sent, as
	// Codex does not send them again
	for i, t := range append(q.Tools, found...) {
		if strings.HasPrefix(t.Type, "web_search") {
			r.WebSearch = true
		}
		switch t.Type {
		case "function":
			offer(i, Tool{Name: t.Name, Description: t.Description, Schema: Unsealed(t.Parameters), Strict: t.Strict != nil && *t.Strict}, NsTool{})
		case "custom":
			offer(i, Tool{Name: t.Name, Description: CustomDescription(t), Schema: CustomSchema}, NsTool{Name: t.Name, Custom: true})
		case "namespace":
			// offered flat, as few models know namespaces; a call is given
			// its namespace back on the way out
			for _, nt := range t.Tools {
				flat := ProtocolFlatName(t.Name, nt.Name)
				switch nt.Type {
				case "function":
					offer(i, Tool{Name: flat, Description: nt.Description, Schema: Unsealed(nt.Parameters), Strict: nt.Strict != nil && *nt.Strict}, NsTool{Namespace: t.Name, Name: nt.Name})
				case "custom":
					offer(i, Tool{Name: flat, Description: CustomDescription(nt), Schema: CustomSchema}, NsTool{Namespace: t.Name, Name: nt.Name, Custom: true})
				}
			}
		case ToolSearch:
			if t.Execution == "client" {
				offer(i, Tool{Name: ToolSearch, Description: t.Description, Schema: t.Parameters}, NsTool{Search: true})
			}
		}
	}
	var tc string
	if DecodeJSON(q.ToolChoice, &tc) == nil {
		r.ToolChoice = tc
	} else {
		var o struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Mode  string `json:"mode"`
			Tools []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"tools"`
		}
		if DecodeJSON(q.ToolChoice, &o) == nil {
			switch o.Type {
			case "allowed_tools":
				if o.Mode != "auto" && o.Mode != "required" {
					return nil, fmt.Errorf("invalid allowed_tools mode %q", o.Mode)
				}
				r.ToolChoice = o.Mode
				allowed := make(map[string]bool, len(o.Tools))
				webSearch := false
				for _, tool := range o.Tools {
					switch {
					case (tool.Type == "function" || tool.Type == "custom") && tool.Name != "":
						if tool.Namespace != "" {
							allowed[ProtocolFlatName(tool.Namespace, tool.Name)] = true
						} else {
							allowed[tool.Name] = true
						}
					case strings.HasPrefix(tool.Type, "web_search"):
						webSearch = true
					}
				}
				r.WebSearch = r.WebSearch && webSearch
				var tools []Tool
				for _, tool := range r.Tools {
					if allowed[tool.Name] {
						tools = append(tools, tool)
					}
				}
				r.Tools = tools
				for name := range r.Namespaced {
					if !allowed[name] {
						delete(r.Namespaced, name)
					}
				}
			case "function":
				if o.Name != "" {
					r.ToolChoice = "name:" + o.Name
				}
			}
		}
	}
	return r, nil
}

// toolCallItem is a Responses input item that makes a tool call, and
// toolResultItem one that answers a call.
func ToolCallItem(t string) bool {
	switch t {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	}
	return false
}

func ToolResultItem(t string) bool {
	switch t {
	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		return true
	}
	return false
}

// pairToolItems gives a Responses request's tool call that no result in
// the request answers a synthetic one, as pairToolMessages does for a Chat
// request: an upstream that checks the exchange on the chat form it turns
// the request into refuses a lone call — Volcengine's coding plan answers
// 400 "An assistant message with 'tool_calls' must be followed by tool
// messages responding to each 'tool_call_id'" (#1341), and OpenAI's own
// Responses API "No tool output found for function call". The result goes
// at the end of the run of calls and results the call is in, where a chat
// form has its tool messages. A result with no call is left to
// orphanedToolOutputs, and one naming a call another request carried
// (previous_response_id) stays as it is.
func PairToolItems(body []byte) []byte {
	if !bytes.Contains(body, []byte(`_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if DecodeJSON(body, &q) != nil {
		return body
	}
	var input []json.RawMessage
	if DecodeJSON(q["input"], &input) != nil {
		return body
	}
	type item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	items := make([]item, len(input))
	answered := map[string]bool{}
	for i, raw := range input {
		if DecodeJSON(raw, &items[i]) != nil {
			items[i] = item{}
		}
		if t := items[i].Type; (t == "function_call_output" || t == "custom_tool_call_output") && items[i].CallID != "" {
			answered[items[i].CallID] = true
		}
	}
	exchange := func(t string) bool {
		return ToolCallItem(t) || ToolResultItem(t)
	}
	out := make([]json.RawMessage, 0, len(input)+1)
	var synthetic []json.RawMessage // results owed by the run in progress
	for i, raw := range input {
		out = append(out, raw)
		it := items[i]
		if (it.Type == "function_call" || it.Type == "custom_tool_call") && it.CallID != "" && !answered[it.CallID] {
			kind := "function_call_output"
			if it.Type == "custom_tool_call" {
				kind = "custom_tool_call_output"
			}
			result, _ := MarshalPlain(map[string]any{"type": kind, "call_id": it.CallID,
				"output": "[The result of this tool call is unavailable: the turn was interrupted.]"})
			synthetic = append(synthetic, result)
			answered[it.CallID] = true
		}
		if len(synthetic) > 0 && exchange(it.Type) && (i+1 == len(input) || !exchange(items[i+1].Type)) {
			out = append(out, synthetic...)
			synthetic = nil
		}
	}
	if len(out) == len(input) {
		return body
	}
	q["input"], _ = MarshalPlain(out)
	encoded, err := MarshalPlain(q)
	if err != nil {
		return body
	}
	return encoded
}

// mergeTurns joins consecutive messages of the same role, since the
// Responses API splits an assistant turn into one item per part.
func MergeTurns(msgs []Message) []Message {
	var out []Message
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			continue
		}
		out = append(out, m)
	}
	return out
}

func ResponsesParts(raw json.RawMessage) []Part {
	var s string
	if DecodeJSON(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Part{{Kind: Text, Text: s}}
	}
	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	DecodeJSON(raw, &items)
	var out []Part
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text":
			out = append(out, Part{Kind: Text, Text: it.Text})
		case "input_image":
			if it.ImageURL != "" {
				out = append(out, ImagePart(it.ImageURL))
			}
		}
	}
	return out
}

// replaysReasoning is whether model, served by host's Responses API, wants
// a turn's reasoning text back between tool calls (DeepSeek: "The
// reasoning_text in the thinking mode must be passed back", #388). OpenAI's,
// xAI's, Copilot's and Azure's read only their own sealed reasoning.
func ReplaysReasoning(model, host string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek") &&
		!slices.Contains([]string{"chatgpt.com", "api.openai.com", "api.x.ai", "api.githubcopilot.com"}, host) &&
		!strings.HasSuffix(host, ".openai.azure.com")
}

// stepKind is what a Responses input item is to a step of the model's: its
// reasoning, something it said or a call it made, what answered a call, or
// a message of the user's (or the system's) that starts a turn.
type StepKind int

const (
	StepOther StepKind = iota
	StepReasoning
	StepSaid
	StepCall
	StepOutput
	StepTurn
)

func StepKindOf(typ, role string) StepKind {
	switch {
	case typ == "reasoning":
		return StepReasoning
	case strings.HasSuffix(typ, "_output"):
		return StepOutput
	case strings.HasSuffix(typ, "_call"):
		return StepCall
	case (typ == "message" || typ == "") && role == "assistant":
		return StepSaid
	case typ == "message" || typ == "" && role != "":
		return StepTurn
	}
	return StepOther
}

// noReasoningItem is the reasoning item a step leads with when it has none
// of its own (reasonFirst).
func NoReasoningItem() map[string]any {
	return map[string]any{"type": "reasoning", "summary": []any{},
		"content": []map[string]any{{"type": "reasoning_text", "text": NoReasoning}}}
}

// reasonFirst has each step of the model's (a run of its reasoning, what it
// said and the calls it made, between a user message or a tool output and
// the next) that makes a call, or that follows a tool output, lead with a
// reasoning item: the step's first one moved ahead of the rest, else none.
// DeepSeek's /v1/responses reads a step as one assistant message whose
// thinking is the reasoning item it starts with, and turns the whole
// request away ("The `reasoning_text` in the thinking mode must be passed
// back to the API") when a step of the turn being continued starts with
// anything else: a call (#1592), or what the model said, as Codex sends a
// step that said something before its call with its reasoning between them,
// or with none, which magpie put between them (#1822). Checked against
// api.deepseek.com on 2026-10-12: [text, reasoning, call] and [output,
// text] are refused, [reasoning, text, call] and [output, reasoning, text]
// are taken. ok is false, and items comes back as it is, when every such
// step leads with one already.
func ReasonFirst[T any](items []T, kinds []StepKind, none T) (led []T, ok bool) {
	step := func(k StepKind) bool { return k == StepReasoning || k == StepSaid || k == StepCall }
	afterOutput := false
	for i := 0; i < len(items); {
		if !step(kinds[i]) {
			switch kinds[i] {
			case StepOutput:
				afterOutput = true
			case StepTurn:
				afterOutput = false
			}
			if ok {
				led = append(led, items[i])
			}
			i++
			continue
		}
		j, calls, first := i, false, -1
		for ; j < len(items) && step(kinds[j]); j++ {
			calls = calls || kinds[j] == StepCall
			if kinds[j] == StepReasoning && first < 0 {
				first = j
			}
		}
		if (calls || afterOutput) && first != i {
			if !ok {
				led, ok = append(make([]T, 0, len(items)+1), items[:i]...), true
			}
			if first < 0 {
				led = append(led, none)
			} else {
				led = append(led, items[first])
			}
			for x := i; x < j; x++ {
				if x != first {
					led = append(led, items[x])
				}
			}
		} else if ok {
			led = append(led, items[i:j]...)
		}
		i = j
	}
	if !ok {
		return items, false
	}
	return led, true
}

// sealedReasoning is a reasoning item a Responses API sealed, fit to go
// back to one as it came: encrypted_content in it, and no reasoning text
// (which OpenAI and the ChatGPT backend refuse in input, #1411; one with
// text goes as before).
func SealedReasoning(p Part) bool {
	if !p.sealedBy(SealResponses) {
		return false
	}
	var it struct {
		Enc     string            `json:"encrypted_content"`
		Content []json.RawMessage `json:"content"`
	}
	return DecodeJSON(p.Sealed, &it) == nil && it.Enc != "" && len(it.Content) == 0
}

// buildResponses renders a request for a Responses upstream.
func BuildResponses(r *Request, model, host string, rejectTemp bool) []byte {
	// A turn's reasoning goes back as a reasoning item, as a model that
	// thinks between tool calls wants it (DeepSeek: "The reasoning_text in
	// the thinking mode must be passed back", #388). Only a DeepSeek model
	// gets it: OpenAI's and those in front of it read only their own
	// sealed reasoning, and may refuse an item without it.
	replay := ReplaysReasoning(model, host)
	var input []any
	// a second answer to one call is left out, as the Chat and Messages
	// builds leave it (dropSecondAnswers)
	for _, m := range DropSecondAnswers(r.Messages) {
		var content []map[string]any
		flushMsg := func() {
			if len(content) == 0 {
				return
			}
			input = append(input, map[string]any{"type": "message", "role": m.Role, "content": content})
			content = nil
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text == "" && p.Signature != "" {
					continue // only Gemini's signature, which is Gemini's
				}
				t := "input_text"
				if m.Role == "assistant" {
					t = "output_text"
				}
				content = append(content, map[string]any{"type": t, "text": p.Text})
			case File:
				t := "input_text"
				if m.Role == "assistant" {
					t = "output_text"
				}
				content = append(content, map[string]any{"type": t, "text": AttachmentText(p)})
			case Image:
				if m.Role != "assistant" {
					content = append(content, map[string]any{"type": "input_image", "detail": "auto", "image_url": DataURL(p)})
				}
			case Thinking:
				if !replay && SealedReasoning(p) {
					// a Responses API's own sealed reasoning goes back as
					// it came: id, summary and encrypted_content (#1445)
					flushMsg()
					input = append(input, p.Sealed)
				} else if replay && p.Text != "" {
					flushMsg()
					input = append(input, map[string]any{"type": "reasoning", "summary": []any{},
						"content": []map[string]any{{"type": "reasoning_text", "text": p.Text}}})
				}
			case ToolCall:
				flushMsg()
				id := p.ID
				if id == "" {
					id = "call_" + NewID()
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": p.Name, "arguments": ArgsString(p)})
			case ToolResult:
				flushMsg()
				if p.Standalone != nil {
					input = append(input, p.Standalone)
					continue
				}
				var output any = p.Text
				if len(p.Images) > 0 {
					// an output can be a list of text and images
					var items []map[string]any
					if strings.TrimSpace(p.Text) != "" {
						items = append(items, map[string]any{"type": "input_text", "text": p.Text})
					}
					for _, im := range p.Images {
						items = append(items, map[string]any{"type": "input_image", "detail": "auto", "image_url": DataURL(im)})
					}
					output = items
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": output})
			}
		}
		flushMsg()
	}
	if replay {
		// each step that makes a call, or follows a tool output, leads with
		// its reasoning, or with a space's (#1592, #1822)
		kinds := make([]StepKind, len(input))
		for i, it := range input {
			var k struct {
				Type string `json:"type"`
				Role string `json:"role"`
			}
			switch v := it.(type) {
			case map[string]any:
				k.Type, _ = v["type"].(string)
				k.Role, _ = v["role"].(string)
			case json.RawMessage:
				DecodeJSON(v, &k)
			}
			kinds[i] = StepKindOf(k.Type, k.Role)
		}
		input, _ = ReasonFirst(input, kinds, any(NoReasoningItem()))
	}
	if input == nil {
		input = []any{}
	}
	out := map[string]any{"model": model, "input": input, "stream": r.Stream, "store": false}
	if len(r.ClientMetadata) > 0 {
		out["client_metadata"] = r.ClientMetadata
	}
	if len(r.Text) > 0 || r.Format != nil {
		text := map[string]any{}
		DecodeJSON(r.Text, &text)
		if r.Format != nil {
			text["format"] = r.Format.responses()
		}
		out["text"] = text
	}
	if r.CacheKey != "" {
		out["prompt_cache_key"] = r.CacheKey
	}
	if r.System != "" {
		out["instructions"] = r.System
	}
	if r.MaxTokens > 0 {
		out["max_output_tokens"] = r.MaxTokens
	}
	if !rejectTemp {
		if r.Temp != nil {
			out["temperature"] = *r.Temp
		}
		if r.TopP != nil {
			out["top_p"] = *r.TopP
		}
	}
	// Fast goes to OpenAI's own backends; another's may not know the tier
	if r.Fast && (host == "chatgpt.com" || host == "api.openai.com") {
		out["service_tier"] = "priority"
	}
	// Ultrafast only the ChatGPT backend offers
	if r.Ultrafast && host == "chatgpt.com" {
		out["service_tier"] = "ultrafast"
	}
	if r.OwnTier && r.Tier != "" {
		out["service_tier"] = r.Tier
	}
	if r.Effort != "" {
		out["reasoning"] = map[string]any{"effort": r.Effort, "summary": "auto"}
	} else if r.Thinking {
		out["reasoning"] = map[string]any{"summary": "auto"}
	}
	// the client's include goes on as the request would have without
	// magpie. Sealed reasoning is asked for only with reasoning, as Codex
	// asks for it: OpenAI refuses it of a model that doesn't reason. What
	// comes back sealed goes to a Responses client as it came, and the
	// input's goes back to a Responses upstream, as it would without magpie
	// (#1445); an account or vendor that can't read another's turns it away,
	// and is asked again without it (foreignReasoning, withoutRefused).
	var include []string
	for _, v := range r.Include {
		if v == "" || slices.Contains(include, v) || (v == "reasoning.encrypted_content" && out["reasoning"] == nil) {
			continue
		}
		include = append(include, v)
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			// strict is said, as Codex says it: left out, the ChatGPT
			// backend holds the schema to strict mode's rules and refuses a
			// pattern with a lookaround (MiniMax Code's path, #383)
			tool := map[string]any{"type": "function", "name": t.Name, "description": t.Description, "strict": t.Strict}
			if len(t.Schema) > 0 {
				tool["parameters"] = t.Schema
			}
			tools = append(tools, tool)
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search"})
			// the pages it found, which a client of another protocol is
			// told of (OpenAI's option; xAI's API isn't known to take it)
			if host != "api.x.ai" && !slices.Contains(include, "web_search_call.action.sources") {
				include = append(include, "web_search_call.action.sources")
			}
		}
		out["tools"] = tools
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "none" || r.ToolChoice == "required":
			out["tool_choice"] = r.ToolChoice
		case strings.HasPrefix(r.ToolChoice, "name:"):
			out["tool_choice"] = map[string]any{"type": "function", "name": strings.TrimPrefix(r.ToolChoice, "name:")}
		}
		if r.Parallel != nil {
			out["parallel_tool_calls"] = *r.Parallel
		}
	}
	if len(include) > 0 {
		out["include"] = include
	}
	b, _ := json.Marshal(out)
	// a call the client left unanswered (a turn it interrupted) gets its
	// synthetic result, as buildChat's pairToolMessages and
	// buildAnthropic's pairToolBlocks give one: the Responses API refuses
	// the lone call, "No tool output found for function call <id>", on
	// every turn after it too (#2222)
	return PairToolItems(b)
}

type RUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
		// what was written to the cache, which Codex reads too (#589)
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// usage: input_tokens is the whole prompt, what was read from the cache and
// what was written to it among it.
func (u RUsage) Usage() Usage {
	d := u.InputTokensDetails
	return Usage{Input: max(u.InputTokens-d.CachedTokens-d.CacheWriteTokens, 0), Output: u.OutputTokens,
		CacheRead: d.CachedTokens, CacheWrite: d.CacheWriteTokens, Reasoning: u.OutputTokensDetails.ReasoningTokens}
}

func (u Usage) Responses() map[string]any {
	in := u.Prompt()
	return map[string]any{"input_tokens": in, "output_tokens": u.Output, "total_tokens": in + u.Output,
		"input_tokens_details":  map[string]any{"cached_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite},
		"output_tokens_details": map[string]any{"reasoning_tokens": u.Reasoning}}
}

// responsesDecoder turns a Responses stream into events.
//
// A function call's arguments are given once the call is done, not as
// their deltas come: the deltas can leave out what the finished call holds
// (#613: Codex's spawn_agent came back with arguments {} where the request
// was translated, though the upstream's finished call had them), and what
// was sent of a call's arguments can't be taken back.
type ResponsesDecoder struct {
	started bool
	called  bool            // a function call was streamed
	calling bool            // a function call is open
	args    strings.Builder // the open call's argument deltas
	full    string          // the open call's arguments as its done events give them
}

// endCall gives the open call's arguments: the deltas, or what its done
// events give where that holds more.
func (d *ResponsesDecoder) endCall(emit func(Event)) {
	if !d.calling {
		return
	}
	d.calling = false
	args := PickArgs(d.args.String(), d.full)
	d.args.Reset()
	d.full = ""
	if args != "" {
		emit(Event{Kind: KToolArgs, Text: args})
	}
}

// pickArgs is the fuller of a call's arguments as streamed and as its done
// events give them, JSON first.
func PickArgs(streamed, done string) string {
	s, f := strings.TrimSpace(streamed), strings.TrimSpace(done)
	switch sv, fv := json.Valid([]byte(s)), json.Valid([]byte(f)); {
	case fv && (!sv || len(f) > len(s)):
		return done
	case sv || f == "":
		return streamed
	default:
		return done
	}
}

func (d *ResponsesDecoder) Decode(data string, emit func(Event)) error {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Item  RItem  `json:"item"`
		// function_call_arguments.done's
		Arguments string `json:"arguments"`
		Response  struct {
			ID                string `json:"id"`
			Model             string `json:"model"`
			Status            string `json:"status"`
			Usage             RUsage `json:"usage"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Output []RItem `json:"output"`
		} `json:"response"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := DecodeJSON([]byte(data), &ev); err != nil {
		// an error in a shape of its own is still the reply's end (#1933)
		if e, ok := CarriedError(data); ok {
			d.endCall(emit)
			emit(e)
		}
		return nil
	}
	switch ev.Type {
	case "response.created":
		if !d.started {
			d.started = true
			emit(Event{Kind: KStart, MsgID: ev.Response.ID, Model: ev.Response.Model})
		}
	case "response.output_item.added":
		d.endCall(emit)
		if ev.Item.Type == "function_call" {
			d.called, d.calling = true, true
			emit(Event{Kind: KToolStart, ID: ev.Item.CallID, Name: ev.Item.Name})
		}
		if ev.Item.Type == "reasoning" {
			// an item of its own, by the id the upstream gave it (#1445)
			emit(Event{Kind: KThinkStart, ID: ev.Item.ID})
		}
	case "response.output_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KText, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		d.endCall(emit)
		emit(Event{Kind: KThink, Text: ev.Delta})
	case "response.function_call_arguments.delta":
		if d.calling {
			d.args.WriteString(ev.Delta)
		} else {
			emit(Event{Kind: KToolArgs, Text: ev.Delta})
		}
	case "response.function_call_arguments.done":
		if d.calling && ev.Arguments != "" {
			d.full = ev.Arguments
		}
	case "response.output_item.done":
		if ev.Item.Type == "function_call" {
			if ev.Item.Arguments != "" {
				d.full = string(ev.Item.Arguments)
			}
			d.endCall(emit)
		}
		if ev.Item.Type == "reasoning" && ev.Item.EncryptedContent != "" {
			// sealed: the item as the upstream wrote it, for a Responses
			// client and for its next request (#1445)
			var whole struct {
				Item json.RawMessage `json:"item"`
			}
			if DecodeJSON([]byte(data), &whole) == nil && len(whole.Item) > 0 {
				emit(Event{Kind: KSealed, Name: SealResponses, ID: ev.Item.ID, Text: string(whole.Item)})
			}
		}
		if a := ev.Item.Action; ev.Item.Type == "web_search_call" && a != nil && a.Query != "" {
			var hits []Hit
			for _, src := range a.Sources {
				if src.URL != "" {
					hits = append(hits, Hit{Title: src.URL, URL: src.URL})
				}
			}
			emit(Event{Kind: KSearch, Text: a.Query, Hits: hits})
		}
	case "response.completed", "response.incomplete", "response.failed":
		d.endCall(emit)
		if ev.Response.Error != nil || ev.Type == "response.failed" || ev.Response.Status == "failed" {
			// a failed response is an error with or without the words
			// for it, never a whole reply (#1933)
			msg := ""
			if ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			emit(Event{Kind: KError, Text: cmp.Or(msg, StreamErrorText(data)), Code: RefusedCode(data)})
			return nil
		}
		stop := "stop"
		if ev.Response.Status == "incomplete" {
			stop = "length"
			if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "content_filter" {
				stop = "filter"
			}
		} else if d.called {
			// the ChatGPT backend's completed response lists no output
			stop = "tool"
		} else {
			for _, it := range ev.Response.Output {
				if it.Type == "function_call" {
					stop = "tool"
				}
			}
		}
		emit(Event{Kind: KStop, Stop: stop})
		emit(Event{Kind: KUsage, Usage: ev.Response.Usage.Usage()})
	case "error":
		d.endCall(emit)
		msg := ev.Message
		if ev.Error != nil {
			msg = cmp.Or(ev.Error.Message, msg)
		}
		emit(Event{Kind: KError, Text: cmp.Or(msg, StreamErrorText(data)), Code: RefusedCode(data)})
	}
	return nil
}

// responsesEncoder writes events as a Responses stream. Codex reads the
// full item from output_item.done and usage from response.completed.
type ResponsesEncoder struct {
	W       *SseWriter
	Model   string
	Id      string
	created int64
	seq     int
	started bool
	item    int             // index of the open output item
	open    Kind            // kind of the open item
	itemID  string          // id of the open item
	text    strings.Builder // text of the open message / reasoning
	// a reasoning item began upstream (KThinkStart): the next thinking
	// opens an item of its own, by thinkID when the upstream named it
	thinkNew bool
	thinkID  string
	output   []any
	col      Collector
	Named    map[string]NsTool // the request's namespaced tools
	// call is the open function_call item's id and name, kept here: the
	// collector's last part isn't it once a hidden thinking block (signed
	// with no text, or redacted) has come after the call (#1786)
	call Part
}

// callTo names the tool a function_call item is to as the client knows it:
// a namespaced tool by its name and namespace, not the flat name the model
// used.
func CallTo(item map[string]any, name string, named map[string]NsTool) map[string]any {
	if q := named[name]; q.Search {
		// Codex runs its tool search itself, from the item it knows it by
		args, _ := item["arguments"].(string)
		delete(item, "name")
		item["type"], item["execution"], item["arguments"] = "tool_search_call", "client", ParseArgs(args)
	} else if q.Custom {
		// a custom tool's call carries its free-form input, not arguments
		args, _ := item["arguments"].(string)
		delete(item, "arguments")
		item["type"], item["name"], item["input"] = "custom_tool_call", q.Name, ""
		if args != "" {
			item["input"] = CustomInput(args)
		}
		if q.Namespace != "" {
			item["namespace"] = q.Namespace
		}
	} else if q, ok := named[name]; ok {
		item["name"], item["namespace"] = q.Name, q.Namespace
		// Nothing magpie serves seals arguments. Codex reads a namespaced call
		// without this list as sealed: spawn_agent's message in MultiAgentV2
		// would reach the subagent as ciphertext, which is really plain text.
		item["encrypted_function_args"] = []any{}
	} else {
		item["name"] = name
	}
	return item
}

// itemPrefix is the prefix of a call item's id, by its type: OpenAI turns
// away a tool_search_call or custom_tool_call whose id isn't its own kind
// ("Invalid 'input[98].id': 'fc_…'. Expected an ID that begins with
// 'tsc'", openaiItemPrefix), and Codex hands the item back to it when the
// conversation goes there.
func ItemPrefix(item map[string]any) string {
	if t, _ := item["type"].(string); OpenaiItemPrefix[t] != "" {
		return OpenaiItemPrefix[t]
	}
	return "fc_"
}

func (e *ResponsesEncoder) send(typ string, fields map[string]any) {
	fields["type"] = typ
	fields["sequence_number"] = e.seq
	e.seq++
	e.W.Event(typ, fields)
}

func (e *ResponsesEncoder) response(status string, extra map[string]any) map[string]any {
	// a list, never null, before any item: Muse Code refuses a stream whose
	// response.created has output null (its nil slice in an any isn't nil)
	output := e.output
	if output == nil {
		output = []any{}
	}
	out := map[string]any{"id": e.Id, "object": "response", "created_at": e.created, "status": status,
		"model": e.Model, "output": output, "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (e *ResponsesEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started, e.item = true, -1
	e.Id, e.created = ev.MsgID, time.Now().Unix()
	if e.Id == "" {
		e.Id = NewID()
	}
	if !strings.HasPrefix(e.Id, "resp_") {
		e.Id = "resp_" + e.Id
	}
	if ev.Model != "" {
		e.Model = ev.Model
	}
	e.send("response.created", map[string]any{"response": e.response("in_progress", nil)})
	e.send("response.in_progress", map[string]any{"response": e.response("in_progress", nil)})
}

func (e *ResponsesEncoder) closeItem() {
	e.closeAs(nil)
}

// closeAs closes the open item, as sealed when the upstream wrote it
// whole (a reasoning item it sealed).
func (e *ResponsesEncoder) closeAs(sealed json.RawMessage) {
	if e.open == "" {
		return
	}
	var item any
	switch e.open {
	case Text:
		t := e.text.String()
		e.send("response.output_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "text": t, "logprobs": []any{}})
		part := map[string]any{"type": "output_text", "text": t, "annotations": []any{}, "logprobs": []any{}}
		e.send("response.content_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "part": part})
		item = map[string]any{"id": e.itemID, "type": "message", "role": "assistant", "status": "completed", "content": []map[string]any{part}}
	case Thinking:
		t := e.text.String()
		e.send("response.reasoning_summary_text.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "text": t})
		part := map[string]any{"type": "summary_text", "text": t}
		e.send("response.reasoning_summary_part.done", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "part": part})
		item = map[string]any{"id": e.itemID, "type": "reasoning", "status": "completed", "summary": []map[string]any{part}}
		if sealed != nil {
			item = sealed
		}
	case ToolCall:
		args := strings.TrimSpace(e.text.String())
		if args == "" {
			args = "{}"
		}
		p := e.call
		if q := e.Named[p.Name]; !q.Search && !q.Custom {
			e.send("response.function_call_arguments.done", CallTo(map[string]any{"item_id": e.itemID, "output_index": e.item, "call_id": p.ID, "arguments": args}, p.Name, e.Named))
		}
		item = CallTo(map[string]any{"id": e.itemID, "type": "function_call", "status": "completed", "call_id": p.ID, "arguments": args}, p.Name, e.Named)
	}
	e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
	e.output = append(e.output, item)
	e.open, e.text = "", strings.Builder{}
}

func (e *ResponsesEncoder) openItem(k Kind, prefix string, item map[string]any) {
	e.closeItem()
	e.item++
	e.open, e.itemID = k, prefix+NewID()
	if k == Thinking && e.thinkID != "" {
		e.itemID = e.thinkID
	}
	e.thinkNew, e.thinkID = false, ""
	item["id"] = e.itemID
	item["status"] = "in_progress"
	e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": item})
}

func (e *ResponsesEncoder) Event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text == "" {
			return
		}
		if e.open != Text {
			e.openItem(Text, "msg_", map[string]any{"type": "message", "role": "assistant", "content": []any{}})
			e.send("response.content_part.added", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}})
		}
		e.text.WriteString(ev.Text)
		e.send("response.output_text.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "content_index": 0, "delta": ev.Text, "logprobs": []any{}})
	case KThink:
		if ev.Text == "" {
			return
		}
		if e.open != Thinking || e.thinkNew {
			e.openItem(Thinking, "rs_", map[string]any{"type": "reasoning", "summary": []any{}})
			e.send("response.reasoning_summary_part.added", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": ""}})
		}
		e.text.WriteString(ev.Text)
		e.send("response.reasoning_summary_text.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "summary_index": 0, "delta": ev.Text})
	case KThinkStart:
		e.thinkNew, e.thinkID = true, ""
		if strings.HasPrefix(ev.ID, "rs_") {
			e.thinkID = ev.ID
		}
	case KSealed:
		if ev.Name != SealResponses {
			break // another API's, which a Responses client can't read
		}
		raw := json.RawMessage(ev.Text)
		if e.open == Thinking && !e.thinkNew && (ev.ID == "" || ev.ID == e.itemID) {
			// the item streamed so far, done as the upstream wrote it
			e.closeAs(raw)
			break
		}
		// none of it streamed (no summary): added and done whole
		e.closeItem()
		e.item++
		e.thinkNew, e.thinkID = false, ""
		e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": raw})
		e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": raw})
		e.output = append(e.output, raw)
	case KSearch:
		// A search arrives whole, after it ran. Tell the client its query
		// and sources, as Anthropic's encoder does, without inventing
		// earlier in-progress events or citation offsets in the answer.
		e.closeItem()
		e.item++
		item := ResponsesSearchItem(ev.Text, ev.Hits)
		e.send("response.output_item.added", map[string]any{"output_index": e.item, "item": item})
		e.send("response.web_search_call.completed", map[string]any{"output_index": e.item, "item_id": item["id"]})
		e.send("response.output_item.done", map[string]any{"output_index": e.item, "item": item})
		e.output = append(e.output, item)
	case KToolStart:
		if ev.ID == "" {
			ev.ID = "call_" + NewID()
		}
		// Open (and so close the previous item) before recording this call:
		// closeItem reads the previous call's id and name from e.call.
		item := CallTo(map[string]any{"type": "function_call", "call_id": ev.ID, "arguments": ""}, ev.Name, e.Named)
		e.openItem(ToolCall, ItemPrefix(item), item)
		e.call = Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
		e.col.Add(ev)
		return
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.text.WriteString(ev.Text)
			// a custom tool's input is whole only once its arguments are:
			// it goes in the item when that is done
			if !e.Named[e.call.Name].Custom {
				e.send("response.function_call_arguments.delta", map[string]any{"item_id": e.itemID, "output_index": e.item, "delta": ev.Text})
			}
		}
	case KError:
		e.closeItem()
		code := "server_error"
		if ev.Code != "" {
			code = ev.Code
		}
		e.send("response.failed", map[string]any{"response": e.response("failed", map[string]any{"error": map[string]any{"code": code, "message": ev.Text}})})
	}
	e.col.Add(ev)
}

// keepalive is response.in_progress again: Codex's idle timeout counts
// events only, an SSE comment never reaching it, and skips this one.
func (e *ResponsesEncoder) Keepalive() {
	if !e.started {
		e.start(Event{}) // response.created and response.in_progress
		return
	}
	e.send("response.in_progress", map[string]any{"response": e.response("in_progress", nil)})
}

func (e *ResponsesEncoder) Finish() {
	if !e.started {
		e.start(Event{})
	}
	e.closeItem()
	res := e.col.Finish()
	status, typ := "completed", "response.completed"
	extra := map[string]any{"usage": res.Usage.Responses(), "incomplete_details": nil}
	if res.Stop == "length" || res.Stop == "filter" {
		status, typ = "incomplete", "response.incomplete"
		reason := "max_output_tokens"
		if res.Stop == "filter" {
			reason = "content_filter"
		}
		extra["incomplete_details"] = map[string]any{"reason": reason}
	}
	e.send(typ, map[string]any{"response": e.response(status, extra)})
}

// responsesSearchItem keeps a completed search in both Responses reply forms.
// Keep query for older clients and queries for clients using the current API.
func ResponsesSearchItem(query string, hits []Hit) map[string]any {
	sources := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if h.URL != "" {
			sources = append(sources, map[string]any{"type": "url", "url": h.URL})
		}
	}
	return map[string]any{
		"id": "ws_" + NewID(), "type": "web_search_call", "status": "completed",
		"action": map[string]any{"type": "search", "query": query, "queries": []string{query}, "sources": sources},
	}
}

// renderResponses is the non-streaming reply.
func RenderResponses(res Result, model string, named map[string]NsTool) []byte {
	output := []any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text == "" && p.Signature != "" {
				continue // only Gemini's signature, which is Gemini's
			}
			output = append(output, map[string]any{"id": "msg_" + NewID(), "type": "message", "role": "assistant", "status": "completed",
				"content": []map[string]any{{"type": "output_text", "text": p.Text, "annotations": []any{}}}})
		case Thinking:
			if p.sealedBy(SealResponses) {
				// as the upstream wrote it: id, summary, encrypted_content
				output = append(output, p.Sealed)
				continue
			}
			if p.Text == "" {
				continue // another API's sealed thinking, or only its signature
			}
			output = append(output, map[string]any{"id": "rs_" + NewID(), "type": "reasoning", "status": "completed",
				"summary": []map[string]any{{"type": "summary_text", "text": p.Text}}})
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + NewID()
			}
			item := CallTo(map[string]any{"type": "function_call", "status": "completed",
				"call_id": id, "arguments": ArgsString(p)}, p.Name, named)
			item["id"] = ItemPrefix(item) + NewID()
			output = append(output, item)
		case Search:
			output = append(output, ResponsesSearchItem(p.Text, p.Hits))
		}
	}
	id := res.ID
	if id == "" {
		id = NewID()
	}
	if !strings.HasPrefix(id, "resp_") {
		id = "resp_" + id
	}
	if res.Model != "" {
		model = res.Model
	}
	status := "completed"
	var incomplete any
	switch res.Stop {
	case "length":
		status, incomplete = "incomplete", map[string]any{"reason": "max_output_tokens"}
	case "filter":
		// as the stream says it (#248)
		status, incomplete = "incomplete", map[string]any{"reason": "content_filter"}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status,
		"model": model, "output": output, "usage": res.Usage.Responses(), "incomplete_details": incomplete,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}, "error": nil})
	return b
}
