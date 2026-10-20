package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routerruntime"
)

const (
	DefaultLLMMaxChunkChars      = 1800
	DefaultLLMChunkOverlap       = 120
	DefaultLLMReviewMinRemaining = 3 * time.Second
)

const PrimaryLLMSystemPrompt = `你是隐私信息抽取器。请识别文本中的额外敏感实体，只输出 JSON，格式为 {"entities":[{"label":"NAME|PHONE|EMAIL|WECHAT|QQ|ID_CARD|PASSPORT|HM_PASS|TW_PASS|DRIVER_LICENSE|MILITARY_ID|BIRTH_DATE|BANK_CARD|SOCIAL_CREDIT_CODE|CASE_NUMBER|WORK_ORDER|PLATE_NUMBER|ADDRESS|ORG|COMPANY|SCHOOL|IP|MAC|HOSTNAME|URL|SECRET|AMOUNT","value":"原文子串","confidence":0.0}]}。
要求：
1. value 必须是原文中的精确子串。
2. 不要输出解释，不要输出 markdown。
3. 优先识别中文姓名、机构、地址、口令、密钥、业务单号、证件号以及与收入/合同/打款/余额/税费相关的敏感金额。
4. 不要把普通年份、纯季节时间、宏观统计数字、泛化词（如"客户""公司""接口"）识别成敏感实体。
5. 如果没有发现，输出 {"entities":[]}。`

const FocusedReviewLLMSystemPrompt = `你是隐私漏检复核器。请结合整段上下文，检查文本中是否还有被遗漏的敏感信息，只输出 JSON，格式为 {"entities":[{"label":"NAME|PHONE|EMAIL|WECHAT|QQ|ID_CARD|PASSPORT|HM_PASS|TW_PASS|DRIVER_LICENSE|MILITARY_ID|BIRTH_DATE|BANK_CARD|SOCIAL_CREDIT_CODE|CASE_NUMBER|WORK_ORDER|PLATE_NUMBER|ADDRESS|ORG|COMPANY|SCHOOL|IP|MAC|HOSTNAME|URL|SECRET|AMOUNT","value":"原文子串","confidence":0.0}]}。
重点检查：
1. 简称、代称、花名、英文名、口语化提及的人名或联系人。
2. 未带完整后缀的机构名、部门名、客户名、项目代号、学校/医院/园区名称。
3. 口语化地址、楼栋房间、工位、园区、家庭住址片段。
4. 散落在自然语言里的口令、验证码、内网地址、业务单号、客户编号、工单号。
5. 与薪资、合同、报价、回款、转账、余额、税费相关的金额数字（含中文大写或口语化金额）。
要求：
1. 已识别实体列表中的完全相同子串无需重复返回，但若存在更完整的边界或遗漏的新实体必须返回。
2. value 必须是原文中的精确子串，不要改写。
3. 不要输出解释，不要输出 markdown；若无新增实体，输出 {"entities":[]}。`

// TextChunk represents a slice of input text with both rune and UTF-8 byte offsets.
type TextChunk struct {
	RuneOffset int
	ByteOffset int
	Text       string
}

// LLMRawEntity represents a parsed candidate entity from the local LLM output.
type LLMRawEntity struct {
	Label      string
	Value      string
	Confidence float64
	Start      *int
	End        *int
}

// LLMCompletionFunc is an optional callback for executing chat completions directly in tests or custom runtimes.
type LLMCompletionFunc func(ctx context.Context, systemPrompt, userPrompt string, review bool) (string, error)

// LocalLLMDetectorOptions configures LocalLLMDetector.
type LocalLLMDetectorOptions struct {
	Invoker            RuntimeInvoker
	ModelID            string
	CompletionFunc     LLMCompletionFunc
	MaxChunkChars      int
	ChunkOverlap       int
	EnableReviewPass   bool
	ReviewMinRemaining time.Duration
}

// LocalLLMDetector performs two-stage chunked privacy entity extraction using a local LLM.
type LocalLLMDetector struct {
	invoker            RuntimeInvoker
	modelID            string
	completionFunc     LLMCompletionFunc
	maxChunkChars      int
	chunkOverlap       int
	enableReviewPass   bool
	reviewMinRemaining time.Duration
}

// NewLocalLLMDetector creates a LocalLLMDetector with default chunking and review settings.
func NewLocalLLMDetector(opts LocalLLMDetectorOptions) *LocalLLMDetector {
	maxChunk := opts.MaxChunkChars
	if maxChunk <= 0 {
		maxChunk = DefaultLLMMaxChunkChars
	}
	overlap := opts.ChunkOverlap
	if overlap < 0 {
		overlap = DefaultLLMChunkOverlap
	} else if overlap == 0 && opts.MaxChunkChars == 0 {
		overlap = DefaultLLMChunkOverlap
	}
	if overlap >= maxChunk {
		overlap = maxChunk / 4
	}
	minRem := opts.ReviewMinRemaining
	if minRem <= 0 {
		minRem = DefaultLLMReviewMinRemaining
	}
	return &LocalLLMDetector{
		invoker:            opts.Invoker,
		modelID:            opts.ModelID,
		completionFunc:     opts.CompletionFunc,
		maxChunkChars:      maxChunk,
		chunkOverlap:       overlap,
		enableReviewPass:   opts.EnableReviewPass,
		reviewMinRemaining: minRem,
	}
}

// ChunkText splits text into overlapping rune chunks, preferring newline boundaries near the chunk end.
func ChunkText(text string, maxChars, overlap int) []TextChunk {
	if text == "" {
		return nil
	}
	if maxChars <= 0 {
		maxChars = DefaultLLMMaxChunkChars
	}
	if overlap < 0 {
		overlap = DefaultLLMChunkOverlap
	}
	if overlap >= maxChars {
		overlap = maxChars / 4
	}
	runes, runeToByte := buildRuneToByteIndex(text)
	if len(runes) <= maxChars {
		return []TextChunk{{RuneOffset: 0, ByteOffset: 0, Text: text}}
	}

	var chunks []TextChunk
	start := 0
	for start < len(runes) {
		end := min(len(runes), start+maxChars)
		if end < len(runes) {
			// Prefer splitting at the last '\n' within the last 200 characters of the chunk.
			newlineFloor := max(start+1, end-200)
			splitAt := -1
			for i := end - 1; i >= newlineFloor; i-- {
				if runes[i] == '\n' {
					splitAt = i + 1
					break
				}
			}
			if splitAt < 0 {
				// Fallback: sentence boundary in the second half of the chunk.
				sentenceFloor := start + maxChars/2
				for i := end - 1; i >= sentenceFloor; i-- {
					r := runes[i]
					if r == '。' || r == '！' || r == '？' || r == '；' || r == ';' {
						splitAt = i + 1
						break
					}
					if (r == '.' || r == '!' || r == '?') && i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
						splitAt = i + 1
						break
					}
				}
			}
			if splitAt > start {
				end = splitAt
			}
		}
		byteStart := runeToByte[start]
		byteEnd := runeToByte[end]
		chunks = append(chunks, TextChunk{
			RuneOffset: start,
			ByteOffset: byteStart,
			Text:       text[byteStart:byteEnd],
		})
		if end >= len(runes) {
			break
		}
		nextStart := end - overlap
		if nextStart <= start {
			nextStart = start + 1
		}
		start = nextStart
	}
	return chunks
}

// Detect runs chunked Primary + optional Focused Review LLM extraction on text.
func (d *LocalLLMDetector) Detect(ctx context.Context, text string, existing []DetectedSpan) ([]DetectedSpan, error) {
	if d == nil || text == "" || (d.invoker == nil && d.completionFunc == nil) {
		return nil, nil
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	chunks := ChunkText(text, d.maxChunkChars, d.chunkOverlap)
	var allSpans []DetectedSpan
	type spanKey struct {
		canonical string
		start     int
		end       int
	}
	seen := make(map[spanKey]bool)

	for _, chunk := range chunks {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		chunkExisting := filterSpansInChunk(existing, chunk.ByteOffset, chunk.ByteOffset+len(chunk.Text))
		chunkSpans, err := d.detectChunk(ctx, chunk, chunkExisting)
		if err != nil {
			return nil, err
		}
		for _, sp := range chunkSpans {
			key := spanKey{canonical: sp.CanonicalLabel, start: sp.Start, end: sp.End}
			if seen[key] {
				continue
			}
			seen[key] = true
			allSpans = append(allSpans, sp)
		}
	}
	return allSpans, nil
}

func filterSpansInChunk(spans []DetectedSpan, chunkStart, chunkEnd int) []DetectedSpan {
	if len(spans) == 0 {
		return nil
	}
	var out []DetectedSpan
	for _, s := range spans {
		if s.Start >= chunkStart && s.End <= chunkEnd {
			local := s
			local.Start -= chunkStart
			local.End -= chunkStart
			out = append(out, local)
		}
	}
	return out
}

func (d *LocalLLMDetector) detectChunk(ctx context.Context, chunk TextChunk, existingLocal []DetectedSpan) ([]DetectedSpan, error) {
	userPrompt := "请识别以下文本中的敏感实体，并返回 JSON：\n" + chunk.Text
	rawContent, err := d.complete(ctx, PrimaryLLMSystemPrompt, userPrompt, false)
	if err != nil {
		return nil, err
	}
	primaryEntities := ParseLLMContent(rawContent)
	spans := d.alignAndFilterEntities(chunk, primaryEntities, "local_llm_primary")

	if d.enableReviewPass && d.hasRemainingBudget(ctx) {
		combinedLocal := make([]DetectedSpan, 0, len(existingLocal)+len(spans))
		combinedLocal = append(combinedLocal, existingLocal...)
		for _, sp := range spans {
			local := sp
			local.Start -= chunk.ByteOffset
			local.End -= chunk.ByteOffset
			combinedLocal = append(combinedLocal, local)
		}
		reviewUserPrompt := buildFocusedReviewUserPrompt(chunk.Text, combinedLocal)
		reviewContent, reviewErr := d.complete(ctx, FocusedReviewLLMSystemPrompt, reviewUserPrompt, true)
		if reviewErr == nil && strings.TrimSpace(reviewContent) != "" {
			reviewEntities := ParseLLMContent(reviewContent)
			reviewSpans := d.alignAndFilterEntities(chunk, reviewEntities, "local_llm_review")
			spans = append(spans, reviewSpans...)
		}
	}
	return spans, nil
}

func (d *LocalLLMDetector) hasRemainingBudget(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) >= d.reviewMinRemaining
}

func buildFocusedReviewUserPrompt(chunkText string, existingLocal []DetectedSpan) string {
	var b strings.Builder
	if len(existingLocal) > 0 {
		b.WriteString("已识别实体：\n")
		seen := make(map[string]bool)
		for _, sp := range existingLocal {
			canon := sp.CanonicalLabel
			if canon == "" {
				canon = CanonicalizeLabel(sp.Label)
			}
			val := strings.TrimSpace(sp.Value)
			if val == "" && sp.Start >= 0 && sp.End <= len(chunkText) && sp.Start < sp.End {
				val = strings.TrimSpace(chunkText[sp.Start:sp.End])
			}
			if val == "" {
				continue
			}
			item := canon + "=" + val
			if seen[item] {
				continue
			}
			seen[item] = true
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	b.WriteString("请复核以下文本中遗漏的敏感实体，并返回 JSON：\n")
	b.WriteString(chunkText)
	return b.String()
}

func (d *LocalLLMDetector) complete(ctx context.Context, systemPrompt, userPrompt string, review bool) (string, error) {
	if d.completionFunc != nil {
		return d.completionFunc(ctx, systemPrompt, userPrompt, review)
	}
	if d.invoker == nil {
		return "", fail("redaction_unavailable")
	}
	reqPayload := map[string]any{
		"temperature":          0,
		"stream":               false,
		"response_format":      map[string]any{"type": "json_object"},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"messages": []any{
			map[string]any{"role": "system", "content": systemPrompt},
			map[string]any{"role": "user", "content": userPrompt},
		},
	}
	if d.modelID != "" {
		reqPayload["model"] = d.modelID
	}
	if review {
		reqPayload["max_tokens"] = 1800
	} else {
		reqPayload["max_tokens"] = PrivacyMaxOutputTokens
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fail("redaction_specification_failed")
	}
	var content string
	consumed := 0
	err = d.invoker.Invoke(ctx, routerruntime.Redaction, body, func(resp *http.Response) error {
		consumed++
		if consumed != 1 || resp == nil || resp.Body == nil || resp.StatusCode != http.StatusOK {
			return fail("redaction_extraction_failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if readErr != nil || len(raw) > 1<<20 {
			return fail("redaction_invalid_result")
		}
		if usage := reportedUsageFromBody(raw); usage != nil {
			routerruntime.PublishGenerationUsage(ctx, usage)
		}
		content = extractCompletionText(raw)
		return nil
	})
	if ctxErr := contextError(ctx); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil || consumed != 1 {
		var failure *Failure
		if errors.As(err, &failure) {
			return "", fail(SafeFailureCode(failure.Code))
		}
		return "", fail("redaction_extraction_failed")
	}
	return content, nil
}

func extractCompletionText(raw []byte) string {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Choices) > 0 {
		switch v := envelope.Choices[0].Message.Content.(type) {
		case string:
			return v
		case []any:
			var b strings.Builder
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						b.WriteString(t)
					}
				}
			}
			return b.String()
		}
	}
	return string(raw)
}

var (
	llmThinkBlockRe = regexp.MustCompile(`(?is)<think>.*?</think>`)
	llmCodeFenceRe  = regexp.MustCompile(`(?is)^` + "```" + `(?:json)?\s*(.*?)\s*` + "```" + `$`)
	llmObjectRe     = regexp.MustCompile(`(?s)\{[^{}]*\}`)
	llmStringField  = regexp.MustCompile(`"(label|type|category|value|text|entity)"\s*:\s*"((?:\\.|[^"\\])*)"`)
	llmNumField     = regexp.MustCompile(`"(confidence|score|start|end)"\s*:\s*(-?\d+(?:\.\d+)?)`)
)

// ParseLLMContent tolerantly parses JSON or semi-structured entity lists from LLM responses.
func ParseLLMContent(raw string) []LLMRawEntity {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return nil
	}
	// If passed a full OpenAI response JSON, unwrap message.content first.
	if strings.HasPrefix(cleaned, "{") && strings.Contains(cleaned, `"choices"`) {
		if unwrapped := extractCompletionText([]byte(cleaned)); unwrapped != "" && unwrapped != cleaned {
			cleaned = strings.TrimSpace(unwrapped)
		}
	}
	cleaned = strings.TrimSpace(llmThinkBlockRe.ReplaceAllString(cleaned, ""))
	if m := llmCodeFenceRe.FindStringSubmatch(cleaned); len(m) == 2 {
		cleaned = strings.TrimSpace(m[1])
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimPrefix(cleaned, "```JSON")
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")
		cleaned = strings.TrimSpace(cleaned)
	}

	if entities, ok := tryUnmarshalLLMEntities(cleaned); ok {
		return entities
	}
	// Try extracting JSON substring from first '{' or '[' to last '}' or ']'.
	firstBrace := strings.IndexAny(cleaned, "{[")
	lastBrace := strings.LastIndexAny(cleaned, "}]")
	if firstBrace >= 0 && lastBrace > firstBrace {
		if entities, ok := tryUnmarshalLLMEntities(cleaned[firstBrace : lastBrace+1]); ok {
			return entities
		}
	}

	// Fallback: regex scan over individual {...} object blocks.
	var out []LLMRawEntity
	for _, block := range llmObjectRe.FindAllString(cleaned, -1) {
		var label, value string
		conf := 0.85
		var startPtr, endPtr *int
		for _, sm := range llmStringField.FindAllStringSubmatch(block, -1) {
			key := sm[1]
			val := unescapeJSONString(sm[2])
			switch key {
			case "label", "type", "category":
				if label == "" {
					label = val
				}
			case "value", "text", "entity":
				if value == "" {
					value = val
				}
			}
		}
		for _, nm := range llmNumField.FindAllStringSubmatch(block, -1) {
			key := nm[1]
			var num float64
			if json.Unmarshal([]byte(nm[2]), &num) != nil {
				continue
			}
			switch key {
			case "confidence", "score":
				conf = clampConfidence(num)
			case "start":
				v := int(num)
				startPtr = &v
			case "end":
				v := int(num)
				endPtr = &v
			}
		}
		if label != "" && value != "" {
			out = append(out, LLMRawEntity{
				Label:      label,
				Value:      value,
				Confidence: conf,
				Start:      startPtr,
				End:        endPtr,
			})
		}
	}
	return out
}

func unescapeJSONString(raw string) string {
	var out string
	if json.Unmarshal([]byte(`"`+raw+`"`), &out) == nil {
		return out
	}
	return raw
}

func clampConfidence(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0.85
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func tryUnmarshalLLMEntities(input string) ([]LLMRawEntity, bool) {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, false
	}
	switch v := root.(type) {
	case []any:
		return parseLLMEntitySlice(v), true
	case map[string]any:
		if list, ok := v["entities"].([]any); ok {
			return parseLLMEntitySlice(list), true
		}
		if list, ok := v["findings"].([]any); ok {
			return parseLLMEntitySlice(list), true
		}
		if fields, ok := v["fields"].([]any); ok {
			var combined []LLMRawEntity
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					if list, ok := fm["findings"].([]any); ok {
						combined = append(combined, parseLLMEntitySlice(list)...)
					}
					if list, ok := fm["entities"].([]any); ok {
						combined = append(combined, parseLLMEntitySlice(list)...)
					}
				}
			}
			return combined, true
		}
	}
	return nil, false
}

func parseLLMEntitySlice(items []any) []LLMRawEntity {
	var out []LLMRawEntity
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := firstString(m, "label", "type", "category")
		value := firstString(m, "value", "text", "entity")
		if label == "" || value == "" {
			continue
		}
		conf := 0.85
		if c, ok := firstFloat(m, "confidence", "score"); ok {
			conf = clampConfidence(c)
		}
		var startPtr, endPtr *int
		if s, ok := firstInt(m, "start"); ok {
			startPtr = &s
		}
		if e, ok := firstInt(m, "end"); ok {
			endPtr = &e
		}
		out = append(out, LLMRawEntity{
			Label:      label,
			Value:      value,
			Confidence: conf,
			Start:      startPtr,
			End:        endPtr,
		})
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstFloat(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		case float64:
			return v, true
		}
	}
	return 0, false
}

func firstInt(m map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return int(i), true
			}
		case float64:
			return int(v), true
		}
	}
	return 0, false
}

var existingPlaceholderRe = regexp.MustCompile(`<[^<>\n]+:\d+>|\[\[PG_[^\n]*?\]\]|\{MASKED_[^{}\n]+\}`)

// ResolveEntitySpans aligns an LLM-extracted entity value to exact UTF-8 byte [start, end) offsets within chunkText.
func ResolveEntitySpans(chunkText string, item LLMRawEntity) [][2]int {
	val := strings.TrimSpace(item.Value)
	if val == "" {
		return nil
	}
	placeholderSpans := existingPlaceholderRe.FindAllStringIndex(chunkText, -1)
	overlapsPlaceholder := func(start, end int) bool {
		for _, mask := range placeholderSpans {
			if start < mask[1] && end > mask[0] {
				return true
			}
		}
		return false
	}

	// 1. Check explicit coordinates if supplied by the model.
	if item.Start != nil && item.End != nil && *item.Start >= 0 && *item.End > *item.Start {
		s, e := *item.Start, *item.End
		// Check as UTF-8 byte offsets first.
		if e <= len(chunkText) && chunkText[s:e] == val && !overlapsPlaceholder(s, e) && validLLMASCIISpanBoundary(chunkText, val, s, e) {
			return [][2]int{{s, e}}
		}
		// Check as Unicode rune offsets.
		runes, runeToByte := buildRuneToByteIndex(chunkText)
		if e <= len(runes) && string(runes[s:e]) == val {
			bs, be := runeToByte[s], runeToByte[e]
			if !overlapsPlaceholder(bs, be) && validLLMASCIISpanBoundary(chunkText, val, bs, be) {
				return [][2]int{{bs, be}}
			}
		}
	}

	// 2. Exact substring search via strings.Index.
	var spans [][2]int
	for offset := 0; offset < len(chunkText); {
		idx := strings.Index(chunkText[offset:], val)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(val)
		if !overlapsPlaceholder(start, end) && validLLMASCIISpanBoundary(chunkText, val, start, end) {
			spans = append(spans, [2]int{start, end})
		}
		offset = start + len(val)
	}
	if len(spans) > 0 {
		return spans
	}

	// 3. Relaxed whitespace regex alignment when LLM omitted or normalized spaces.
	relaxed := buildRelaxedWhitespaceRegex(val)
	if relaxed != nil {
		for _, loc := range relaxed.FindAllStringIndex(chunkText, -1) {
			start, end := loc[0], loc[1]
			matched := chunkText[start:end]
			if !overlapsPlaceholder(start, end) && validLLMASCIISpanBoundary(chunkText, matched, start, end) {
				spans = append(spans, [2]int{start, end})
			}
		}
	}
	return spans
}

func validLLMASCIISpanBoundary(text, val string, start, end int) bool {
	if val == "" {
		return false
	}
	first := val[0]
	last := val[len(val)-1]
	if isASCIIWordByte(first) && start > 0 && isASCIIWordByte(text[start-1]) {
		return false
	}
	if isASCIIWordByte(last) && end < len(text) && isASCIIWordByte(text[end]) {
		return false
	}
	return true
}

func isASCIIWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func buildRelaxedWhitespaceRegex(val string) *regexp.Regexp {
	runes := []rune(strings.TrimSpace(val))
	if len(runes) < 2 || len(runes) > 128 {
		return nil
	}
	var b bytes.Buffer
	prevSpace := false
	for i, r := range runes {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteString(`\s*`)
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteString(regexp.QuoteMeta(string(r)))
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			// Allow optional whitespace between CJK and ASCII or punctuation tokens.
			if (r > 127) != (runes[i+1] > 127) {
				b.WriteString(`\s*`)
			}
		}
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

var (
	nonSensitiveDatePattern = regexp.MustCompile(`^(?:(?:19|20)\d{2}\s*年?|(?:19|20)\d{2}[-/.年](?:0?[1-9]|1[0-2])月?|(?:0?[1-9]|1[0-2])月(?:0?[1-9]|[12]\d|3[01])日?|今天|昨天|明天|前天|后天|本周|上周|下周|本月|上月|下月|上个月|这个月|下个月|今年|去年|明年|年初|年中|年底|月末|月初|季度|第[一二三四1-4]季度|Q[1-4]|q[1-4]|春季|夏季|秋季|冬季|\d+\s*(?:年|个月|周|天|日|小时|分钟|秒)内?)$`)
	amountCurrencyMarkerRe  = regexp.MustCompile(`(?i)(?:[￥¥$€£]|人民币|美元|欧元|英镑|港元|港币|日元|元|块|万|亿|RMB|CNY|USD|HKD|EUR|JPY|GBP)`)
	amountDigitOrCNNumRe    = regexp.MustCompile(`[0-9零〇一二两三四五六七八九十百千万亿壹贰叁肆伍陆柒捌玖拾佰仟]`)
	asciiIdentifierShapeRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

var genericSensitiveValueStopwords = map[string]bool{
	"密码": true, "口令": true, "账号": true, "账户": true, "用户名": true,
	"客户": true, "公司": true, "企业": true, "集团": true, "项目": true,
	"接口": true, "密钥": true, "身份证": true, "身份证号": true, "手机号": true,
	"手机": true, "电话": true, "联系电话": true, "邮箱": true, "电子邮箱": true,
	"地址": true, "住址": true, "姓名": true, "联系人": true, "银行卡": true,
	"银行卡号": true, "金额": true, "余额": true, "工资": true, "薪资": true,
	"合同": true, "工单": true, "车牌": true, "车牌号": true, "微信号": true,
	"微信": true, "验证码": true, "客户端": true, "董事会": true, "公告": true,
	"password": true, "passwd": true, "pwd": true, "token": true,
	"secret": true, "api_key": true, "apikey": true, "username": true,
	"account": true, "client": true, "company": true, "project": true,
	"interface": true, "phone": true, "email": true, "address": true,
}

var codeIdentifierStopwords = map[string]bool{
	"data": true, "result": true, "results": true, "response": true, "request": true,
	"config": true, "configuration": true, "user": true, "users": true,
	"user_id": true, "userid": true, "admin": true, "root": true,
	"localhost": true, "null": true, "none": true, "nil": true,
	"true": true, "false": true, "string": true, "number": true,
	"boolean": true, "object": true, "array": true, "function": true,
	"class": true, "return": true, "import": true, "export": true,
	"default": true, "value": true, "values": true, "index": true,
	"item": true, "items": true, "list": true, "map": true,
	"key": true, "keys": true, "val": true, "err": true, "error": true,
	"ctx": true, "context": true, "req": true, "res": true, "resp": true,
	"msg": true, "message": true, "messages": true, "info": true,
	"debug": true, "warn": true, "warning": true, "status": true,
	"code": true, "type": true, "id": true, "name": true, "text": true,
	"body": true, "header": true, "headers": true, "params": true,
	"query": true, "path": true, "url": true, "uri": true, "host": true,
	"port": true, "db": true, "database": true, "table": true,
	"column": true, "row": true, "field": true, "record": true,
	"model": true, "service": true, "controller": true, "handler": true,
	"router": true, "server": true, "test": true, "mock": true,
	"demo": true, "example": true, "sample": true,
}

var personExcludedSuffixes = []string{
	"公司", "集团", "银行", "证券", "基金", "保险", "科技", "技术", "股份",
	"医药", "医院", "学校", "大学", "政府", "委员会", "中心", "研究院",
}

// ShouldKeepLLMEntity applies post-extraction heuristic filters to suppress false positives from local LLM output.
func ShouldKeepLLMEntity(rawLabel, value string) bool {
	normLabel := NormalizeLabel(rawLabel)
	canonLabel := CanonicalizeLabel(rawLabel)
	if LLMIgnoredLabels[normLabel] || LLMIgnoredLabels[canonLabel] {
		return false
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || utf8.RuneCountInString(trimmed) < 2 {
		return false
	}
	if existingPlaceholderRe.MatchString(trimmed) {
		return false
	}
	if nonSensitiveDatePattern.MatchString(trimmed) {
		return false
	}
	lower := strings.ToLower(trimmed)
	if genericSensitiveValueStopwords[trimmed] || genericSensitiveValueStopwords[lower] || GenericOrganizationValues[trimmed] {
		return false
	}
	if canonLabel == "NAME" || canonLabel == "ORG" || canonLabel == "HOSTNAME" {
		if codeIdentifierStopwords[lower] {
			return false
		}
		if strings.Contains(trimmed, "_") && asciiIdentifierShapeRe.MatchString(trimmed) {
			return false
		}
	}
	if canonLabel == "NAME" {
		for _, r := range trimmed {
			if unicode.IsDigit(r) || r == '\n' || r == '\r' || r == '\t' {
				return false
			}
		}
		for _, suffix := range personExcludedSuffixes {
			if strings.HasSuffix(trimmed, suffix) {
				return false
			}
		}
		// Reject camelCase code identifiers mistaken for English names.
		if asciiIdentifierShapeRe.MatchString(trimmed) && !strings.Contains(trimmed, " ") && hasInternalUpperCase(trimmed) {
			return false
		}
	}
	if canonLabel == "AMOUNT" {
		if strings.ContainsAny(trimmed, "%％") {
			return false
		}
		if !amountDigitOrCNNumRe.MatchString(trimmed) || !amountCurrencyMarkerRe.MatchString(trimmed) {
			return false
		}
	}
	return true
}

func hasInternalUpperCase(s string) bool {
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func (d *LocalLLMDetector) alignAndFilterEntities(chunk TextChunk, rawEntities []LLMRawEntity, detectorName string) []DetectedSpan {
	var out []DetectedSpan
	for _, item := range rawEntities {
		spans := ResolveEntitySpans(chunk.Text, item)
		for _, sp := range spans {
			matchedText := chunk.Text[sp[0]:sp[1]]
			if !ShouldKeepLLMEntity(item.Label, matchedText) {
				continue
			}
			rawLabel := NormalizeLabel(item.Label)
			canon := CanonicalizeLabel(rawLabel)
			conf := item.Confidence
			if conf <= 0 {
				conf = 0.85
			}
			out = append(out, DetectedSpan{
				Label:          rawLabel,
				CanonicalLabel: canon,
				Start:          chunk.ByteOffset + sp[0],
				End:            chunk.ByteOffset + sp[1],
				Value:          matchedText,
				Confidence:     conf,
				Source:         "local_llm",
				RuleName:       detectorName,
			})
		}
	}
	return out
}
