package cloudcodec

import (
	"encoding/json"
	"strings"
	"time"
)

// Kind is what a part of a message holds.
type Kind string

const (
	Text       Kind = "text"
	Image      Kind = "image"
	File       Kind = "file"
	ToolCall   Kind = "tool_call"
	ToolResult Kind = "tool_result"
	Thinking   Kind = "thinking"
	Search     Kind = "web_search" // a web search run for the model: its query and hits
)

// Part is one block of a message.
type Part struct {
	Kind Kind
	Text string // text, thinking, or a tool result's output

	// image or file
	MediaType string
	Data      string // base64
	URL       string

	// tool_call
	ID   string
	Name string
	Args json.RawMessage // a JSON object

	// tool_result
	CallID     string
	IsError    bool
	Images     []Part         // the images the tool returned beside its text
	Standalone map[string]any // native Responses notification with no call ID

	// thinking: its signature (Anthropic's, or a Gemini thought's). On a
	// tool call, and on text, Gemini's thoughtSignature: a tool call's
	// comes only from Google, as gemini_signature.go carries it, and
	// text's only from a Gemini API (#1445)
	Signature string
	// thinking an upstream sealed, as its API wrote it: Anthropic's
	// redacted_thinking block, a Responses reasoning item with its id,
	// summary and encrypted_content. Only that API's builder, encoder and
	// renderer write it, byte for byte; to any other it is reasoning with
	// no text, or Text's (#1445). SealedBy is the API (sealAnthropic,
	// sealResponses).
	Sealed   json.RawMessage
	SealedBy string

	// web_search: Text is the query
	Hits []Hit
}

// The APIs a Part's Sealed reasoning is in.
const (
	SealAnthropic = "anthropic"
	SealResponses = "responses"
)

// sealedBy reports whether p is reasoning api sealed.
func (p Part) sealedBy(api string) bool {
	return p.Kind == Thinking && p.SealedBy == api && len(p.Sealed) > 0
}

// Hit is a page a web search found.
type Hit struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// PageAge is how old the search said the page is, "" when it didn't
	PageAge string `json:"page_age,omitempty"`
}

// attachmentText is the fallback when a protocol cannot carry a Gemini file.
// Inline data has no URL to show and must not be relabeled as an image.
func AttachmentText(p Part) string {
	if p.URL != "" {
		return "[attachment " + p.MediaType + ": " + p.URL + "]"
	}
	return "[attachment " + p.MediaType + "]"
}

// Message is one turn.
type Message struct {
	Role  string // user | assistant
	Parts []Part
}

// joinSplitCalls joins an assistant message onto the assistant message
// before it when that one made tool calls: Anthropic's Messages takes
// consecutive assistant messages as one turn, and an agent can send a
// turn's parallel tool_use blocks split over two of them, answered by one
// user message (#1275). A builder that closes a turn's calls at the next
// assistant message would otherwise answer the first calls as interrupted
// and lose their real results. ms is left as it is.
func JoinSplitCalls(ms []Message) []Message {
	out := make([]Message, 0, len(ms))
	for _, m := range ms {
		if n := len(out); n > 0 && m.Role == "assistant" && out[n-1].Role == "assistant" && HasCalls(out[n-1].Parts) {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			continue
		}
		out = append(out, Message{Role: m.Role, Parts: append([]Part(nil), m.Parts...)})
	}
	return out
}

// hasCalls reports whether parts hold a tool call.
func HasCalls(parts []Part) bool {
	for _, p := range parts {
		if p.Kind == ToolCall {
			return true
		}
	}
	return false
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema of the arguments
	Strict      bool            // the client asked for its arguments held to the schema
}

// Request is a call to a model, whichever API it arrived in.
type Request struct {
	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string // "" | auto | none | required | name:<tool>
	MaxTokens  int
	Temp       *float64
	TopP       *float64
	Stop       []string
	Stream     bool
	Effort     string // low | medium | high | xhigh | max, when the client asked
	Thinking   bool   // the client asked for visible reasoning
	// ThinkOff is the client turning reasoning off: effort "none" (which
	// Effort reads as low, for vendors with no way to turn it off) or an
	// Anthropic request with thinking disabled.
	ThinkOff  bool
	Parallel  *bool // parallel tool calls allowed
	WebSearch bool  // the client offered its provider's own web search
	Fast      bool  // the client asked for priority processing: service_tier priority (Codex's Fast mode)
	// Ultrafast is Codex's Ultrafast (service_tier "ultrafast"), which a
	// ChatGPT account on a plan with it offers on its models; Fast is set
	// with it, so where there is no Ultrafast the request goes fast
	Ultrafast bool
	// Tier is the service_tier the client sent, as it sent it. It goes on
	// as it is to a provider the user added by its address (OwnTier):
	// a relay of theirs may serve Fast and Ultrafast where magpie can't
	// tell (hsiangron on X: both were left out)
	Tier string
	// OwnTier is set for such a provider (gateway.go, as each attempt is
	// built); one that turns the field away is asked again without it
	// (optionalFields)
	OwnTier bool
	// CacheKey is the client's prompt_cache_key (Codex sends its thread's
	// id), which OpenAI, and relays in front of it, route a conversation by
	// to where its prompt is cached.
	CacheKey string
	// Include is a Responses client's include, the extra output it asked
	// for (Codex's reasoning.encrypted_content), which a Responses upstream
	// is asked for too: a relay may refuse a request without it (#315).
	Include []string
	// ClientMetadata and Text are a Responses client's client_metadata
	// (Codex's installation and session ids, which a relay may check, #374)
	// and text (its verbosity), which go on as they were sent when the
	// request is built again for a Responses upstream; no other API takes
	// them. text's format is read into Format.
	ClientMetadata json.RawMessage
	Text           json.RawMessage
	// Metadata is an Anthropic client's metadata (Claude Code's user_id),
	// which goes on as it was sent when the request is built again for an
	// Anthropic upstream: a relay that serves only Claude Code turns a
	// request without it away (#359).
	Metadata json.RawMessage
	// Safeguards are the caller's safety context, opaque to the gateway.
	Safeguards    json.RawMessage
	SafeguardBeta string
	// Format is the shape the client asked the answer in (structured
	// output: format.go), nil for plain text. Text, a Responses client's,
	// holds it no more: it is asked for again in the upstream's own words.
	Format *Format
	// FormatTold is set when Format was told in the system prompt instead
	// (inSystem): the client still wants the answer as JSON.
	FormatTold bool
	// GeminiCompat is the upstream being Gemini's OpenAI-compatible API
	// (AI Studio's, or a proxy in front of it on this machine or the LAN),
	// which gives the model's thoughts only when asked in thinking_config.
	GeminiCompat bool
	// OffLevel is the level such an API is asked to think at when the
	// client turned reasoning off: Gemini 3 can't stop thinking, and thinks
	// least at minimal, or at its lowest level where it has no minimal or
	// turned minimal away (geminiLevels). "" is minimal.
	OffLevel string
	// Resume is set on a request built to go on with a reply the client
	// already has part of (continuation.go): its last message is that
	// part, an assistant message the model goes on from, not a turn
	// answered.
	Resume bool
	// LastIsTurn is set on a request from a client whose API reads a
	// conversation ending with the assistant's message as a turn already
	// said, to answer after (OpenAI's Chat and Responses), not one to go
	// on from (Anthropic's prefill): to a model that takes no prefill it
	// is asked with a user turn after it (prefill.go).
	LastIsTurn bool
	// Namespaced are the tools a Responses client offered inside a
	// namespace, by the flat name the model is offered them under.
	Namespaced map[string]NsTool
	// Grok is set when the model asked is one of Grok's, whose calls'
	// arguments reach the client with their zero fractions dropped
	// (grok_integral.go).
	Grok bool
}

// nsTool is a tool as a Responses client knows it: by its namespace and its
// name in it (Codex's collaboration.spawn_agent). Search is Codex's own
// tool search, offered to the model as a function and handed back as the
// tool_search_call Codex runs. Custom is a custom (freeform) tool, offered
// as a function taking its input, its call handed back as the
// custom_tool_call Codex runs.
type NsTool struct {
	Namespace, Name string
	Search, Custom  bool
}

// EventKind is what a streamed event carries.
type EventKind int

const (
	KStart     EventKind = iota // MsgID, Model, Usage (input side)
	KText                       // Text
	KThink                      // Text (reasoning)
	KSig                        // Text (thinking signature)
	KToolStart                  // ID, Name
	KToolArgs                   // Text (partial JSON of the arguments)
	KStop                       // Stop
	KUsage                      // Usage
	KError                      // Text
	KSearch                     // Text (the query), Hits: a web search run for the model
	KImage                      // Name (media type), Text (base64): an image the model made
	// KThinkStart: a new block of reasoning begins (Anthropic's thinking
	// block, a Responses reasoning item, ID its id): what follows is not
	// the one before's, even with nothing between them
	KThinkStart
	// KSealed: Text is reasoning the upstream sealed, whole as its API
	// wrote it, Name that API (Part.Sealed): Anthropic's redacted_thinking
	// block, a Responses reasoning item as it was done
	KSealed
	// KTextSig: Text is Gemini's thoughtSignature on the text before it
	KTextSig
)

// Event is one thing a streaming reply said.
type Event struct {
	Kind   EventKind
	Status int // upstream HTTP status for KError, when known
	Text   string
	ID     string
	Name   string
	MsgID  string
	Model  string
	Stop   string // stop | length | tool | filter
	// Code: for KError, the source error or safety-filter code (rate_limit,
	// server_error, bio_policy, content_filter…); RequestID: the vendor's id for the
	// request the event is of, when it is known by then
	Code             string
	RequestID        string
	Usage            Usage
	Hits             []Hit
	SafeguardResults json.RawMessage
	// StopDetails: Anthropic's stop_details, what its classifier said of
	// a refusal (#1768), carried to the client as it came
	StopDetails json.RawMessage

	// Wait: for KError, how long the vendor said to wait before asking
	// again, when it said; sent on as Retry-After
	Wait time.Duration
}

// Usage counts tokens.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	// CacheWrite1h is how many of the CacheWrite tokens were written to
	// be kept for an hour, which Anthropic bills at 2× input where a
	// 5-minute write is 1.25×, when its usage says so (cache_creation's
	// ephemeral_1h_input_tokens)
	CacheWrite1h int `json:"cache_write_1h,omitempty"`
	Reasoning    int `json:"reasoning"`
	// Served: the model the vendor's reply says answered, when it named
	// one — which may not be the one it was asked for
	Served string `json:"served,omitempty"`
	// Upstream: the provider an aggregator says answered behind it
	// (OpenRouter's "provider": DeepInfra, Novita …), when it says one
	Upstream string `json:"upstream,omitempty"`
	// RequestID: the id the vendor gave the request, from its reply's
	// headers (Claude Code's own for a subscription); ErrType: what a
	// failed request's error body called the error
	RequestID string `json:"request_id,omitempty"`
	// ResponseID is the final client response ID, independent of request headers.
	ResponseID string `json:"response_id,omitempty"`
	ErrType    string `json:"err_type,omitempty"`
	// Stop is why the upstream said its reply ended, in its own words
	// (stop_reason, finish_reason, a Responses status): "" when it said
	// none, as a stream that just stops does (upstreamStop)
	Stop string `json:"stop,omitempty"`
	// Tier is the service tier the reply says it ran at (upstreamTier),
	// "" when it says none
	Tier string `json:"tier,omitempty"`
	// Deferred is what the reply counted of earlier requests of its
	// conversation, which their own replies couldn't (cUsage's
	// MagpieDeferred): billed with this one, never told as its prompt
	Deferred *Usage `json:"deferred,omitempty"`
}

// billed is what the request is billed for: its own count, and what it
// counted of earlier ones (Deferred).
func (u Usage) Billed() Usage {
	if d := u.Deferred; d != nil {
		u.Input += d.Input
		u.Output += d.Output
		u.CacheRead += d.CacheRead
		u.CacheWrite += d.CacheWrite
		u.CacheWrite1h += d.CacheWrite1h
		u.Reasoning += d.Reasoning
		u.Deferred = nil
	}
	return u
}

// prompt is every token the prompt came to, as OpenAI's and Gemini's
// counts have it: Anthropic's leaves out what was read from its cache and
// what was written to it.
func (u Usage) Prompt() int {
	return u.Input + u.CacheRead + u.CacheWrite
}

func (u *Usage) Add(v Usage) {
	if v.Input > 0 {
		u.Input = v.Input
	}
	if v.Output > 0 {
		u.Output = v.Output
	}
	if v.CacheRead > 0 {
		u.CacheRead = v.CacheRead
	}
	if v.CacheWrite > 0 {
		u.CacheWrite = v.CacheWrite
	}
	if v.CacheWrite1h > 0 {
		u.CacheWrite1h = v.CacheWrite1h
	}
	if v.Reasoning > 0 {
		u.Reasoning = v.Reasoning
	}
	if v.Served != "" {
		u.Served = v.Served
	}
	if v.Upstream != "" {
		u.Upstream = v.Upstream
	}
	if v.Tier != "" {
		u.Tier = v.Tier
	}
	if v.RequestID != "" {
		u.RequestID = v.RequestID
	}
	if v.ResponseID != "" {
		u.ResponseID = v.ResponseID
	}
	if v.ErrType != "" {
		u.ErrType = v.ErrType
	}
	if v.Stop != "" {
		u.Stop = v.Stop
	}
	if v.Deferred != nil {
		u.Deferred = v.Deferred
	}
}

// Result is a whole reply, for non-streaming clients.
type Result struct {
	ID               string
	Model            string
	Parts            []Part
	Stop             string
	Usage            Usage
	SafeguardResults json.RawMessage
	StopDetails      json.RawMessage
}

// collector assembles a Result from events. Encoders use the same logic to
// know what the reply contained so far.
type Collector struct {
	Res  Result
	args strings.Builder // arguments of the open tool call
	// fresh: a KThinkStart said the next reasoning is a block of its own
	fresh bool
	Err   string
	// the error's status and kind, as its event gave them
	ErrStatus int
	ErrCode   string
}

func (c *Collector) last(k Kind) *Part {
	if n := len(c.Res.Parts); n > 0 && c.Res.Parts[n-1].Kind == k {
		return &c.Res.Parts[n-1]
	}
	return nil
}

// open is the reasoning the next of it goes on: the last part, when it is
// reasoning not sealed whole, and no new block has begun since.
func (c *Collector) open() *Part {
	if c.fresh {
		return nil
	}
	if p := c.last(Thinking); p != nil && len(p.Sealed) == 0 {
		return p
	}
	return nil
}

func (c *Collector) closeTool() {
	if p := c.last(ToolCall); p != nil && p.Args == nil {
		s := strings.TrimSpace(c.args.String())
		if s == "" {
			s = "{}"
		}
		p.Args = json.RawMessage(s)
		c.args.Reset()
	}
}

func (c *Collector) Add(ev Event) {
	if len(ev.SafeguardResults) > 0 {
		c.Res.SafeguardResults = ev.SafeguardResults
	}
	if len(ev.StopDetails) > 0 {
		c.Res.StopDetails = ev.StopDetails
	}
	switch ev.Kind {
	case KStart:
		c.Res.ID, c.Res.Model = ev.MsgID, ev.Model
		c.Res.Usage.Add(ev.Usage)
	case KText:
		if p := c.last(Text); p != nil {
			p.Text += ev.Text
		} else {
			c.closeTool()
			c.Res.Parts = append(c.Res.Parts, Part{Kind: Text, Text: ev.Text})
		}
	case KThinkStart:
		c.fresh = true
		return
	case KThink:
		if p := c.open(); p != nil {
			p.Text += ev.Text
		} else {
			c.closeTool()
			c.Res.Parts = append(c.Res.Parts, Part{Kind: Thinking, Text: ev.Text})
		}
	case KSig:
		// a block signed with no text in it (Claude's thinking when its
		// display is omitted) is a block all the same (#1445)
		if p := c.open(); p != nil {
			p.Signature += ev.Text
		} else if c.fresh {
			c.closeTool()
			c.Res.Parts = append(c.Res.Parts, Part{Kind: Thinking, Signature: ev.Text})
		}
	case KSealed:
		// a Responses item's summary came before it as reasoning of its
		// own: the item is that reasoning, sealed
		if p := c.open(); p != nil && ev.Name == SealResponses && p.Signature == "" {
			p.Sealed, p.SealedBy = json.RawMessage(ev.Text), ev.Name
		} else {
			c.closeTool()
			c.Res.Parts = append(c.Res.Parts, Part{Kind: Thinking, Sealed: json.RawMessage(ev.Text), SealedBy: ev.Name})
		}
	case KTextSig:
		if p := c.last(Text); p != nil && p.Signature == "" {
			p.Signature = ev.Text
		} else {
			c.closeTool()
			c.Res.Parts = append(c.Res.Parts, Part{Kind: Text, Signature: ev.Text})
		}
	case KToolStart:
		c.closeTool()
		c.Res.Parts = append(c.Res.Parts, Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name})
	case KToolArgs:
		c.args.WriteString(ev.Text)
	case KStop:
		c.closeTool()
		c.Res.Stop = ev.Stop
	case KUsage:
		c.Res.Usage.Add(ev.Usage)
		c.Res.Usage.Add(Usage{RequestID: ev.RequestID})
	case KError:
		c.Err, c.ErrStatus, c.ErrCode = ev.Text, ev.Status, ev.Code
	case KSearch:
		c.closeTool()
		c.Res.Parts = append(c.Res.Parts, Part{Kind: Search, Text: ev.Text, Hits: ev.Hits})
	case KImage:
		c.closeTool()
		c.Res.Parts = append(c.Res.Parts, Part{Kind: Image, MediaType: ev.Name, Data: ev.Text})
	}
	switch ev.Kind {
	case KStart, KUsage, KError:
	default:
		// what began has had its first content, or something else came
		c.fresh = false
	}
}

func (c *Collector) Finish() Result {
	c.closeTool()
	// a reply of tool calls ended "stop" (a relay's end_turn beside its
	// tool_use blocks, 蓝猫 on Discord) is the client's to run the calls
	// of: told it stopped, an agent ends its turn there
	if c.Res.Stop == "" || c.Res.Stop == "stop" {
		c.Res.Stop = "stop"
		for _, p := range c.Res.Parts {
			if p.Kind == ToolCall {
				c.Res.Stop = "tool"
			}
		}
	}
	return c.Res
}

// argsOf is a tool call's arguments as a JSON object, never empty.
func ArgsOf(p Part) json.RawMessage {
	if len(p.Args) == 0 || strings.TrimSpace(string(p.Args)) == "" {
		return json.RawMessage("{}")
	}
	return p.Args
}

// argsString is the same as a string, for the APIs that want one.
func ArgsString(p Part) string { return string(ArgsOf(p)) }

// parseArgs turns the string form of arguments into an object; a string
// that is not JSON is wrapped so nothing is lost.
func ParseArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) && strings.HasPrefix(s, "{") {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(map[string]string{"input": s})
	return b
}

// text joins the text parts of a message.
func ProtocolText(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == Text {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// stringOrText reads a JSON value that is either a string or an array of
// {type:"text", text} blocks (Anthropic's system, tool results…).
func StringOrText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if DecodeJSON(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ToolName string `json:"tool_name"`
	}
	if DecodeJSON(raw, &blocks) == nil {
		var b strings.Builder
		for _, x := range blocks {
			switch x.Type {
			case "text", "input_text", "output_text":
				b.WriteString(x.Text)
			case "tool_reference":
				// Claude Code's ToolSearch loads a deferred tool by naming
				// it; every tool is already offered to a model elsewhere, so
				// it is told the tool is there, not handed an empty result
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(ToolLoadedText(x.ToolName))
			}
		}
		return b.String()
	}
	return ""
}

// toolOutput reads a tool's result: its text, as stringOrText reads it,
// and the images in it. Anthropic's tool_result holds image blocks, a
// Responses function_call_output input_image parts and a Chat tool message,
// from clients that send them, image_url parts. An image named only by a
// vendor's file id has nothing to carry and is left out.
func ToolOutput(raw json.RawMessage) (string, []Part) {
	text := StringOrText(raw)
	var blocks []struct {
		Type   string `json:"type"`
		Source *struct {
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if len(raw) == 0 || raw[0] != '[' || DecodeJSON(raw, &blocks) != nil {
		return text, nil
	}
	var images []Part
	for _, b := range blocks {
		switch b.Type {
		case "image":
			if s := b.Source; s != nil && s.Data != "" {
				images = append(images, Part{Kind: Image, MediaType: s.MediaType, Data: s.Data})
			} else if s != nil && s.URL != "" {
				images = append(images, Part{Kind: Image, URL: s.URL})
			}
		case "input_image", "image_url":
			var u string
			if DecodeJSON(b.ImageURL, &u) != nil {
				var o struct {
					URL string `json:"url"`
				}
				DecodeJSON(b.ImageURL, &o)
				u = o.URL
			}
			if u != "" {
				images = append(images, ImagePart(u))
			}
		}
	}
	return text, images
}

// effortOf normalises the reasoning effort names the APIs use.
func EffortOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal", "none":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(s)
	case "ultra": // Codex's max, with its own agents to hand work to
		return "max"
	}
	return ""
}

// budgetOf is the Anthropic thinking budget for an effort level.
func BudgetOf(effort string) int {
	switch effort {
	case "low":
		return 4096
	case "medium":
		return 10000
	case "high":
		return 24000
	case "xhigh", "max":
		return 32000
	}
	return 10000
}

// effortOfBudget goes the other way, for Anthropic clients asking others.
func EffortOfBudget(n int) string {
	switch {
	case n <= 0:
		return ""
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	case n <= 24000:
		return "high"
	}
	return "xhigh"
}
