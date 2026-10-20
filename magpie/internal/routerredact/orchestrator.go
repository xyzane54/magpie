package routerredact

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"
)

// Orchestrator 串联 RuleDetector、DictionaryDetector 与 LocalLLMDetector，
// 执行复合凭据子跨度抑制、相邻中文地址合并、重叠冲突消解与分类标签收敛。
type Orchestrator struct {
	rules    *RuleDetector
	dict     *DictionaryDetector
	llm      *LocalLLMDetector
	versions Versions
}

// NewOrchestrator 创建三层混合敏感信息检测编排器。
func NewOrchestrator(rules *RuleDetector, dict *DictionaryDetector, llm *LocalLLMDetector) *Orchestrator {
	if rules == nil {
		rules = DefaultRuleDetector()
	}
	if dict == nil {
		dict = DefaultDictionaryDetector()
	}
	return &Orchestrator{
		rules: rules,
		dict:  dict,
		llm:   llm,
	}
}

// DefaultOrchestrator 使用内置 YAML 规则与词典创建编排器。
func DefaultOrchestrator(llm *LocalLLMDetector) *Orchestrator {
	return NewOrchestrator(DefaultRuleDetector(), DefaultDictionaryDetector(), llm)
}

// WithVersions 绑定 Pipeline 所需的协议版本标识。
func (o *Orchestrator) WithVersions(v Versions) *Orchestrator {
	if o == nil {
		return nil
	}
	cp := *o
	cp.versions = v
	return &cp
}

// Available 实现 Extractor 接口的可用性检查。
func (o *Orchestrator) Available(ctx context.Context, versions Versions) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if o == nil {
		return fail("redaction_unavailable")
	}
	if (o.versions != Versions{}) && o.versions != versions {
		return fail("redaction_unavailable")
	}
	return nil
}

// Extract 实现 Extractor 接口，对输入文本字段逐个执行三层混合检测并返回去重消解后的实体列表。
func (o *Orchestrator) Extract(ctx context.Context, in Extraction) (Result, error) {
	if err := o.Available(ctx, in.Versions); err != nil {
		return Result{}, err
	}
	if len(in.Texts) > 8192 {
		return Result{}, fail("redaction_input_limit")
	}
	var allEntities []Entity
	for fieldIdx, text := range in.Texts {
		if err := contextError(ctx); err != nil {
			return Result{}, err
		}
		if text == "" {
			continue
		}
		entities, err := o.DetectEntities(ctx, fieldIdx, text)
		if err != nil {
			return Result{}, err
		}
		allEntities = append(allEntities, entities...)
		if len(allEntities) > maxEntities {
			return Result{}, fail("redaction_invalid_result")
		}
	}
	validated, err := validateEntities(in.Texts, Result{Complete: true, Entities: allEntities})
	if err != nil {
		return Result{}, err
	}
	return Result{Complete: true, Entities: validated}, nil
}

// DetectEntities 对单个字段文本执行完整检测并转换为 Pipeline 所需的 Entity 列表。
func (o *Orchestrator) DetectEntities(ctx context.Context, field int, text string) ([]Entity, error) {
	spans, err := o.Detect(ctx, text)
	if err != nil {
		return nil, err
	}
	entities := make([]Entity, 0, len(spans))
	for _, s := range spans {
		cat := s.CanonicalLabel
		if cat == "" {
			cat = CanonicalizeLabel(s.Label)
		}
		entities = append(entities, Entity{
			Field:    field,
			Start:    s.Start,
			End:      s.End,
			Category: cat,
			Text:     s.Value,
		})
	}
	return entities, nil
}

// Detect 对单段文本依次执行正则规则、敏感词典与本地 LLM 检测，并完成消解合并。
func (o *Orchestrator) Detect(ctx context.Context, text string) ([]DetectedSpan, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}

	var findings []DetectedSpan
	if o != nil && o.rules != nil {
		findings = append(findings, o.rules.Detect(text)...)
	}
	if o != nil && o.dict != nil {
		findings = append(findings, o.dict.Detect(text)...)
	}
	if o != nil && o.llm != nil {
		llmSpans, err := o.llm.Detect(ctx, text, findings)
		if err != nil {
			return nil, err
		}
		findings = append(findings, llmSpans...)
	}

	if len(findings) == 0 {
		return nil, nil
	}

	// 1. 统一补齐 CanonicalLabel 与 Value，丢弃越界跨度
	normalized := make([]DetectedSpan, 0, len(findings))
	for _, span := range findings {
		if span.Start < 0 || span.End <= span.Start || span.End > len(text) {
			continue
		}
		span.Value = text[span.Start:span.End]
		if span.CanonicalLabel == "" {
			span.CanonicalLabel = CanonicalizeLabel(span.Label)
		}
		if span.CanonicalLabel == "" {
			continue
		}
		normalized = append(normalized, span)
	}

	// 2. 抑制复合凭据（CONNECTION_STRING/API_KEY/SECRET/COOKIE/URL 等）内部被拆碎的子跨度（如内网 IP、端口、子路径）
	normalized = suppressCompoundSubspans(normalized)

	// 3. 合并相邻中文地址片段（如 "安徽省" + "合肥市高新区..."）
	normalized = mergeAdjacentLocations(text, normalized)

	// 4. 重叠冲突消解
	resolved := resolveOverlaps(normalized)

	// 5. 消解后再次检查相邻中文地址片段并合并
	resolved = mergeAdjacentLocations(text, resolved)

	// 6. 过滤掉 allow 策略标签（PUBLIC_INFORMATION、JOB_TITLE 等）并将 Label 统一设为 CanonicalLabel
	finalSpans := make([]DetectedSpan, 0, len(resolved))
	for _, span := range resolved {
		if AllowedPassThroughLabels[span.CanonicalLabel] || AllowedPassThroughLabels[span.Label] {
			continue
		}
		span.Label = span.CanonicalLabel
		finalSpans = append(finalSpans, span)
	}

	return finalSpans, nil
}

// suppressCompoundSubspans 当外层命中了复合机密标签（如 CONNECTION_STRING、API_KEY、SECRET、COOKIE、URL 等）时，
// 抑制完全落在该复合跨度内部的任何子跨度（例如连接串中的 IP、邮箱或密码片段）。
func suppressCompoundSubspans(findings []DetectedSpan) []DetectedSpan {
	var compounds []DetectedSpan
	for _, f := range findings {
		if CompoundSecretLabels[f.Label] || CompoundSecretLabels[f.CanonicalLabel] {
			compounds = append(compounds, f)
		}
	}
	if len(compounds) == 0 {
		return findings
	}

	out := make([]DetectedSpan, 0, len(findings))
	for _, f := range findings {
		suppressed := false
		for _, c := range compounds {
			if c.Start <= f.Start && f.End <= c.End {
				if c.Start != f.Start || c.End != f.End {
					suppressed = true
					break
				}
				if !CompoundSecretLabels[f.Label] && !CompoundSecretLabels[f.CanonicalLabel] {
					suppressed = true
					break
				}
			}
		}
		if !suppressed {
			out = append(out, f)
		}
	}
	return out
}

var chineseRegionSuffixes = []string{"省", "市", "区", "县", "州", "盟", "旗"}

func hasChineseRegionSuffix(val string) bool {
	trimmed := strings.TrimSpace(val)
	for _, suffix := range chineseRegionSuffixes {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	return false
}

// mergeAdjacentLocations 合并紧邻的中文地址片段（例如 "安徽省" + "合肥市高新区望江西路800号"）。
func mergeAdjacentLocations(text string, findings []DetectedSpan) []DetectedSpan {
	if len(findings) <= 1 {
		return findings
	}
	sorted := append([]DetectedSpan(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})

	merged := make([]DetectedSpan, 0, len(sorted))
	for _, curr := range sorted {
		if len(merged) == 0 {
			merged = append(merged, curr)
			continue
		}
		prev := &merged[len(merged)-1]
		if canMergeLocation(text, *prev, curr) {
			prev.End = curr.End
			prev.Value = text[prev.Start:curr.End]
			prev.Label = "ADDRESS"
			prev.CanonicalLabel = "ADDRESS"
			if curr.Confidence > prev.Confidence {
				prev.Confidence = curr.Confidence
			}
			if SourcePriority[curr.Source] > SourcePriority[prev.Source] {
				prev.Source = curr.Source
			}
			continue
		}
		merged = append(merged, curr)
	}
	return merged
}

func canMergeLocation(text string, prev, curr DetectedSpan) bool {
	if curr.Start < prev.End || curr.End > len(text) {
		return false
	}
	gap := text[prev.End:curr.Start]
	if strings.Trim(gap, " \t\r\n、") != "" {
		return false
	}
	combined := text[prev.Start:curr.End]
	if utf8.RuneCountInString(combined) > 80 {
		return false
	}
	prevCanon := prev.CanonicalLabel
	if prevCanon == "" {
		prevCanon = CanonicalizeLabel(prev.Label)
	}
	currCanon := curr.CanonicalLabel
	if currCanon == "" {
		currCanon = CanonicalizeLabel(curr.Label)
	}
	if prevCanon == "ADDRESS" && currCanon == "ADDRESS" {
		return true
	}
	if (prevCanon == "ORG" || prevCanon == "ADDRESS") && currCanon == "ADDRESS" && hasChineseRegionSuffix(prev.Value) {
		return true
	}
	return false
}

// resolveOverlaps 对所有候选跨度按起点升序、跨度长度降序排列后执行冲突消解。
func resolveOverlaps(findings []DetectedSpan) []DetectedSpan {
	if len(findings) <= 1 {
		return findings
	}
	sorted := append([]DetectedSpan(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return (sorted[i].End - sorted[i].Start) > (sorted[j].End - sorted[j].Start)
	})

	selected := make([]DetectedSpan, 0, len(sorted))
	for _, cand := range sorted {
		if len(selected) == 0 {
			selected = append(selected, cand)
			continue
		}
		curr := selected[len(selected)-1]
		if cand.Start >= curr.End {
			selected = append(selected, cand)
			continue
		}
		if preferCandidate(curr, cand) {
			selected[len(selected)-1] = cand
		}
	}
	return selected
}

func spanLabelPriority(span DetectedSpan) int {
	if span.CanonicalLabel == "PUBLIC_INFORMATION" || span.Label == "PUBLIC_INFORMATION" {
		return 100
	}
	if span.Label != "" {
		return GetLabelPriority(span.Label)
	}
	return GetLabelPriority(span.CanonicalLabel)
}

func preferCandidate(curr, cand DetectedSpan) bool {
	if cand.CanonicalLabel == "PUBLIC_INFORMATION" && curr.CanonicalLabel != "PUBLIC_INFORMATION" {
		return true
	}
	if curr.CanonicalLabel == "PUBLIC_INFORMATION" && cand.CanonicalLabel != "PUBLIC_INFORMATION" {
		return false
	}

	currLen := curr.End - curr.Start
	candLen := cand.End - cand.Start
	currPri := spanLabelPriority(curr)
	candPri := spanLabelPriority(cand)

	// 情形 1：curr 完全包含 cand 且 curr 更长
	if curr.Start <= cand.Start && cand.End <= curr.End && currLen > candLen {
		if cand.Source == "rule" && candPri >= 80 && candPri >= currPri+8 {
			return true
		}
		return false
	}

	// 情形 2：cand 完全包含 curr 且 cand 更长
	if cand.Start <= curr.Start && curr.End <= cand.End && candLen > currLen {
		if curr.Source == "rule" && currPri >= 80 && currPri >= candPri+8 {
			return false
		}
		return true
	}

	// 情形 3：部分重叠或完全相同跨度，按四元组 (SourcePriority, RuneLength, LabelPriority, Confidence) 比较
	currSourcePri := SourcePriority[curr.Source]
	candSourcePri := SourcePriority[cand.Source]
	if candSourcePri != currSourcePri {
		return candSourcePri > currSourcePri
	}

	currRunes := utf8.RuneCountInString(curr.Value)
	candRunes := utf8.RuneCountInString(cand.Value)
	if candRunes != currRunes {
		return candRunes > currRunes
	}

	if candPri != currPri {
		return candPri > currPri
	}

	return cand.Confidence > curr.Confidence
}
