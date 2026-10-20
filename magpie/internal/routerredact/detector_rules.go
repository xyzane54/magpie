package routerredact

import (
	_ "embed"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

//go:embed privacy_assets/default.yaml
var defaultPolicyYAML []byte

//go:embed privacy_assets/rules.zh.yaml
var rulesZhYAML []byte

// DetectedSpan 表示单个检测器识别出的敏感文本区间。
type DetectedSpan struct {
	Label          string
	CanonicalLabel string
	Start          int
	End            int
	Value          string
	Confidence     float64
	Source         string
	RuleName       string
}

// RuleDefinition 描述单条正则检测规则配置。
type RuleDefinition struct {
	Name            string   `yaml:"name"`
	Label           string   `yaml:"label"`
	Pattern         string   `yaml:"pattern"`
	Flags           []string `yaml:"flags"`
	Score           float64  `yaml:"score"`
	Validator       string   `yaml:"validator"`
	RedactGroup     any      `yaml:"redact_group"`
	ContextKeywords []string `yaml:"context_keywords"`
	ExcludeValues   []string `yaml:"exclude_values"`
	WordBoundary    bool     `yaml:"word_boundary"`
	DigitBoundary   bool     `yaml:"digit_boundary"`
}

type compiledRule struct {
	def             RuleDefinition
	label           string
	canonicalLabel  string
	re              *regexp2.Regexp
	score           float64
	validator       func(string) bool
	redactGroupNum  int
	redactGroupName string
	contextKeywords []string
	excludeSet      map[string]bool
}

// GenericOrganizationValues 定义不应当被单独作为 ORG 脱敏的泛称词。
var GenericOrganizationValues = map[string]bool{
	"公司":     true,
	"本公司":    true,
	"该公司":    true,
	"集团":     true,
	"本集团":    true,
	"该集团":    true,
	"银行":     true,
	"证券公司":   true,
	"基金管理公司": true,
	"客户":     true,
	"客户方":    true,
	"供应商":    true,
	"项目":     true,
	"董事会":    true,
	"公告":     true,
}

var defaultRuleDefinitions = []RuleDefinition{
	{
		Name:          "cn_phone",
		Label:         "PHONE",
		Pattern:       `(?<!\d)(?:\+?86[-\s]?)?1[3-9]\d(?:[-\s]?\d){8}(?!\d)`,
		Score:         0.98,
		DigitBoundary: true,
	},
	{
		Name:    "landline",
		Label:   "PHONE",
		Pattern: `(?<!\d)0\d{2,3}[-\s]?\d{7,8}(?!\d)`,
		Score:   0.96,
	},
	{
		Name:    "email",
		Label:   "EMAIL",
		Pattern: `(?<![A-Za-z0-9._%+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,63}(?![A-Za-z0-9.-])`,
		Flags:   []string{"I"},
		Score:   0.99,
	},
	{
		Name:          "id_card",
		Label:         "ID_CARD",
		Pattern:       `(?<!\d)\d{17}[0-9Xx](?!\w)`,
		Score:         0.999,
		Validator:     "id_card",
		DigitBoundary: true,
	},
	{
		Name:          "bank_card",
		Label:         "BANK_CARD",
		Pattern:       `(?<!\d)(?:\d[ -]?){12,18}\d(?!\d)`,
		Score:         0.98,
		Validator:     "luhn",
		DigitBoundary: true,
	},
	{
		Name:        "bank_account_context",
		Label:       "BANK_CARD",
		Pattern:     `(?:银行账号|银行账户|收款账号|结算账户|对公账户)\s*[:=：]?\s*["']?(?P<value>\d(?:[ -]?\d){5,31})["']?`,
		Score:       0.96,
		RedactGroup: "value",
	},
	{
		Name:    "ipv4",
		Label:   "IP",
		Pattern: `(?<!\d)(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?!\d)`,
		Score:   0.95,
	},
	{
		Name:    "ipv6",
		Label:   "IP",
		Pattern: `(?<![A-Fa-f0-9:])(?:[A-Fa-f0-9]{1,4}:){7}[A-Fa-f0-9]{1,4}(?![A-Fa-f0-9:])`,
		Score:   0.95,
	},
	{
		Name:    "mac_address",
		Label:   "MAC",
		Pattern: `(?<![A-Fa-f0-9:-])(?:[A-Fa-f0-9]{2}[:-]){5}[A-Fa-f0-9]{2}(?![A-Fa-f0-9:-])`,
		Score:   0.96,
	},
	{
		Name:    "internal_domain",
		Label:   "HOSTNAME",
		Pattern: `(?<![A-Za-z0-9.-])(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:internal|local|corp|lan|intranet|priv|svc\.cluster\.local)(?![A-Za-z0-9.-])`,
		Flags:   []string{"I"},
		Score:   0.94,
	},
	{
		Name:    "jwt",
		Label:   "JWT",
		Pattern: `(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?![A-Za-z0-9_-])`,
		Score:   0.98,
	},
	{
		Name:    "private_key_block",
		Label:   "PRIVATE_KEY",
		Pattern: `-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`,
		Score:   0.999,
	},
	{
		Name:    "connection_string",
		Label:   "CONNECTION_STRING",
		Pattern: `(?i)\b(?:https?|postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?)://[^\s/:@]+:[^\s/@]+@[^\s"'<>，,。；;！）)】\]]+`,
		Score:   0.995,
	},
	{
		Name:    "openai_style_key",
		Label:   "API_KEY",
		Pattern: `(?<![A-Za-z0-9])sk-[A-Za-z0-9_-]{16,}(?![A-Za-z0-9])`,
		Score:   0.995,
	},
	{
		Name:    "aws_access_key",
		Label:   "API_KEY",
		Pattern: `(?<![A-Z0-9])(?:AKIA|ASIA)[A-Z0-9]{16}(?![A-Z0-9])`,
		Score:   0.995,
	},
	{
		Name:        "secret_assignment",
		Label:       "SECRET",
		Pattern:     `(?i)(?:(?:api[_-]?key|access[_-]?token|client[_-]?secret|password|passwd|密码|口令|访问令牌|令牌|密钥)\s*[:=：]\s*)["']?(?P<value>[^\s,，;；"']{6,256})["']?`,
		Score:       0.94,
		RedactGroup: "value",
	},
	{
		Name:        "bearer_token",
		Label:       "TOKEN",
		Pattern:     `(?i)(?<![A-Za-z0-9])Bearer\s+(?P<value>[A-Za-z0-9._~+/-]{12,}=*)`,
		Score:       0.98,
		RedactGroup: "value",
	},
	{
		Name:        "cookie_header",
		Label:       "COOKIE",
		Pattern:     `(?i)(?:Set-Cookie|Cookie)\s*:\s*(?P<value>[^\r\n]+)`,
		Score:       0.96,
		RedactGroup: "value",
	},
	{
		Name:    "case_number",
		Label:   "CASE_NUMBER",
		Pattern: `[（(]\d{4}[）)][\u4e00-\u9fffA-Za-z0-9]{1,16}\d{1,10}号`,
		Score:   0.98,
	},
	{
		Name:    "plate_number",
		Label:   "PLATE_NUMBER",
		Pattern: `(?<![\u4e00-\u9fffA-Za-z0-9])[京津沪渝冀豫云辽黑湘皖鲁新苏浙赣鄂桂甘晋蒙陕吉闽贵粤青藏川宁琼][A-HJ-NP-Z][A-HJ-NP-Z0-9]{4,5}[A-HJ-NP-Z0-9挂学警港澳](?![A-Za-z0-9])`,
		Score:   0.96,
	},
	{
		Name:        "wechat_account",
		Label:       "WECHAT",
		Pattern:     `(?i)(?:微信号|微信|wechat)\s*(?:为|是|[:=：])\s*["']?(?P<value>[a-zA-Z][-_a-zA-Z0-9]{5,19})["']?`,
		Score:       0.95,
		RedactGroup: "value",
	},
	{
		Name:        "qq_account",
		Label:       "QQ",
		Pattern:     `(?i)(?:QQ号?|qq)\s*(?:为|是|[:=：])\s*["']?(?P<value>[1-9]\d{4,11})["']?`,
		Score:       0.95,
		RedactGroup: "value",
	},
}

// RuleDetector 基于 PCRE2 (regexp2) 声明式规则与校验器执行敏感信息识别。
type RuleDetector struct {
	rules []*compiledRule
}

var (
	defaultRuleDetectorOnce sync.Once
	defaultRuleDetectorInst *RuleDetector
)

// DefaultRuleDetector 返回加载了内置规则与 privacy_assets/rules.zh.yaml 的单例规则检测器。
func DefaultRuleDetector() *RuleDetector {
	defaultRuleDetectorOnce.Do(func() {
		defaultRuleDetectorInst = NewRuleDetectorFromAssets()
	})
	return defaultRuleDetectorInst
}

// NewRuleDetectorFromAssets 从内嵌的 default.yaml 与 rules.zh.yaml 构建规则检测器。
func NewRuleDetectorFromAssets() *RuleDetector {
	merged := make([]RuleDefinition, 0, len(defaultRuleDefinitions)+64)
	indexByName := make(map[string]int, len(defaultRuleDefinitions)+64)

	addOrReplace := func(def RuleDefinition) {
		name := strings.TrimSpace(def.Name)
		if name == "" || strings.TrimSpace(def.Pattern) == "" || strings.TrimSpace(def.Label) == "" {
			return
		}
		if idx, exists := indexByName[name]; exists {
			merged[idx] = def
			return
		}
		indexByName[name] = len(merged)
		merged = append(merged, def)
	}

	for _, def := range defaultRuleDefinitions {
		addOrReplace(def)
	}
	for _, def := range parseRuleDefinitionsYAML(defaultPolicyYAML) {
		addOrReplace(def)
	}
	for _, def := range parseRuleDefinitionsYAML(rulesZhYAML) {
		addOrReplace(def)
	}

	return NewRuleDetector(merged)
}

// NewRuleDetector 根据给定的规则定义列表编译生成 RuleDetector。
func NewRuleDetector(defs []RuleDefinition) *RuleDetector {
	compiled := make([]*compiledRule, 0, len(defs))
	for _, def := range defs {
		cr, err := compileRuleDefinition(def)
		if err != nil || cr == nil {
			continue
		}
		compiled = append(compiled, cr)
	}
	return &RuleDetector{rules: compiled}
}

// Rules 返回已编译规则的原始定义列表。
func (d *RuleDetector) Rules() []RuleDefinition {
	if d == nil {
		return nil
	}
	out := make([]RuleDefinition, 0, len(d.rules))
	for _, r := range d.rules {
		out = append(out, r.def)
	}
	return out
}

func parseRuleDefinitionsYAML(data []byte) []RuleDefinition {
	if len(data) == 0 {
		return nil
	}
	var wrapper struct {
		Rules []RuleDefinition `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err == nil && len(wrapper.Rules) > 0 {
		return wrapper.Rules
	}
	var list []RuleDefinition
	if err := yaml.Unmarshal(data, &list); err == nil && len(list) > 0 {
		return list
	}
	return nil
}

func compileRuleDefinition(def RuleDefinition) (*compiledRule, error) {
	opts := regexp2.RegexOptions(regexp2.Multiline)
	for _, f := range def.Flags {
		switch strings.ToUpper(strings.TrimSpace(f)) {
		case "I", "IGNORECASE":
			opts |= regexp2.IgnoreCase
		case "S", "DOTALL", "SINGLELINE":
			opts |= regexp2.Singleline
		case "M", "MULTILINE":
			opts |= regexp2.Multiline
		case "X", "VERBOSE":
			opts |= regexp2.IgnorePatternWhitespace
		}
	}

	pattern := strings.ReplaceAll(def.Pattern, "(?P<", "(?<")
	re, err := regexp2.Compile(pattern, opts)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = 500 * time.Millisecond

	score := def.Score
	if score <= 0 {
		score = 0.9
	}

	label := strings.TrimSpace(def.Label)
	if def.Name == "passport_number" {
		label = "PASSPORT"
	} else if def.Name == "birth_date" {
		label = "BIRTH_DATE"
	}
	canonical := CanonicalizeLabel(label)

	var groupNum int
	var groupName string
	switch v := def.RedactGroup.(type) {
	case int:
		groupNum = v
	case int64:
		groupNum = int(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed != "" && trimmed != "0" {
			if n, err := strconv.Atoi(trimmed); err == nil {
				groupNum = n
			} else {
				groupName = trimmed
			}
		}
	}

	keywords := make([]string, 0, len(def.ContextKeywords))
	for _, kw := range def.ContextKeywords {
		if k := strings.ToLower(strings.TrimSpace(kw)); k != "" {
			keywords = append(keywords, k)
		}
	}

	excludeSet := make(map[string]bool, len(def.ExcludeValues))
	for _, ex := range def.ExcludeValues {
		if trimmed := strings.TrimSpace(ex); trimmed != "" {
			excludeSet[trimmed] = true
		}
	}

	return &compiledRule{
		def:             def,
		label:           label,
		canonicalLabel:  canonical,
		re:              re,
		score:           score,
		validator:       resolveValidator(def.Validator),
		redactGroupNum:  groupNum,
		redactGroupName: groupName,
		contextKeywords: keywords,
		excludeSet:      excludeSet,
	}, nil
}

// Detect 在文本中运行全部正则规则，返回 UTF-8 字节偏移量对齐的候选实体列表。
func (d *RuleDetector) Detect(text string) []DetectedSpan {
	if d == nil || text == "" {
		return nil
	}
	runes, runeToByte := buildRuneToByteIndex(text)
	if len(runes) == 0 {
		return nil
	}

	var out []DetectedSpan
	for _, rule := range d.rules {
		if AllowedPassThroughLabels[rule.canonicalLabel] {
			continue
		}
		match, err := rule.re.FindRunesMatch(runes)
		for err == nil && match != nil {
			targetGroup := selectMatchGroup(match, rule.redactGroupNum, rule.redactGroupName)
			if targetGroup != nil && targetGroup.Length > 0 {
				runeStart := targetGroup.Index
				runeEnd := targetGroup.Index + targetGroup.Length
				if runeStart >= 0 && runeEnd > runeStart && runeEnd <= len(runes) {
					byteStart := runeToByte[runeStart]
					byteEnd := runeToByte[runeEnd]
					val := text[byteStart:byteEnd]
					trimmedVal := strings.TrimSpace(val)

					if d.acceptMatch(rule, runes, runeStart, runeEnd, text, byteStart, byteEnd, val, trimmedVal) {
						out = append(out, DetectedSpan{
							Label:          rule.label,
							CanonicalLabel: rule.canonicalLabel,
							Start:          byteStart,
							End:            byteEnd,
							Value:          val,
							Confidence:     rule.score,
							Source:         "rule",
							RuleName:       rule.def.Name,
						})
					}
				}
			}
			match, err = rule.re.FindNextMatch(match)
		}
	}
	return out
}

func (d *RuleDetector) acceptMatch(
	rule *compiledRule,
	runes []rune,
	runeStart, runeEnd int,
	text string,
	byteStart, byteEnd int,
	val, trimmedVal string,
) bool {
	if trimmedVal == "" {
		return false
	}
	if rule.excludeSet[trimmedVal] {
		return false
	}
	if rule.canonicalLabel == "ORG" && GenericOrganizationValues[trimmedVal] {
		return false
	}
	if rule.def.WordBoundary {
		if (runeStart > 0 && isASCIIWordRune(runes[runeStart-1])) ||
			(runeEnd < len(runes) && isASCIIWordRune(runes[runeEnd])) {
			return false
		}
	}
	if rule.def.DigitBoundary {
		if (runeStart > 0 && isDigitRune(runes[runeStart-1])) ||
			(runeEnd < len(runes) && isDigitRune(runes[runeEnd])) {
			return false
		}
	}
	if len(rule.contextKeywords) > 0 {
		winStart := runeStart - 24
		if winStart < 0 {
			winStart = 0
		}
		winEnd := runeEnd + 24
		if winEnd > len(runes) {
			winEnd = len(runes)
		}
		windowLower := strings.ToLower(string(runes[winStart:winEnd]))
		matchedKeyword := false
		for _, kw := range rule.contextKeywords {
			if strings.Contains(windowLower, kw) {
				matchedKeyword = true
				break
			}
		}
		if !matchedKeyword {
			return false
		}
	}
	if rule.validator != nil && !rule.validator(trimmedVal) {
		return false
	}
	if rule.canonicalLabel == "AMOUNT" {
		if monetarySpanContinues(runes, runeEnd) {
			return false
		}
	}
	_ = text
	_ = byteStart
	_ = byteEnd
	return true
}

func selectMatchGroup(m *regexp2.Match, groupNum int, groupName string) *regexp2.Group {
	if groupName != "" {
		if g := m.GroupByName(groupName); g != nil && len(g.Captures) > 0 {
			return g
		}
	}
	if groupNum > 0 {
		if g := m.GroupByNumber(groupNum); g != nil && len(g.Captures) > 0 {
			return g
		}
	}
	return &m.Group
}

func buildRuneToByteIndex(text string) ([]rune, []int) {
	runes := []rune(text)
	runeToByte := make([]int, len(runes)+1)
	byteIdx := 0
	for i, r := range runes {
		runeToByte[i] = byteIdx
		byteIdx += utf8.RuneLen(r)
	}
	runeToByte[len(runes)] = len(text)
	return runes, runeToByte
}

func isASCIIWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func isDigitRune(r rune) bool {
	return r >= '0' && r <= '9'
}

func monetarySpanContinues(runes []rune, runeEnd int) bool {
	i := runeEnd
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\u3000') {
		i++
	}
	if i >= len(runes) {
		return false
	}
	if runes[i] == '%' || runes[i] == '％' {
		return true
	}
	if i == runeEnd {
		r := runes[i]
		if isDigitRune(r) || r == '万' || r == '亿' || r == '千' || r == '百' || r == '十' || r == '元' {
			return true
		}
	}
	return false
}

func resolveValidator(name string) func(string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "id_card", "china_id_checksum":
		return validateChinaIDCard
	case "luhn":
		return validateLuhn
	case "uscc_checksum":
		return validateUSCC
	default:
		return nil
	}
}

var idCardWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
var idCardCheckDigits = "10X98765432"

func validateChinaIDCard(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if len(normalized) != 18 {
		return false
	}
	if normalized[:2] == "00" {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		ch := normalized[i]
		if ch < '0' || ch > '9' {
			return false
		}
		sum += int(ch-'0') * idCardWeights[i]
	}
	last := normalized[17]
	if !((last >= '0' && last <= '9') || last == 'X') {
		return false
	}
	// 校验出生日期合法性 (YYYYMMDD: 1900-01-01 ~ 当前年份)
	birthStr := normalized[6:14]
	birthTime, err := time.Parse("20060102", birthStr)
	if err != nil {
		return false
	}
	if birthTime.Year() < 1900 || birthTime.After(time.Now().Add(24*time.Hour)) {
		return false
	}
	expected := idCardCheckDigits[sum%11]
	return last == expected
}

func validateLuhn(value string) bool {
	digits := make([]int, 0, len(value))
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch >= '0' && ch <= '9' {
			digits = append(digits, int(ch-'0'))
		} else if ch != ' ' && ch != '-' {
			return false
		}
	}
	if len(digits) < 12 || len(digits) > 19 {
		return false
	}
	allSame := true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}
	checksum := 0
	parity := len(digits) % 2
	for idx, d := range digits {
		if idx%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		checksum += d
	}
	return checksum%10 == 0
}

const usccCharset = "0123456789ABCDEFGHJKLMNPQRTUWXY"

var usccWeights = [17]int{1, 3, 9, 27, 19, 26, 16, 17, 20, 29, 25, 13, 8, 24, 10, 30, 28}

func validateUSCC(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if len(normalized) != 18 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		idx := strings.IndexByte(usccCharset, normalized[i])
		if idx < 0 {
			return false
		}
		sum += idx * usccWeights[i]
	}
	expectedIdx := (31 - (sum % 31)) % 31
	return normalized[17] == usccCharset[expectedIdx]
}
