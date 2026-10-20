package routerredact

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

//go:embed privacy_assets/dictionary.yaml
var defaultDictionaryYAML []byte

// DictionaryEntry 描述词典检测器中的单条敏感词配置。
type DictionaryEntry struct {
	Term           string `yaml:"term"`
	Value          string `yaml:"value"`
	Label          string `yaml:"label"`
	CanonicalLabel string `yaml:"-"`
	CaseSensitive  bool   `yaml:"case_sensitive"`
	Provenance     string `yaml:"provenance"`
}

// DictionaryDetector 基于自定义词表执行精确子串与词边界扫描。
type DictionaryDetector struct {
	entries []DictionaryEntry
}

var (
	defaultDictDetectorOnce sync.Once
	defaultDictDetectorInst *DictionaryDetector
)

// DefaultDictionaryDetector 返回内嵌默认词典的单例词典检测器。
func DefaultDictionaryDetector() *DictionaryDetector {
	defaultDictDetectorOnce.Do(func() {
		defaultDictDetectorInst = NewDictionaryDetectorFromYAML(defaultDictionaryYAML)
	})
	return defaultDictDetectorInst
}

// NewDictionaryDetectorFromYAML 从 YAML 字节流解析并构建词典检测器。
func NewDictionaryDetectorFromYAML(data []byte) *DictionaryDetector {
	return NewDictionaryDetector(parseDictionaryYAML(data))
}

// NewDictionaryDetector 根据给定的词条列表构建词典检测器（按词长降序排列）。
func NewDictionaryDetector(entries []DictionaryEntry) *DictionaryDetector {
	normalized := make([]DictionaryEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		term := strings.TrimSpace(entry.Term)
		if term == "" {
			term = strings.TrimSpace(entry.Value)
		}
		label := strings.TrimSpace(entry.Label)
		if term == "" || label == "" {
			continue
		}
		canon := CanonicalizeLabel(label)
		if AllowedPassThroughLabels[canon] && canon != "PUBLIC_INFORMATION" {
			continue
		}
		key := canon + "\x00" + term
		if seen[key] {
			continue
		}
		seen[key] = true
		entry.Term = term
		entry.Value = term
		entry.CanonicalLabel = canon
		normalized = append(normalized, entry)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return len(normalized[i].Term) > len(normalized[j].Term)
	})
	return &DictionaryDetector{entries: normalized}
}

// Entries 返回词典检测器已加载的词条列表。
func (d *DictionaryDetector) Entries() []DictionaryEntry {
	if d == nil {
		return nil
	}
	return append([]DictionaryEntry(nil), d.entries...)
}

func parseDictionaryYAML(data []byte) []DictionaryEntry {
	if len(data) == 0 {
		return nil
	}
	var list []DictionaryEntry
	if err := yaml.Unmarshal(data, &list); err == nil && len(list) > 0 {
		return list
	}
	var grouped map[string][]string
	if err := yaml.Unmarshal(data, &grouped); err == nil && len(grouped) > 0 {
		var out []DictionaryEntry
		for label, terms := range grouped {
			for _, t := range terms {
				out = append(out, DictionaryEntry{
					Term:          t,
					Label:         label,
					CaseSensitive: false,
				})
			}
		}
		return out
	}
	return nil
}

// Detect 在文本中扫描所有词典词条，返回满足 ASCII 词边界约束的敏感实体区间。
func (d *DictionaryDetector) Detect(text string) []DetectedSpan {
	if d == nil || text == "" || len(d.entries) == 0 {
		return nil
	}
	var lowerText string
	var out []DetectedSpan

	for _, entry := range d.entries {
		needle := entry.Term
		if needle == "" {
			continue
		}
		haystack := text
		if !entry.CaseSensitive && hasASCIILetter(needle) {
			if lowerText == "" {
				lowerText = asciiToLower(text)
			}
			haystack = lowerText
			needle = asciiToLower(needle)
		}

		offset := 0
		for offset < len(haystack) {
			idx := strings.Index(haystack[offset:], needle)
			if idx < 0 {
				break
			}
			start := offset + idx
			end := start + len(needle)
			if validDictionaryBoundary(text, start, end) {
				out = append(out, DetectedSpan{
					Label:          entry.Label,
					CanonicalLabel: entry.CanonicalLabel,
					Start:          start,
					End:            end,
					Value:          text[start:end],
					Confidence:     0.95,
					Source:         "dictionary",
					RuleName:       "dictionary",
				})
			}
			_, step := utf8.DecodeRuneInString(haystack[start:])
			if step <= 0 {
				step = 1
			}
			offset = start + step
		}
	}
	return out
}

func validDictionaryBoundary(text string, start, end int) bool {
	if start < 0 || end <= start || end > len(text) {
		return false
	}
	firstRune, _ := utf8.DecodeRuneInString(text[start:end])
	lastRune, _ := utf8.DecodeLastRuneInString(text[start:end])
	if isASCIIAlphaNum(firstRune) && start > 0 {
		prevRune, _ := utf8.DecodeLastRuneInString(text[:start])
		if isASCIIAlphaNum(prevRune) {
			return false
		}
	}
	if isASCIIAlphaNum(lastRune) && end < len(text) {
		nextRune, _ := utf8.DecodeRuneInString(text[end:])
		if isASCIIAlphaNum(nextRune) {
			return false
		}
	}
	return true
}

func isASCIIAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func hasASCIILetter(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

func asciiToLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
