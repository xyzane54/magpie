package routerrequest

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

const JudgmentSummaryPolicy = "routing-features-v2"

// BoundedTextSummary explicitly identifies an incomplete description. Excerpts
// retain their source order; they are request data, never router instructions.
// Rune bounds control payload construction only, not local token accounting.
type BoundedTextSummary struct {
	SourceRunes         int      `json:"source_runes"`
	SHA256              string   `json:"sha256"`
	Excerpts            []string `json:"excerpts,omitempty"`
	Omitted             bool     `json:"omitted,omitempty"`
	RestrictionsOmitted bool     `json:"restrictions_omitted,omitempty"`
}

// CompactJudgment produces a detached decision-only view. It retains the entire
// conversation and every tool relationship, removes duplicate representations,
// and bounds fixed host descriptions. The execution body never uses this view.
func CompactJudgment(source JudgmentView) JudgmentView {
	v := cloneJudgment(source)
	v.SummaryPolicy = JudgmentSummaryPolicy
	const systemRunes = 1800
	perSystem := systemRunes
	if len(v.System) > 0 {
		perSystem /= len(v.System)
	}
	systems := v.System
	v.System = nil
	v.SystemSummaries = nil
	for _, text := range systems {
		summary := summarizeFixedText(text, perSystem)
		v.RestrictionsOmitted = v.RestrictionsOmitted || summary.RestrictionsOmitted
		v.SystemSummaries = append(v.SystemSummaries, summary)
	}
	const descriptionRunes = 960
	perTool := 160
	if len(v.Tools) > 6 {
		perTool = descriptionRunes / len(v.Tools)
	}
	for i := range v.Tools {
		if utf8.RuneCountInString(v.Tools[i].Description) <= perTool {
			continue
		}
		summary := summarizeFixedText(v.Tools[i].Description, perTool)
		v.Tools[i].Description = ""
		v.Tools[i].DescriptionSummary = &summary
		v.RestrictionsOmitted = v.RestrictionsOmitted || summary.RestrictionsOmitted
	}
	for i := range v.History {
		m := &v.History[i]
		if len(m.Parts) > 0 {
			var text strings.Builder
			var calls []JudgmentToolCall
			var results []JudgmentToolResult
			for _, part := range m.Parts {
				if part.Kind == "text" {
					text.WriteString(part.Text)
				}
				if part.ToolCall != nil {
					calls = append(calls, *part.ToolCall)
				}
				if part.ToolResult != nil {
					results = append(results, *part.ToolResult)
				}
			}
			if text.String() == m.Text {
				m.Text = ""
			}
			if reflect.DeepEqual(calls, m.ToolCalls) {
				m.ToolCalls = nil
			}
			if reflect.DeepEqual(results, m.ToolResults) {
				m.ToolResults = nil
			}
			if len(results) > 0 && m.ToolResult != nil && reflect.DeepEqual(results[0], *m.ToolResult) {
				m.ToolResult = nil
			}
		}
		if len(m.ToolResults) > 0 && m.ToolResult != nil && reflect.DeepEqual(m.ToolResults[0], *m.ToolResult) {
			m.ToolResult = nil
		}
		// Chat normalization records tool-role content once as text and once
		// as its result. The result is the authoritative ordered representation.
		if m.Role == "tool" && len(m.Parts) > 1 {
			var text strings.Builder
			var result *JudgmentToolResult
			results := 0
			for _, part := range m.Parts {
				if part.Kind == "text" {
					text.WriteString(part.Text)
				}
				if part.ToolResult != nil {
					result = part.ToolResult
					results++
				}
			}
			if results == 1 && result != nil && text.String() == result.Text {
				parts := m.Parts[:0]
				for _, part := range m.Parts {
					if part.Kind != "text" {
						parts = append(parts, part)
					}
				}
				m.Parts = parts
			}
		}
		for j := range m.Parts {
			if m.Parts[j].ToolResult != nil {
				compactToolResult(m.Parts[j].ToolResult)
			}
		}
		for j := range m.ToolResults {
			compactToolResult(&m.ToolResults[j])
		}
		if m.ToolResult != nil {
			compactToolResult(m.ToolResult)
		}
	}
	return v
}

func compactToolResult(result *JudgmentToolResult) {
	if len(result.Content) > 0 {
		if text, err := textValue(result.Content); err == nil && text == result.Text {
			result.Text = ""
		}
	}
}

func summarizeFixedText(text string, limit int) BoundedTextSummary {
	hash := sha256.Sum256([]byte(text))
	s := BoundedTextSummary{SourceRunes: utf8.RuneCountInString(text), SHA256: hex.EncodeToString(hash[:])}
	if s.SourceRunes <= limit {
		if text != "" {
			s.Excerpts = []string{text}
		}
		return s
	}
	s.Omitted = true
	// Split throughout the source, including trailing conditions. A prefix-only
	// or last-turn-only summary can miss the rule that determines the route.
	lines := fixedTextSegments(text)
	selected := make([]bool, len(lines))
	remaining := limit
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !restrictionLine(line) {
			continue
		}
		n := utf8.RuneCountInString(line)
		if n <= remaining {
			selected[i] = true
			remaining -= n
		} else {
			s.RestrictionsOmitted = true
		}
	}
	// Keep headings and the opening/closing context after complete conditions.
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !selected[i] && (i == 0 || i == len(lines)-1 || strings.HasPrefix(line, "#")) {
			n := utf8.RuneCountInString(line)
			if n <= remaining {
				selected[i] = true
				remaining -= n
			}
		}
	}
	for i, line := range lines {
		if selected[i] {
			s.Excerpts = append(s.Excerpts, strings.TrimSpace(line))
		}
	}
	if len(s.Excerpts) == 0 && remaining > 0 && !s.RestrictionsOmitted {
		// An unbroken descriptive paragraph still contributes a labeled excerpt.
		runes := []rune(text)
		prefix := remaining / 2
		s.Excerpts = []string{string(runes[:prefix]), string(runes[len(runes)-(remaining-prefix):])}
	}
	return s
}

// Each segment is a contiguous source substring, including its punctuation.
// A period inside a decimal, version or URL is never a sentence delimiter.
func fixedTextSegments(text string) []string {
	var segments []string
	start := 0
	for index, r := range text {
		end := index + utf8.RuneLen(r)
		boundary := r == '\n' || r == '\r' || r == '。' || r == ';' || r == '；'
		if r == '.' {
			next, _ := utf8.DecodeRuneInString(text[end:])
			boundary = end == len(text) || unicode.IsSpace(next)
		}
		if boundary {
			if segment := strings.TrimSpace(text[start:end]); segment != "" {
				segments = append(segments, segment)
			}
			start = end
		}
	}
	if segment := strings.TrimSpace(text[start:]); segment != "" {
		segments = append(segments, segment)
	}
	return segments
}

func restrictionLine(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{"must", "never", "only", "do not", "don't", "cannot", "can't", "require", "forbid", "unless", "if ", "when ", "should", "always", "limit", "cloud", "local", "必须", "不得", "禁止", "不能", "不可", "仅", "只", "需要", "如果", "除非", "当", "限制", "云端", "本地"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
