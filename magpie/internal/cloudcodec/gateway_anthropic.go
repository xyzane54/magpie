package cloudcodec

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type ABlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
	} `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// a prompt-cache breakpoint: the prompt up to here is cached
	CacheControl map[string]string `json:"cache_control,omitempty"`
	// sealed: a redacted_thinking block as Anthropic wrote it, which goes
	// back as it came (Part.Sealed, #1445)
	sealed json.RawMessage
}

// MarshalJSON writes a thinking block's text even when it is empty.
// Messages requires the field: a signed block with no text (Claude Code's
// thinking when its display is omitted) sent without it is refused,
// "messages.N.content.0.thinking.thinking: Field required" (#1447). Every
// other block keeps its omitempty fields.
func (b ABlock) MarshalJSON() ([]byte, error) {
	type plain ABlock
	if len(b.sealed) > 0 {
		return b.sealed, nil
	}
	if b.Type != "thinking" {
		return json.Marshal(plain(b))
	}
	return json.Marshal(struct {
		plain
		Thinking string `json:"thinking"`
	}{plain(b), b.Thinking})
}

// ephemeral marks a prompt-cache breakpoint.
var Ephemeral = map[string]string{"type": "ephemeral"}

type ARequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system,omitempty"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type        string          `json:"type,omitempty"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
	} `json:"tools,omitempty"`
	ToolChoice *struct {
		Type                   string `json:"type"`
		Name                   string `json:"name,omitempty"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
	} `json:"tool_choice,omitempty"`
	MaxTokens     int      `json:"max_tokens"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
	Stream        bool     `json:"stream,omitempty"`
	Thinking      *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens,omitempty"`
	} `json:"thinking,omitempty"`
	OutputConfig *struct {
		Effort string `json:"effort,omitempty"`
		Format *struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format,omitempty"`
	} `json:"output_config,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Speed      string          `json:"speed,omitempty"` // "fast": Claude's fast mode
	Safeguards json.RawMessage `json:"safeguards,omitempty"`
}

func ParseAnthropic(body []byte) (*Request, error) {
	var a ARequest
	if err := DecodeJSON(body, &a); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: a.Model, System: StringOrText(a.System), MaxTokens: a.MaxTokens,
		Temp: a.Temperature, TopP: a.TopP, Stop: a.StopSequences, Stream: a.Stream, Fast: a.Speed == "fast"}
	r.Safeguards = a.Safeguards
	if string(r.Safeguards) == "null" {
		r.Safeguards = nil
	}
	if len(a.Metadata) > 0 && string(a.Metadata) != "null" {
		r.Metadata = a.Metadata
	}
	if oc := a.OutputConfig; oc != nil && oc.Format != nil && oc.Format.Type == "json_schema" && len(oc.Format.Schema) > 0 {
		r.Format = &Format{Type: "json_schema", Name: FormatName, Schema: oc.Format.Schema}
	}
	for _, m := range a.Messages {
		msg := Message{Role: m.Role}
		var s string
		if DecodeJSON(m.Content, &s) == nil {
			if s != "" {
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: s})
			}
		} else {
			var blocks []ABlock
			if err := DecodeJSON(m.Content, &blocks); err != nil {
				return nil, fmt.Errorf("invalid message content: %v", err)
			}
			var raws []json.RawMessage // each block as the client sent it
			for i, b := range blocks {
				switch b.Type {
				case "text":
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: b.Text})
				case "image":
					if b.Source != nil {
						msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL})
					}
				case "tool_use":
					msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: AnthropicInID(b.ID), Name: b.Name, Args: b.Input})
				case "tool_result":
					out, images := ToolOutput(b.Content)
					msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: AnthropicInID(b.ToolUseID), Text: out, Images: images, IsError: b.IsError})
				case "thinking":
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: b.Thinking, Signature: b.Signature})
				case "redacted_thinking":
					// Claude's reasoning its safety systems sealed: it
					// goes back to Messages whole, and is nothing to
					// another API (#1445)
					if raws == nil {
						_ = DecodeJSON(m.Content, &raws)
					}
					if i < len(raws) {
						msg.Parts = append(msg.Parts, Part{Kind: Thinking, Sealed: raws[i], SealedBy: SealAnthropic})
					}
				}
			}
		}
		r.Messages = append(r.Messages, msg)
	}
	for _, t := range a.Tools {
		if t.Type != "" && t.Type != "custom" && len(t.InputSchema) == 0 {
			// server-side tools mean nothing elsewhere; web search is
			// noted for those that can search by themselves
			if strings.HasPrefix(t.Type, "web_search") {
				r.WebSearch = true
			}
			continue
		}
		if t.Name == "DeferredToolPlaceholder" {
			// only keeps Anthropic's deferred loading on; never to be called
			continue
		}
		r.Tools = append(r.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
	}
	if tc := a.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto", "none":
			r.ToolChoice = tc.Type
		case "any":
			r.ToolChoice = "required"
		case "tool":
			r.ToolChoice = "name:" + tc.Name
		}
		if tc.DisableParallelToolUse {
			f := false
			r.Parallel = &f
		}
	}
	// output_config's effort sets how hard the model thinks only when it
	// was asked to think: Claude Code's title requests carry effort but no
	// thinking, and reasoning_effort would turn it on upstream
	// between_tools is Sonnet 5.5's thinking off (#1454)
	if th := a.Thinking; th != nil && (th.Type == "disabled" || th.Type == "between_tools") {
		r.ThinkOff = true
	} else if th != nil && (th.Type == "enabled" || th.Type == "adaptive") {
		r.Thinking = true
		r.Effort = EffortOfBudget(th.BudgetTokens)
		if oc := a.OutputConfig; oc != nil {
			if e := EffortOf(oc.Effort); e != "" {
				r.Effort = e
			}
		}
	}
	return r, nil
}

// anthropicReplaysThinking is whether model, served by host's Messages
// API, wants a thinking block on every assistant turn that calls a tool:
// DeepSeek's Anthropic endpoint, thinking (its default, and when asked),
// refuses a tool_use turn with none, "The `content[].thinking` in the
// thinking mode must be passed back to the API", and turns
// redacted_thinking away as a block it doesn't know (422), checked against
// api.deepseek.com/anthropic on 2026-10-11 (#1611). It takes Claude's
// signed thinking, its own, one with no signature and a space.
func AnthropicReplaysThinking(host, model string) bool {
	if host == "api.anthropic.com" || AnthropicModel.MatchString(model) {
		return false
	}
	return strings.Contains(host, "deepseek") || strings.Contains(strings.ToLower(model), "deepseek")
}

// withThinkingOnToolTurns gives each assistant message of a Messages
// request that has a tool_use, or that answers tool results alone (DeepSeek
// refuses that one too, checked 2026-10-12, #1822), and no thinking block
// one holding noReasoning, and turns a redacted_thinking block (Claude's, sealed, which
// DeepSeek can't read) into that too, or leaves it out when the message
// has thinking already, for anthropicReplaysThinking. A Claude Code routing
// group that falls from Claude to DeepSeek sends both (#1611). Every other
// message, and a body with nothing to change, goes byte for byte.
func WithThinkingOnToolTurns(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_use"`)) && !bytes.Contains(body, []byte(`"redacted_thinking"`)) {
		return body
	}
	var q map[string]json.RawMessage
	var msgs []json.RawMessage
	if DecodeJSON(body, &q) != nil || DecodeJSON(q["messages"], &msgs) != nil {
		return body
	}
	space, _ := json.Marshal(ABlock{Type: "thinking", Thinking: NoReasoning})
	changed := false
	// afterTool: the message before is the user's tool results alone
	afterTool := false
	for i, raw := range msgs {
		var m map[string]json.RawMessage
		var role string
		var blocks []json.RawMessage
		if DecodeJSON(raw, &m) != nil || DecodeJSON(m["role"], &role) != nil {
			afterTool = false
			continue
		}
		if role != "assistant" {
			afterTool = DecodeJSON(m["content"], &blocks) == nil && len(blocks) > 0 &&
				!slices.ContainsFunc(blocks, func(b json.RawMessage) bool {
					var t struct {
						Type string `json:"type"`
					}
					return DecodeJSON(b, &t) != nil || t.Type != "tool_result"
				})
			continue
		}
		after := afterTool
		afterTool = false
		if DecodeJSON(m["content"], &blocks) != nil {
			continue
		}
		var out []json.RawMessage
		thought, called, edited := false, false, false
		for _, b := range blocks {
			var t struct {
				Type string `json:"type"`
			}
			DecodeJSON(b, &t)
			switch t.Type {
			case "thinking":
				thought = true
			case "tool_use":
				called = true
			case "redacted_thinking":
				edited = true
				if thought {
					continue
				}
				b, thought = space, true
			}
			out = append(out, b)
		}
		if (called || after) && !thought {
			out = append([]json.RawMessage{space}, out...)
			edited = true
		}
		if !edited {
			continue
		}
		m["content"], _ = json.Marshal(out)
		if nb, err := json.Marshal(m); err == nil {
			msgs[i], changed = nb, true
		}
	}
	if !changed {
		return body
	}
	q["messages"], _ = json.Marshal(msgs)
	nb, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return nb
}

// anthropicModel is one of Anthropic's own models, which think only when asked,
// so a request for one is left as the agent sent it.
var AnthropicModel = regexp.MustCompile(`(?i)(?:^|[/.:-])claude-`)

// claudeVersion finds the family's version in a Claude model id however a
// relay spells it: claude-opus-4-6, claude-opus-5, anthropic.claude-sonnet-4.6-v1,
// a relay's opus-5.5, claude-fable-5-1, or the old order, claude-3-7-sonnet.
var ClaudeVersion = regexp.MustCompile(`(?:^|[^a-z0-9])(?:claude-)?(?:opus|sonnet|haiku|fable|mythos)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)|claude-(\d+)(?:[-.](\d))?-(?:opus|sonnet|haiku)`)

// adaptiveOnly is a Claude model from 4.6 on, which thinks adaptively:
// claude-opus-5-5 refuses thinking.type=enabled with a budget ("requires
// adaptive thinking"), as do Fable and Mythos 5 and 5.1, whose adaptive
// thinking is always on, so how hard it thinks goes in output_config.effort.
func AdaptiveOnly(model string) bool {
	major, minor, ok := ClaudeVersionOf(model)
	return ok && (major > 4 || major == 4 && minor >= 6)
}

// noXhigh is a Claude that thinks only adaptively but has no xhigh effort,
// Opus and Sonnet 4.6, so it is asked max in its place. Every later one
// takes xhigh (platform.claude.com/docs/en/build-with-claude/effort), and
// max there spends without limit.
func NoXhigh(model string) bool {
	major, minor, ok := ClaudeVersionOf(model)
	return ok && major == 4 && minor == 6
}

// claudeVersionOf is a Claude model's version as claudeVersion reads it;
// ok is false for a model it doesn't read as a Claude.
func ClaudeVersionOf(model string) (major, minor int, ok bool) {
	m := ClaudeVersion.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return 0, 0, false
	}
	v := m[1:3]
	if m[3] != "" {
		v = m[3:5]
	}
	major, _ = strconv.Atoi(v[0])
	minor, _ = strconv.Atoi(v[1])
	return major, minor, true
}

// effortInOutputConfig is a model that takes how hard it thinks from
// output_config.effort, beside thinking turned on, as ZCode asks it: Z.ai's
// GLM-5.2 and GLM-5.3 on their Anthropic endpoints. A budget alone leaves
// them at their own default.
var EffortInOutputConfig = regexp.MustCompile(`(?i)glm-5\.[23](?:$|[-.:/\[])`)

// imageBlock is an image as an Anthropic block: inline, or by its URL.
func ImageBlock(p Part) ABlock {
	b := ABlock{Type: "image"}
	b.Source = &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
		URL       string `json:"url,omitempty"`
	}{}
	if p.URL != "" && p.Data == "" {
		b.Source.Type, b.Source.URL = "url", p.URL
	} else {
		b.Source.Type, b.Source.MediaType, b.Source.Data = "base64", p.MediaType, p.Data
	}
	return b
}

// aMessage is one message of a request built for a Messages upstream.
type AMessage struct {
	Role    string   `json:"role"`
	Content []ABlock `json:"content"`
}

// interruptedTool is what an unanswered call gets, as pairToolMessages and
// pairToolItems make for the Chat and Responses paths.
const InterruptedTool = "[The result of this tool call is unavailable: the turn was interrupted.]"

// anthropicBody is buildAnthropic; with replay, for an upstream that wants
// a turn's thinking back (anthropicReplaysThinking), thinking with no
// signature goes as its text instead of being left out.
func AnthropicBody(r *Request, model string, replay bool) []byte {
	if r.Format != nil && r.Format.SchemaValue() == nil {
		// output_config.format takes a schema only: any JSON object is
		// asked for in words
		r = r.InSystem()
	}
	var msgs []AMessage
	push := func(role string, blocks []ABlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, AMessage{Role: role, Content: blocks})
	}
	for _, m := range r.Messages {
		var results, rest []ABlock
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if strings.TrimSpace(p.Text) != "" {
					rest = append(rest, ABlock{Type: "text", Text: p.Text})
				}
			case File:
				rest = append(rest, ABlock{Type: "text", Text: AttachmentText(p)})
			case Image:
				rest = append(rest, ImageBlock(p))
			case ToolCall:
				// Anthropic takes a tool_use id only of [A-Za-z0-9_-]:
				// another vendor's call (Devin's "Bash:0#…") goes as
				// anthropicOutID makes it, its result named the same
				rest = append(rest, ABlock{Type: "tool_use", ID: AnthropicOutID(p.ID), Name: p.Name, Input: ArgsOf(p)})
			case ToolResult:
				c, _ := json.Marshal(p.Text)
				if len(p.Images) > 0 {
					// a tool_result holds images beside its text
					var blocks []ABlock
					if strings.TrimSpace(p.Text) != "" {
						blocks = append(blocks, ABlock{Type: "text", Text: p.Text})
					}
					for _, im := range p.Images {
						blocks = append(blocks, ImageBlock(im))
					}
					c, _ = json.Marshal(blocks)
				}
				results = append(results, ABlock{Type: "tool_result", ToolUseID: AnthropicOutID(p.CallID), Content: c, IsError: p.IsError})
			case Thinking:
				if p.sealedBy(SealAnthropic) {
					rest = append(rest, ABlock{Type: "redacted_thinking", sealed: p.Sealed})
				} else if p.Signature != "" {
					rest = append(rest, ABlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature})
				} else if replay && p.Text != "" {
					rest = append(rest, ABlock{Type: "thinking", Thinking: p.Text})
				}
			}
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		push(role, append(results, rest...))
	}
	// a conversation a client left with its exchange broken — a call whose
	// turn was interrupted, a result whose call it dropped — is mended, as
	// the Chat and Responses paths mend theirs, where Anthropic refuses it
	msgs = PairToolBlocks(msgs)
	// Anthropic caches a prompt only up to a block marked for it, which a
	// request from another API (Codex's, a Chat client's) never has: the
	// conversation so far is marked at its last block, so the next turn,
	// which only adds to it, reads it from the cache; thinking can't be
	// marked. Two marks at most, with the one Claude's sign-in adds kept
	// within Anthropic's four.
	if n := len(msgs); n > 0 {
		c := msgs[n-1].Content
		for i := len(c) - 1; i >= 0; i-- {
			if c[i].Type != "thinking" && c[i].Type != "redacted_thinking" {
				c[i].CacheControl = Ephemeral
				break
			}
		}
	}
	// an assistant turn made only of unsigned thinking is nothing to Anthropic
	out := map[string]any{"model": model, "messages": msgs, "stream": r.Stream}
	if r.System != "" {
		// the tools and system prompt, the same every turn, are cached
		// apart from the conversation
		out["system"] = []ABlock{{Type: "text", Text: r.System, CacheControl: Ephemeral}}
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens // the model's own is filled in before (askedOutput)
	}
	// a client that turned reasoning off — Codex's or a Chat client's
	// effort none, which Effort reads as low — is sent thinking off, not a
	// low budget: it was thinking at 4096 tokens while the log said none
	// (#1636). A model that won't stop thinking is asked again as before
	// (forwardTranslated); thinkingOffAsTaken fits off to each Claude
	think := (r.Thinking || r.Effort != "") && !r.ThinkOff
	if r.ThinkOff {
		out["thinking"] = map[string]any{"type": "disabled"}
	}
	if think && AdaptiveOnly(model) {
		// Opus 4.7 and later leave the thinking text out unless asked
		// (display "omitted" by default), and a Chat or Responses client
		// that asked to reason would see none of it; OpenAI is asked for
		// its summary and the Claude subscription for summarized alike
		out["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		if e := r.Effort; e != "" {
			if e == "xhigh" && NoXhigh(model) {
				e = "max"
			}
			out["output_config"] = map[string]any{"effort": e}
		}
	} else if budget := BudgetOf(r.Effort); think && (r.MaxTokens <= 0 || r.MaxTokens > 2048) {
		if r.MaxTokens > 0 {
			// the client's cap is what the model can write (Pi sends the
			// model's own output limit): the budget fits under it, leaving
			// room for the answer, as a raised max_tokens past what the
			// model takes is refused with a 400
			budget = min(budget, maxTokens-1024)
		} else if maxTokens < budget+4096 {
			maxTokens = budget + 4096
		}
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		if r.Effort != "" && EffortInOutputConfig.MatchString(model) {
			out["output_config"] = map[string]any{"effort": r.Effort}
		}
	} else if r.Temp != nil {
		out["temperature"] = *r.Temp
	} else if r.TopP != nil {
		out["top_p"] = *r.TopP
	}
	if s := r.Format.SchemaValue(); len(s) > 0 {
		oc, _ := out["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		oc["format"] = map[string]any{"type": "json_schema", "schema": s}
		out["output_config"] = oc
	}
	out["max_tokens"] = maxTokens
	if len(r.Stop) > 0 {
		out["stop_sequences"] = r.Stop
	}
	if len(r.Metadata) > 0 {
		out["metadata"] = r.Metadata
	}
	if len(r.Safeguards) > 0 && AnthropicModel.MatchString(model) {
		// auto mode's review, which Claude alone does (automode.go)
		out["safeguards"] = r.Safeguards
	}
	if len(r.Tools) > 0 || r.WebSearch {
		var tools []map[string]any
		for _, t := range r.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			} else {
				schema = ObjectSchema(schema)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		if r.System == "" && len(tools) > 0 {
			tools[len(tools)-1]["cache_control"] = Ephemeral
		}
		if r.WebSearch {
			tools = append(tools, map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5})
		}
		out["tools"] = tools
		var tc map[string]any
		switch {
		case r.ToolChoice == "auto" || r.ToolChoice == "":
			if r.Parallel != nil && !*r.Parallel {
				tc = map[string]any{"type": "auto"}
			}
		case r.ToolChoice == "required":
			tc = map[string]any{"type": "any"}
		case r.ToolChoice == "none":
			tc = map[string]any{"type": "none"}
		case strings.HasPrefix(r.ToolChoice, "name:"):
			tc = map[string]any{"type": "tool", "name": strings.TrimPrefix(r.ToolChoice, "name:")}
		}
		if tc != nil {
			if r.Parallel != nil && !*r.Parallel && tc["type"] != "none" {
				tc["disable_parallel_tool_use"] = true
			}
			out["tool_choice"] = tc
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// pairToolBlocks mends the tool exchange of a request built for a Messages
// upstream, as pairToolMessages does for a Chat one (#554) and
// pairToolItems for a relayed Responses request (#1341): Anthropic refuses
// a tool_use the next message's tool_results don't answer ("`tool_use` ids
// were found without `tool_result` blocks immediately after") and a
// tool_result naming a call it never saw ("unexpected `tool_use_id` found
// in `tool_result` blocks") — an interrupted turn or a client that dropped
// its calls leaves both behind, and a relay serving Claude over Messages
// passes the refusal on. A call left unanswered gets a synthetic result in
// the user message that follows, or one of its own where none does; an
// answer to no call becomes a text block, keeping what it said. A
// well-formed exchange is built exactly as it was.
func PairToolBlocks(msgs []AMessage) []AMessage {
	out := make([]AMessage, 0, len(msgs))
	var pending []string          // the calls of the assistant message in hand
	open := map[string]bool{}     // which of them stand
	answered := map[string]bool{} // pending calls a result answered

	// synthetic is a call's missing result
	synthetic := func(id string) ABlock {
		c, _ := json.Marshal(InterruptedTool)
		return ABlock{Type: "tool_result", ToolUseID: id, Content: c, IsError: true}
	}
	// closeExchange answers the calls still open and ends the exchange
	closeExchange := func() {
		var blocks []ABlock
		for _, id := range pending {
			if !answered[id] {
				blocks = append(blocks, synthetic(id))
			}
		}
		if len(blocks) > 0 {
			out = append(out, AMessage{Role: "user", Content: blocks})
		}
		pending, open, answered = nil, map[string]bool{}, map[string]bool{}
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			closeExchange()
			for _, b := range m.Content {
				if b.Type == "tool_use" {
					pending = append(pending, b.ID)
					open[b.ID] = true
				}
			}
			out = append(out, m)
			continue
		}
		var kept, rest []ABlock
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				rest = append(rest, b)
				continue
			}
			if !open[b.ToolUseID] {
				// answers no call of the exchange in hand — one another
				// request carried, that closed already, or never there
				rest = append(rest, ResultSaid(b)...)
				continue
			}
			if answered[b.ToolUseID] {
				continue // a second answer: the first stands
			}
			answered[b.ToolUseID] = true
			kept = append(kept, b)
		}
		for _, id := range pending {
			if !answered[id] {
				kept = append(kept, synthetic(id))
			}
		}
		out = append(out, AMessage{Role: "user", Content: append(kept, rest...)})
		pending, open, answered = nil, map[string]bool{}, map[string]bool{}
	}
	closeExchange()
	return out
}

// resultSaid is what a tool_result that answers no call keeps: its text as
// a text block, its images beside it as they came.
func ResultSaid(b ABlock) []ABlock {
	var s string
	if len(b.Content) > 0 && json.Unmarshal(b.Content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			s = "Tool result received."
		}
		return []ABlock{{Type: "text", Text: s}}
	}
	var blocks []ABlock
	if len(b.Content) > 0 && json.Unmarshal(b.Content, &blocks) == nil && len(blocks) > 0 {
		return blocks
	}
	return []ABlock{{Type: "text", Text: "Tool result received."}}
}

// anthropicDecoder leaves out the blocks of Anthropic's own server tools —
// a web search it ran — whose input is no call of the client's. A
// tool_use block whose start carries its whole input, as a relay in front
// of another vendor's model may send it with no input_json_delta after
// (蓝猫 on Discord), keeps that input; Anthropic's own start has an empty
// one, its deltas after.
type AnthropicDecoder struct {
	Server map[int]bool
	Whole  map[int]bool // the tool_use blocks whose start carried their input
}

func (d *AnthropicDecoder) Decode(data string, emit func(Event)) error {
	var ev struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
		} `json:"delta"`
	}
	if DecodeJSON([]byte(data), &ev) == nil {
		switch ev.Type {
		case "content_block_start":
			d.Server[ev.Index] = ev.ContentBlock.Type == "server_tool_use"
			d.Whole[ev.Index] = false
			if ev.ContentBlock.Type == "tool_use" && FullInput(ev.ContentBlock.Input) {
				if err := DecodeAnthropic(data, emit); err != nil {
					return err
				}
				d.Whole[ev.Index] = true
				emit(Event{Kind: KToolArgs, Text: string(ev.ContentBlock.Input)})
				return nil
			}
		case "content_block_delta", "content_block_stop":
			if d.Server[ev.Index] {
				return nil
			}
			if d.Whole[ev.Index] && ev.Delta.Type == "input_json_delta" {
				return nil // the input came whole already
			}
		}
	}
	return DecodeAnthropic(data, emit)
}

// fullInput says whether a tool_use start's input is the call's own: an
// object with something in it.
func FullInput(in json.RawMessage) bool {
	var m map[string]any
	return DecodeJSON(in, &m) == nil && len(m) > 0
}

// decodeAnthropic turns an Anthropic event stream into events.
func DecodeAnthropic(data string, emit func(Event)) error {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage AUsage `json:"usage"`
		} `json:"message"`
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
			Text string `json:"text"`
			// thinking
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			StopReason  string `json:"stop_reason"`
			// auto mode's review of the reply's calls (automode.go)
			SafeguardResults json.RawMessage `json:"safeguard_results"`
			StopDetails      json.RawMessage `json:"stop_details"`
		} `json:"delta"`
		Usage AUsage `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := DecodeJSON([]byte(data), &ev); err != nil {
		// keep going: pings and unknown shapes are harmless, but an
		// error in a shape of its own is still the reply's end (#1933)
		if e, ok := CarriedError(data); ok {
			emit(e)
		}
		return nil
	}
	switch ev.Type {
	case "message_start":
		emit(Event{Kind: KStart, MsgID: ev.Message.ID, Model: ev.Message.Model, Usage: ev.Message.Usage.Usage()})
	case "content_block_start":
		switch ev.ContentBlock.Type {
		case "tool_use":
			emit(Event{Kind: KToolStart, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name})
		case "text":
			if ev.ContentBlock.Text != "" {
				emit(Event{Kind: KText, Text: ev.ContentBlock.Text})
			}
		case "thinking":
			// a block of its own, even signed with no text in it, or
			// right after another (#1445)
			emit(Event{Kind: KThinkStart})
			if ev.ContentBlock.Thinking != "" {
				emit(Event{Kind: KThink, Text: ev.ContentBlock.Thinking})
			}
			if ev.ContentBlock.Signature != "" {
				emit(Event{Kind: KSig, Text: ev.ContentBlock.Signature})
			}
		case "redacted_thinking":
			// sealed whole: it goes on as it came, to Messages only
			var whole struct {
				Block json.RawMessage `json:"content_block"`
			}
			if DecodeJSON([]byte(data), &whole) == nil && len(whole.Block) > 0 {
				emit(Event{Kind: KSealed, Name: SealAnthropic, Text: string(whole.Block)})
			}
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			emit(Event{Kind: KText, Text: ev.Delta.Text})
		case "input_json_delta":
			emit(Event{Kind: KToolArgs, Text: ev.Delta.PartialJSON})
		case "thinking_delta":
			emit(Event{Kind: KThink, Text: ev.Delta.Thinking})
		case "signature_delta":
			emit(Event{Kind: KSig, Text: ev.Delta.Signature})
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			emit(Event{Kind: KStop, Stop: StopFromAnthropic(ev.Delta.StopReason)})
		}
		var review json.RawMessage
		if r := ev.Delta.SafeguardResults; len(r) > 0 && string(r) != "null" {
			review = r
		}
		emit(Event{Kind: KUsage, Usage: ev.Usage.Usage(), SafeguardResults: review, StopDetails: StopDetails(ev.Delta.StopDetails)})
	case "error":
		emit(Event{Kind: KError, Text: cmp.Or(ev.Error.Message, ev.Error.Type, StreamErrorText(data)), Code: RefusedCode(data)})
	}
	return nil
}

type AUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// CacheCreation splits the cache writes by how long they are kept,
	// which Anthropic bills apart: 1.25× input for 5 minutes, 2× for an
	// hour
	CacheCreation *ACacheCreation `json:"cache_creation,omitempty"`
}

type ACacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

func (u AUsage) Usage() Usage {
	out := Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
	if c := u.CacheCreation; c != nil {
		out.CacheWrite = max(out.CacheWrite, c.Ephemeral5m+c.Ephemeral1h)
		out.CacheWrite1h = c.Ephemeral1h
	}
	return out
}

// anthropic is the usage as Anthropic says it, the cache writes split by
// how long they are kept where some were for an hour.
func (u Usage) Anthropic() AUsage {
	out := AUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.CacheRead, CacheCreationInputTokens: u.CacheWrite}
	if h := min(u.CacheWrite1h, u.CacheWrite); h > 0 {
		out.CacheCreation = &ACacheCreation{Ephemeral5m: u.CacheWrite - h, Ephemeral1h: h}
	}
	return out
}

func StopFromAnthropic(s string) string {
	switch s {
	case "max_tokens", "model_context_window_exceeded":
		// the second is Claude 4.5+ running into its context window
		// before max_tokens: the reply is cut short all the same
		return "length"
	case "tool_use",
		// an Anthropic-shaped relay in front of another vendor's model,
		// passing on OpenAI's reasons as they came (蓝猫 on Discord)
		"tool_calls", "function_call":
		return "tool"
	case "length":
		return "length"
	case "refusal", "content_filter":
		return "filter"
	}
	return "stop"
}

func StopToAnthropic(s string) string {
	switch s {
	case "length":
		return "max_tokens"
	case "tool":
		return "tool_use"
	case "filter":
		return "refusal"
	}
	return "end_turn"
}

// anthropicEncoder writes events as an Anthropic event stream.
type AnthropicEncoder struct {
	Id    string
	W     *SseWriter
	Model string
	index int
	open  Kind // kind of the open content block, "" when none
	args  bool // the open tool block got arguments
	// fresh: a thinking block began upstream (KThinkStart), to be opened
	// apart from the one open with what it says first
	fresh   bool
	started bool
	col     Collector
}

// anthropicID is a reply's id as Anthropic's API gives one, msg_…: an
// OpenAI-shaped upstream's chatcmpl-… (a plugin's provider, a relay) reads
// as a built-in's does.
func AnthropicID(id string) string {
	if id == "" {
		return "msg_" + NewID()
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + strings.TrimPrefix(id, "chatcmpl-")
}

// Anthropic's Messages API, and Claude Code with it, takes a tool_use id
// only of [A-Za-z0-9_-]: Claude Code drops a call whose id has another
// character, and the turn fails "could not be parsed" (#1304). Other
// vendors' ids can have one (Devin's "Bash:0#a65b…", a plugin's or a
// relay's), so a call's id goes to an Anthropic client as anthropicOutID
// makes it, and an id that comes back in the client's request is turned
// back by anthropicInID, so the upstream that made the call gets its own
// id on the next turn. An id that is already
// safe goes both ways byte for byte, and Devin's "dv_…" ids, safe already,
// are not encoded twice.
const AnthropicIDMark = "mp_"

func AnthropicOutID(id string) string {
	if id == "" {
		return id
	}
	return SafeCallID(AnthropicIDMark, id)
}

func AnthropicInID(id string) string { return RawCallID(AnthropicIDMark, id) }

// safeCallID is id as one of [A-Za-z0-9_-] only: an id with another
// character, or one beginning with mark, is mark and its base64url, which
// rawCallID turns back; any other id is itself. It keeps no state, so an
// id comes back the same from any request, after any restart.
func SafeCallID(mark, id string) string {
	if SafeIDChars.MatchString(id) && !strings.HasPrefix(id, mark) {
		return id
	}
	return mark + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// rawCallID is the id safeCallID was given for id, or id itself when
// safeCallID didn't make it.
func RawCallID(mark, id string) string {
	if !strings.HasPrefix(id, mark) || !SafeIDChars.MatchString(id) {
		return id
	}
	raw, err := base64.RawURLEncoding.DecodeString(id[len(mark):])
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) || SafeCallID(mark, string(raw)) != id {
		return id
	}
	return string(raw)
}

var SafeIDChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (e *AnthropicEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	id := AnthropicID(ev.MsgID)
	e.Id = id
	model := ev.Model
	if model == "" {
		model = e.Model
	}
	e.W.Event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": ev.Usage.Anthropic()}})
}

func (e *AnthropicEncoder) close() {
	if e.open == "" {
		return
	}
	if e.open == ToolCall && !e.args {
		e.W.Event("content_block_delta", map[string]any{"type": "content_block_delta", "index": e.index,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
	}
	e.W.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
	e.index++
	e.open = ""
}

func (e *AnthropicEncoder) openBlock(k Kind, block map[string]any) {
	if e.open == k && k != ToolCall {
		return
	}
	e.close()
	e.args, e.fresh = false, false
	block["type"] = map[Kind]string{Text: "text", Thinking: "thinking", ToolCall: "tool_use"}[k]
	e.W.Event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index, "content_block": block})
	e.open = k
}

// thinking makes the open block a thinking block: the one open, unless a
// new one has begun since (KThinkStart).
func (e *AnthropicEncoder) thinking() {
	if e.fresh {
		e.close()
		e.fresh = false
	}
	e.openBlock(Thinking, map[string]any{"thinking": ""})
}

func (e *AnthropicEncoder) delta(d map[string]any) {
	e.W.Event("content_block_delta", map[string]any{"type": "content_block_delta", "index": e.index, "delta": d})
}

func (e *AnthropicEncoder) Event(ev Event) {
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
		e.openBlock(Text, map[string]any{"text": ""})
		e.delta(map[string]any{"type": "text_delta", "text": ev.Text})
	case KThinkStart:
		// opened with what it says first: one that says nothing isn't
		e.fresh = true
		e.col.Add(ev)
		return
	case KThink:
		if ev.Text == "" {
			return
		}
		e.thinking()
		e.delta(map[string]any{"type": "thinking_delta", "thinking": ev.Text})
	case KSig:
		// a block signed with no text goes as it came, signed (#1445)
		if e.open == Thinking || e.fresh {
			e.thinking()
			e.delta(map[string]any{"type": "signature_delta", "signature": ev.Text})
		}
	case KSealed:
		if ev.Name == SealAnthropic {
			// a redacted_thinking block, whole as Anthropic wrote it
			e.close()
			e.fresh = false
			e.W.Event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index, "content_block": json.RawMessage(ev.Text)})
			e.W.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
			e.index++
		}
	case KToolStart:
		id := ev.ID
		if id == "" {
			id = "toolu_" + NewID()
		}
		e.openBlock(ToolCall, map[string]any{"id": AnthropicOutID(id), "name": ev.Name, "input": map[string]any{}})
	case KToolArgs:
		if e.open == ToolCall && ev.Text != "" {
			e.args = true
			e.delta(map[string]any{"type": "input_json_delta", "partial_json": ev.Text})
		}
	case KSearch:
		// as Anthropic's own web search tells it, which Claude Code's
		// WebSearch counts and takes its links from
		e.close()
		id := "srvtoolu_" + NewID()
		query, _ := json.Marshal(map[string]string{"query": ev.Text})
		e.W.Event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index,
			"content_block": map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{}}})
		e.delta(map[string]any{"type": "input_json_delta", "partial_json": string(query)})
		e.W.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
		e.index++
		e.W.Event("content_block_start", map[string]any{"type": "content_block_start", "index": e.index,
			"content_block": SearchResultBlock(id, ev.Hits)})
		e.W.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": e.index})
		e.index++
	case KError:
		e.close()
		failed := map[string]any{"type": "api_error", "message": ev.Text}
		if ev.Code != "" {
			failed["code"] = ev.Code // preserve the upstream error type, including refusals
		}
		e.W.Event("error", map[string]any{"type": "error", "error": failed})
	}
	e.col.Add(ev)
}

// keepalive is Anthropic's own: a ping event, which its SDKs skip and
// Claude Code's stream watchdog counts.
func (e *AnthropicEncoder) Keepalive() {
	e.W.Event("ping", map[string]any{"type": "ping"})
}

func (e *AnthropicEncoder) Finish() {
	if !e.started {
		e.start(Event{})
	}
	e.close()
	res := e.col.Finish()
	delta := map[string]any{"stop_reason": StopToAnthropic(res.Stop), "stop_sequence": nil}
	if len(res.SafeguardResults) > 0 {
		delta["safeguard_results"] = res.SafeguardResults
	}
	if len(res.StopDetails) > 0 {
		delta["stop_details"] = res.StopDetails
	}
	e.W.Event("message_delta", map[string]any{"type": "message_delta",
		"delta": delta,
		"usage": res.Usage.Anthropic()})
	e.W.Event("message_stop", map[string]any{"type": "message_stop"})
}

// renderAnthropic is the non-streaming reply.
func RenderAnthropic(res Result, model string) []byte {
	content := []any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text == "" && p.Signature != "" {
				continue // only Gemini's signature, which is Gemini's
			}
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		case Thinking:
			switch {
			case p.sealedBy(SealAnthropic):
				content = append(content, p.Sealed)
			case p.Text == "" && p.Signature == "":
				// another API's sealed reasoning, nothing to Messages
			default:
				content = append(content, map[string]any{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
			}
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "toolu_" + NewID()
			}
			content = append(content, map[string]any{"type": "tool_use", "id": AnthropicOutID(id), "name": p.Name, "input": ArgsOf(p)})
		case Search:
			id := "srvtoolu_" + NewID()
			content = append(content,
				map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{"query": p.Text}},
				SearchResultBlock(id, p.Hits))
		}
	}
	id := AnthropicID(res.ID)
	if res.Model != "" {
		model = res.Model
	}
	reply := map[string]any{"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": StopToAnthropic(res.Stop), "stop_sequence": nil, "usage": res.Usage.Anthropic()}
	if len(res.SafeguardResults) > 0 {
		reply["safeguard_results"] = res.SafeguardResults
	}
	if len(res.StopDetails) > 0 {
		reply["stop_details"] = res.StopDetails
	}
	b, _ := json.Marshal(reply)
	return b
}

// searchResultBlock is a web_search_tool_result block of the pages a
// search found.
func SearchResultBlock(id string, hits []Hit) map[string]any {
	results := []map[string]any{}
	for _, h := range hits {
		// never the searcher's encrypted_content: it is sealed for the
		// vendor that searched, and the client would send it to another
		var age any
		if h.PageAge != "" {
			age = h.PageAge
		}
		results = append(results, map[string]any{"type": "web_search_result", "title": h.Title, "url": h.URL,
			"encrypted_content": "", "page_age": age})
	}
	return map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": results}
}

// idSeq keeps ids made within one tick of the clock apart: Windows'
// clock moves in steps of up to 15.6ms and macOS's in microseconds, so
// the parallel calls of one Gemini chunk all got the same id.
var IdSeq atomic.Uint32

// idClock is where newID reads the time; a test holds it still to stand in
// for a coarse clock.
var IdClock = time.Now

func NewID() string {
	return fmt.Sprintf("%x%08x", IdClock().UnixNano(), IdSeq.Add(1))
}

// objectSchema is a tool's input schema with no anyOf, oneOf or allOf at
// its root, which Anthropic's API refuses ("input_schema does not support
// oneOf, allOf, or anyOf at the top level"): Codex's codex_app
// automation_update has one, and Claude models behind Factory answered 400
// to every request offering it (#646). A schema that has none is sent as
// it came.
func ObjectSchema(schema json.RawMessage) json.RawMessage {
	if !bytes.Contains(schema, []byte(`Of"`)) {
		return schema
	}
	var m map[string]any
	if DecodeJSON(schema, &m) != nil || !ObjectRoot(m) {
		return schema
	}
	b, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return b
}

// stopDetails is a reply's stop_details, or nil when it had none.
func StopDetails(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}
