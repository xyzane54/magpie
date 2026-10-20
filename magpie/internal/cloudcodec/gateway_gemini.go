package cloudcodec

// gemini is a Usage as usageMetadata: the prompt count includes cached
// tokens (see prompt), and thoughts are counted apart from the answer.
func (u Usage) Gemini() map[string]any {
	out := map[string]any{"promptTokenCount": u.Prompt(), "candidatesTokenCount": max(u.Output-u.Reasoning, 0),
		"totalTokenCount": u.Prompt() + u.Output}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	return out
}
