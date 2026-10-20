// Package gateway is the local LLM endpoint agents talk to. It serves the
// wire APIs coding agents speak — OpenAI Chat Completions, OpenAI
// Responses, Anthropic Messages and Google Gemini — and forwards each call to whichever
// provider serves the requested model, translating between the APIs when
// the provider does not speak the one the agent used.
package gateway

import (
	"slices"

	"github.com/yetone/magpie/internal/catalog"
)

// The APIs are close cousins; everything below is the shape they have
// in common. A request is parsed into it, and a reply is produced from it.

// saidAnything reports whether a reply has more than thinking: text, a
// call, a search or an image. A turn that only thought and then failed is
// the failure, not an answer with nothing in it.
func saidAnything(parts []Part) bool {
	for _, p := range parts {
		if p.Kind != Thinking {
			return true
		}
	}
	return false
}

// effortRank orders the reasoning levels agents and vendors name.
var effortRank = catalog.EffortRank

// ByStrength is efforts weakest first, in effortRank's order; a level it
// doesn't know keeps its place among the others of its kind, after them.
func ByStrength(efforts []string) []string {
	rank := func(e string) int {
		if i := slices.Index(effortRank, e); i >= 0 {
			return i
		}
		return len(effortRank)
	}
	out := slices.Clone(efforts)
	slices.SortStableFunc(out, func(a, b string) int { return rank(a) - rank(b) })
	return out
}

// fitEffort is the level of the model's own nearest the one asked for — a
// tie goes up — or the one asked for when the model's aren't known. Codex
// asks "medium" of a model it was given no levels for, and an agent's
// setting can outlive the model it was picked for; GLM-5.3 takes low, high
// and max only.
//
// Codex's ultra is no API's level, whatever a list says (ChatGPT's lists
// it for Codex's picker; magpie offers it on Copilot's gpt-6.1-sol, #656):
// it is sent as max, or the nearest the model has below it.
func fitEffort(want string, levels []string) string {
	if want == "ultra" {
		want = "max"
		levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return l == "ultra" })
	}
	return catalog.NearestEffort(want, levels)
}
