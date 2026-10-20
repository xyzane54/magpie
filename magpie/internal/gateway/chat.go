package gateway

import (
	"bytes"

	"regexp"

	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// ---- OpenAI Chat Completions --------------------------------------------------

// buildChat renders a request for a Chat Completions upstream.
// buildHost is the host a request for p is built for. Cursor's plugin is
// "cursor" (plugin://cursor) moved or not: a moved one's Host is the
// built-in's, for show, which Cursor's is none, and its fast mode was lost
// on every request magpie translated (#1360).
func buildHost(p provider.Provider) string {
	if p.IsPlugin() && p.PluginProvider() == "cursor" {
		return "cursor"
	}
	return p.Host()
}

// thinkingEffort moves the level the new Kimi Code (2.x) asks a Chat
// request for, inside its thinking switch ({"type":"enabled","effort":
// "high"}, #333), to reasoning_effort: where kimi-cli put it, beside
// thinking's type, and where the gateway and every other Chat API read it.
// A request that says reasoning_effort itself is left as it is.
func thinkingEffort(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"effort"`)) {
		return body
	}
	var v struct {
		ReasoningEffort *string        `json:"reasoning_effort"`
		Thinking        map[string]any `json:"thinking"`
	}
	if decodeJSON(body, &v) != nil || v.ReasoningEffort != nil {
		return body
	}
	effort, _ := v.Thinking["effort"].(string)
	if effort == "" {
		return body
	}
	delete(v.Thinking, "effort")
	return withFields(body, map[string]any{"reasoning_effort": effort, "thinking": v.Thinking})
}

// thinkingConfigField is Gemini's thinking_config as unfit remembers a
// provider that refused it.
const thinkingConfigField = "thinking_config"

// gemini3Levels are the levels Gemini 3 thinks at, of which a model may
// take only some (gemini-3.8-flash has no minimal): those it is asked at
// where its own aren't known.
var gemini3Levels = []string{"minimal", "low", "medium", "high"}

// geminiLevelRefused is Gemini turning away a thinking level the model
// hasn't, which names thinking but isn't thinking_config turned away
// (refusesThinkingConfig): gemini-3.8-flash's 400 "Thinking level MINIMAL
// is not supported for this model. Please retry with other thinking level."
// at AI Studio's OpenAI-compatible API, and "Thinking level is unsupported:
// THINKING_LEVEL_MINIMAL" at Vertex AI's, for reasoning_effort minimal and
// thinking_level minimal alike; and gemini-3.1-pro-preview's
// "thinking_level MINIMAL is not supported by this model" at its
// generateContent.
var geminiLevelRefused = regexp.MustCompile(`(?i)thinking[ _]level\W+(?:(minimal|low|medium|high)\W+is not supported|is unsupported\W+thinking_level_(minimal|low|medium|high))`)

// geminiRefusedLevel is the thinking level Gemini turned away in body
// (geminiLevelRefused), or "".
func geminiRefusedLevel(body []byte) string {
	m := geminiLevelRefused.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.ToLower(string(m[1]) + string(m[2]))
}

// refusesThinkingConfig recognizes an upstream turning a request away for
// the thinking_config it was sent, by its error naming it.
func refusesThinkingConfig(status int, body []byte) bool {
	if !badRequest(status) {
		return false
	}
	b := bytes.ToLower(body)
	return bytes.Contains(b, []byte("extra_body")) || bytes.Contains(b, []byte("thinking")) || bytes.Contains(b, []byte("include_thoughts"))
}
