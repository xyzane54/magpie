package gateway

import (
	"github.com/yetone/magpie/internal/cloudcodec"

	"bytes"

	"encoding/json"

	"regexp"

	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// ---- Anthropic Messages -----------------------------------------------------

// thinkingOffUnlessAsked says thinking is off when the request doesn't
// mention it. That is what Anthropic's API assumes, but DeepSeek and other
// vendors' Anthropic endpoints think by default, so Claude Code's requests
// for a session title, sent without thinking, spent their tokens thinking.
func thinkingOffUnlessAsked(body []byte) []byte {
	var v struct {
		Thinking json.RawMessage `json:"thinking"`
	}
	if decodeJSON(body, &v) != nil || v.Thinking != nil {
		return body
	}
	// added at the end, the rest left byte for byte as the agent sent it
	b := bytes.TrimRight(body, " \t\r\n")
	if len(b) < 2 || b[len(b)-1] != '}' {
		return withFields(body, map[string]any{"thinking": map[string]any{"type": "disabled"}})
	}
	out := append([]byte{}, b[:len(b)-1]...)
	if len(bytes.TrimSpace(out)) > 1 {
		out = append(out, ',')
	}
	return append(out, `"thinking":{"type":"disabled"}}`...)
}

// alwaysThinks is a vendor refusing to turn a model's thinking off: Z.ai's
// GLM-5.3 answers 1210, "…always engages in thinking…", or, in Chinese
// (ZCode's GLM-5.3-Flash), "该模型始终支持思考，不可关闭" (#699); DashScope's
// own glm-5.3 answers "The value of the enable_thinking parameter is
// restricted to True." MiniMax's MiniMax-M3.1-Flash-Preview answers
// "…requires adaptive thinking; thinking.type="disabled" (including
// reasoning.effort=none) is not allowed (2013)", and Volcengine Ark's
// glm-5.3 "thinking.type `disabled` is not supported by this model"
// (#1141).
var alwaysThinks = regexp.MustCompile(`(?i)always engages in thinking|thinking (?:can ?not|can't) be (?:disabled|turned off)|enable_thinking[^"]{0,60}restricted to true|始终(?:支持|开启|启用)?思考|思考[^"]{0,20}(?:不可|无法|不能)关闭|requires adaptive thinking\W+thinking\.type\W+disabled\W+\(including reasoning\.effort=none\) is not allowed|thinking\.type \x60disabled\x60 is not supported by this model`)

// refusesThinkingOff is an Anthropic endpoint turning away a request
// with thinking turned off (thinking.type disabled): a model that always
// thinks (alwaysThinks), OpenRouter's mandatory reasoning, a Claude that
// takes no disabled (thinkingOffRefused), or a relay reading disabled as
// reasoning effort none and turning that level away by naming the ones
// it takes: Sub2API's gpt-6.1-sol, "gpt-6.1-sol does not support reasoning
// effort \"none\"; use low, medium, high, xhigh or max" (#1465).
func refusesThinkingOff(b []byte) bool {
	// alwaysThinks' words are matched decoded, too (#1678): 思考 escaped as \uXXXX
	return alwaysThinks.Match(wordsOf(b)) || mandatoryReasoning.Match(b) || thinkingOffRefused.Match(b) ||
		effortLevelsNamed.Match(b) && noneQuoted.Match(b)
}

// ThinksOnlyWhenAsked is a model that takes thinking turned off on the
// Messages API whatever its levels: one of Anthropic's own (anthropicModel,
// or a relay's opus-5.5). Another vendor's model there takes it off only
// when none is among its levels; without it, it always thinks, and GLM-5.3
// turns thinking disabled away (#699).
func ThinksOnlyWhenAsked(model string) bool {
	return cloudcodec.AnthropicModel.MatchString(model) || cloudcodec.ClaudeVersion.MatchString(strings.ToLower(model))
}

// withoutThinkingOff is body with its thinking left to the model, when it
// says thinking is off; false when it doesn't.
func withoutThinkingOff(body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	var th struct {
		Type string `json:"type"`
	}
	if decodeJSON(body, &q) != nil || decodeJSON(q["thinking"], &th) != nil || th.Type != "disabled" {
		return nil, false
	}
	delete(q, "thinking")
	out, err := json.Marshal(q)
	return out, err == nil
}

// adaptiveThinking is an Anthropic request as a model that thinks only
// adaptively takes it: thinking.type=enabled with a budget — sent by an
// agent that doesn't know the model (its name in magpie, an alias, or a
// group's member, mapped to the vendor's later), or by magpie fitting an
// effort to it — goes as thinking.type=adaptive, with the budget as the
// effort it is nearest in output_config.effort unless one is there (Keenc
// on Discord: claude-opus-5-5 answered 400). Read off the model the body
// is sent with, the vendor's own name; any other request, older Claudes'
// included, goes as it came, and "disabled" stays. The thinking enabled
// shows (display "summarized" by default) is still shown: Opus 4.7 and
// later leave it out of adaptive thinking unless asked (#1485), so it is
// asked for unless the request names its own display. A provider that
// refuses display has it taken out after (withoutRefusedShapes).
func adaptiveThinking(body []byte) []byte {
	th := gjson.GetBytes(body, "thinking")
	if th.Get("type").String() != "enabled" || !adaptiveOnly(gjson.GetBytes(body, "model").String()) {
		return body
	}
	thinking := map[string]any{"type": "adaptive", "display": "summarized"}
	if d := th.Get("display"); d.Exists() {
		thinking["display"] = d.Value()
	}
	fields := map[string]any{"thinking": thinking}
	if gjson.GetBytes(body, "output_config.effort").String() == "" {
		if e := effortOfBudget(int(th.Get("budget_tokens").Int())); e != "" {
			if e == "xhigh" {
				e = "max" // a budget can't tell xhigh from max (budgetOf), and 4.6 has no xhigh
			}
			oc, _ := gjson.GetBytes(body, "output_config").Value().(map[string]any)
			if oc == nil {
				oc = map[string]any{}
			}
			oc["effort"] = e
			fields["output_config"] = oc
		}
	}
	return withFields(body, fields)
}

// claudeLine finds a Claude model's family and version however a relay
// spells it: claude-sonnet-5-5, claude-sonnet-5.5, a relay's sonnet-5.5,
// Bedrock's anthropic.claude-sonnet-5-5-v1:0, Vertex's
// claude-sonnet-5-5@20261001, claude-fable-5-1.
var claudeLine = regexp.MustCompile(`(?:^|[^a-z0-9])(?:claude-)?(opus|sonnet|haiku|fable|mythos)-(\d{1,2})(?:[-.](\d{1,2}))?(?:[^0-9]|$)`)

// thinkingOff is how a Claude model takes thinking turned off.
type thinkingOff int

const (
	offUnknown      thinkingOff = iota // not a model these rules know: as sent
	offTaken                           // thinking.type=disabled at any effort
	offUpToHigh                        // disabled at effort high or below: Opus 5, Haiku 5.5
	offBetweenTools                    // disabled refused, between_tools asked for: Sonnet 5.5
	offNever                           // thinking can't be turned off: Opus 5.5, Fable 5, Mythos 5
)

// thinkingOffOf is how model takes thinking turned off, from Anthropic's
// own rules: Sonnet 5.5 answers disabled with 400 "To turn thinking off on
// this model, send "thinking": {"type": "between_tools"}…" and takes
// between_tools at effort high or below; Opus 5.5 and the Fable and
// Mythos models refuse disabled at every effort, so thinking is left out
// (they think adaptively); Opus 5 and Haiku 5.5 refuse it at xhigh and
// max. Every other Claude takes it as sent, and between_tools on none of
// them. A model of a later line than these is offUnknown, left as sent.
func thinkingOffOf(model string) thinkingOff {
	m := claudeLine.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return offUnknown
	}
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	family := m[1]
	switch {
	case family == "fable" || family == "mythos":
		if major == 5 {
			return offNever
		}
	case major < 5:
		return offTaken
	case major == 5 && minor == 0:
		if family == "opus" {
			return offUpToHigh
		}
		return offTaken // Sonnet 5, Haiku 5
	case major == 5 && minor == 5:
		switch family {
		case "sonnet":
			return offBetweenTools
		case "opus":
			return offNever
		case "haiku":
			return offUpToHigh
		}
	}
	return offUnknown
}

// thinkingOffAsTaken is an Anthropic request that turns thinking off, as
// the model it is sent to takes that (thinkingOffOf). Claude Code turns
// thinking off for auto mode's classifier and its side queries whenever
// it can't tell the model refuses it — a model reached through a gateway
// is one — and magpie turns it off itself when fitting an effort of none
// or a budget that doesn't fit; claude-sonnet-5-5 answered every one of
// them 400 (#1454). Sonnet 5.5 is sent between_tools, alone in thinking,
// with an effort above high asked as high; Opus 5.5, Fable and Mythos are
// sent no thinking, at effort low unless one is asked, with the room a
// thinking model needs on top of a short reply, as Claude Code gives it
// (classifierRoom); Opus 5 and Haiku 5.5 are asked effort high in place
// of xhigh or max. between_tools sent to a model that doesn't take it goes
// as disabled, or as no thinking where disabled is refused too. model is
// the vendor's name of it: the body's, or, for Bedrock and Vertex, whose
// body names none, the request path's. Any other request, other vendors'
// models' and older Claudes' included, goes byte for byte as it came.
func thinkingOffAsTaken(body []byte, path string) []byte {
	t := gjson.GetBytes(body, "thinking.type").String()
	if t != "disabled" && t != "between_tools" {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = path
	}
	rule := thinkingOffOf(model)
	effort := gjson.GetBytes(body, "output_config.effort").String()
	aboveHigh := effort == "xhigh" || effort == "max"
	var thinking map[string]any // nil: left out
	effortTo := ""
	switch rule {
	case offTaken:
		if t == "disabled" {
			return body
		}
		thinking = map[string]any{"type": "disabled"}
	case offUpToHigh:
		if t == "disabled" && !aboveHigh {
			return body
		}
		thinking = map[string]any{"type": "disabled"}
		if aboveHigh {
			effortTo = "high"
		}
	case offBetweenTools:
		if th := gjson.GetBytes(body, "thinking"); t == "between_tools" && len(th.Map()) == 1 && !aboveHigh {
			return body
		}
		// between_tools takes no other field (display, budget_tokens)
		thinking = map[string]any{"type": "between_tools"}
		if aboveHigh {
			effortTo = "high"
		}
	case offNever:
		if effort == "" {
			effortTo = "low"
		}
	default:
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var q map[string]any
	if dec.Decode(&q) != nil {
		return body
	}
	if thinking != nil {
		q["thinking"] = thinking
	} else {
		delete(q, "thinking")
		if n := gjson.GetBytes(body, "max_tokens").Int(); n > 0 && n < classifierRoom {
			q["max_tokens"] = n + classifierRoom
		}
	}
	if effortTo != "" {
		oc, _ := q["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		oc["effort"] = effortTo
		q["output_config"] = oc
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(q) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// asksBetweenTools is Sonnet 5.5's answer to thinking turned off, asking
// for between_tools: "To turn thinking off on this model, send "thinking":
// {"type": "between_tools"} instead of {"type": "disabled"}" as relays
// pass it on (#1454), or the API's "Use "thinking.type.between_tools" for
// the lowest thinking setting".
var asksBetweenTools = regexp.MustCompile(`(?i)between_tools\W+instead of|use\W+thinking\.type\.between_tools`)

// thinkingOffRefused is a Claude refusing thinking turned off at any
// effort, as Opus 5.5 does: ""thinking.type.disabled" is not supported for
// this model. Use "thinking.type.adaptive"…".
var thinkingOffRefused = regexp.MustCompile(`(?i)thinking\.type\.disabled\W+is not supported`)

// withBetweenTools is body with its thinking turned off as Sonnet 5.5
// asks it, at effort high at most; false when body doesn't turn it off.
func withBetweenTools(body []byte) ([]byte, bool) {
	if gjson.GetBytes(body, "thinking.type").String() != "disabled" {
		return nil, false
	}
	fields := map[string]any{"thinking": map[string]any{"type": "between_tools"}}
	if e := gjson.GetBytes(body, "output_config.effort").String(); e == "xhigh" || e == "max" {
		oc, _ := gjson.GetBytes(body, "output_config").Value().(map[string]any)
		oc["effort"] = "high"
		fields["output_config"] = oc
	}
	return withFields(body, fields), true
}

// samplingRefused is a Claude that answers temperature, top_p or top_k
// with a 400, by Anthropic's rules: Opus 4.7, 4.8, 5 and 5.5, Fable 5 and
// Mythos 5 refuse them at any value; Sonnet 5 and 5.5 refuse a value but
// their default; Haiku 5.5 takes only temperature 1 and top_p 0.99, never
// both, and no top_k ("`temperature` is deprecated for this model", #1454).
// Left out, each model samples at its default. Older Claudes, Haiku 4.5
// and Opus 4.6 among them, take all three; a later line than these is
// left as sent.
func samplingRefused(model string) bool {
	m := claudeLine.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	switch m[1] {
	case "fable", "mythos":
		return major == 5
	case "opus":
		return major == 4 && minor >= 7 || major == 5 && (minor == 0 || minor == 5)
	case "sonnet":
		return major == 5 && (minor == 0 || minor == 5)
	case "haiku":
		return major == 5 && minor == 5
	}
	return false
}

// samplingAsTaken is an Anthropic request without temperature, top_p and
// top_k when the model it is sent to refuses them (samplingRefused):
// Claude Code's auto mode classifier, and magpie's own classifiers, ask at
// temperature 0. model is the vendor's name, from the body or, for
// Bedrock and Vertex, the path. Any other request goes byte for byte as it
// came.
func samplingAsTaken(body []byte, path string) []byte {
	if !hasSampling(body) {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = path
	}
	if !samplingRefused(model) {
		return body
	}
	return withoutFields(body, "temperature", "top_p", "top_k")
}

func hasSampling(body []byte) bool {
	r := gjson.GetManyBytes(body, "temperature", "top_p", "top_k")
	return r[0].Exists() || r[1].Exists() || r[2].Exists()
}

// samplingDeprecated is a Claude refusing a sampling parameter, as Haiku
// 5.5 does: "`temperature` is deprecated for this model." (and `top_p`,
// `top_k`).
var samplingDeprecated = regexp.MustCompile("(?i)\\b(?:temperature|top_p|top_k)`?\\W+is (?:deprecated|not supported) for this model")

// withoutSampling is body without temperature, top_p and top_k; false
// when it has none of them.
func withoutSampling(body []byte) ([]byte, bool) {
	if !hasSampling(body) {
		return nil, false
	}
	return withoutFields(body, "temperature", "top_p", "top_k"), true
}

// AdaptiveThinking is adaptiveOnly for agents told how to ask a model: a
// Claude that takes thinking.type=adaptive and an effort, never a budget.
func AdaptiveThinking(model string) bool { return adaptiveOnly(model) }

// withOutputEffort is body asking, in output_config.effort, for the level
// of levels nearest the reasoning it asks for; body when it asks for none.
func withOutputEffort(body []byte, levels []string) []byte {
	e := requestEffort(provider.Anthropic, body)
	var v struct {
		OutputConfig map[string]any `json:"output_config"`
	}
	if e == "" || decodeJSON(body, &v) != nil {
		return body
	}
	oc := v.OutputConfig
	if oc == nil {
		oc = map[string]any{}
	}
	oc["effort"] = fitEffort(e, levels)
	return withFields(body, map[string]any{"output_config": oc})
}

// buildAnthropic renders a request for an Anthropic-style upstream.
func buildAnthropic(r *Request, model string) []byte {
	return anthropicBody(r, model, false)
}
