package routerredact

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	approxPrefixRe = regexp.MustCompile(`^(?:约|大概|大约|左右|近|超|逾|将近|差不多|[~≈])\s*`)
	approxSuffixRe = regexp.MustCompile(`(?:左右|上下|余|多)\s*$`)
	dateLikeRe     = regexp.MustCompile(`^\d{2,4}[-/.年]\d{1,2}(?:[-/.月]\d{1,2}日?)?$`)
	percentLikeRe  = regexp.MustCompile(`(?:%|％|^百分之|^千分之)`)
	ratioLikeRe    = regexp.MustCompile(`^\d+(?:\.\d+)?\s*[:：/]\s*\d+(?:\.\d+)?$`)
	versionLikeRe  = regexp.MustCompile(`(?i)^v?\d+\.\d+(?:\.\d+)+$`)

	// 区间金额匹配，如 80-120万元、$1,000-$2,000、100~200元
	amountRangeRe = regexp.MustCompile(`^\s*(?P<prefix>(?:人民币|美元|美金|港币|港元|日元|欧元|英镑|新台币|RMB|CNY|USD|HKD|EUR|GBP|JPY|[¥￥$€£])\s*)?(?P<low>\d[\d,]*(?:\.\d+)?)\s*(?P<u1>[万亿千百萬億仟佰])?\s*[-~～至到—–]\s*(?P<p2>(?:人民币|美元|美金|港币|港元|日元|欧元|英镑|新台币|RMB|CNY|USD|HKD|EUR|GBP|JPY|[¥￥$€£])\s*)?(?P<high>\d[\d,]*(?:\.\d+)?)\s*(?P<u2>[万亿千百萬億仟佰])?\s*(?P<suffix>万元整|亿元整|元整|万元|亿元|千元|百元|万块|亿块|元|块|圆|刀|美金|美元|港币|港元|日元|欧元|英镑|RMB|CNY|USD|HKD|EUR|GBP|JPY)?\s*$`)

	// 单值阿拉伯数字金额匹配
	singleNumericAmountRe = regexp.MustCompile(`^\s*(?P<prefix>(?:人民币|美元|美金|港币|港元|日元|欧元|英镑|新台币|RMB|CNY|USD|HKD|EUR|GBP|JPY|[¥￥$€£])\s*)?(?P<num>\d[\d,]*(?:\.\d+)?)\s*(?P<scale>[万亿千百萬億仟佰])?\s*(?P<suffix>元整|元正|元|块|圆|刀|美金|美元|港币|港元|日元|欧元|英镑|RMB|CNY|USD|HKD|EUR|GBP|JPY)?\s*$`)

	// 中文数字金额匹配，如 伍拾万元整、三百五十万元、十二万五千元
	chineseAmountRe = regexp.MustCompile(`^\s*(?P<prefix>(?:人民币|美元|美金|港币|港元|日元|欧元|英镑|新台币)\s*)?(?P<cn>[零〇一二两三四五六七八九十百千万亿壹贰叁肆伍陆柒捌玖拾佰仟萬億点點]+)\s*(?P<suffix>元整|元正|圆整|圆正|元|块|圆|美金|美元|港币|港元|日元|欧元|英镑)?\s*$`)
)

// GeneralizeAmountValue 将精确金额泛化为不可逆的近似区间表达（如 "88.6万元" -> "约90万元"）。
// 若输入不是合法的精确金额（如日期、百分比、比例或已包含近似词），返回 ("", false)。
func GeneralizeAmountValue(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if approxPrefixRe.MatchString(trimmed) || approxSuffixRe.MatchString(trimmed) {
		return "", false
	}
	if dateLikeRe.MatchString(trimmed) || percentLikeRe.MatchString(trimmed) || ratioLikeRe.MatchString(trimmed) || versionLikeRe.MatchString(trimmed) {
		return "", false
	}

	// 1. 尝试区间金额
	if m := amountRangeRe.FindStringSubmatch(trimmed); m != nil {
		prefix := strings.TrimSpace(m[1])
		lowStr := m[2]
		u1 := m[3]
		p2 := strings.TrimSpace(m[4])
		highStr := m[5]
		u2 := m[6]
		suffix := strings.TrimSpace(m[7])
		if prefix == "" && p2 != "" {
			prefix = p2
		}
		if prefix == "" && suffix == "" && u1 == "" && u2 == "" {
			return "", false
		}
		if u1 == "" && u2 != "" {
			u1 = u2
		}
		lowVal, ok1 := parseArabicNumber(lowStr)
		highVal, ok2 := parseArabicNumber(highStr)
		if !ok1 || !ok2 || lowVal <= 0 || highVal <= 0 {
			return "", false
		}
		if strings.HasPrefix(suffix, "万") && u2 == "" {
			u2 = "万"
			if u1 == "" {
				u1 = "万"
			}
			suffix = strings.TrimPrefix(suffix, "万")
		} else if strings.HasPrefix(suffix, "亿") && u2 == "" {
			u2 = "亿"
			if u1 == "" {
				u1 = "亿"
			}
			suffix = strings.TrimPrefix(suffix, "亿")
		}
		suffix = normalizeCurrencySuffix(suffix)
		lowTotal := lowVal * scaleMultiplier(u1)
		highTotal := highVal * scaleMultiplier(u2)
		hasExplicitScale := u1 != "" || u2 != ""
		useChineseScale := shouldUseChineseScale(prefix, suffix, hasExplicitScale)
		lowFormatted, lowUnit := formatGeneralizedNumber(lowTotal, hasExplicitScale, useChineseScale)
		highFormatted, highUnit := formatGeneralizedNumber(highTotal, hasExplicitScale, useChineseScale)
		if lowUnit == highUnit {
			return "约" + prefix + lowFormatted + "-" + highFormatted + highUnit + suffix, true
		}
		return "约" + prefix + lowFormatted + lowUnit + "-" + highFormatted + highUnit + suffix, true
	}

	// 2. 尝试单值阿拉伯数字金额
	if m := singleNumericAmountRe.FindStringSubmatch(trimmed); m != nil {
		prefix := strings.TrimSpace(m[1])
		numStr := m[2]
		scaleStr := m[3]
		suffix := strings.TrimSpace(m[4])
		if prefix == "" && scaleStr == "" && suffix == "" {
			return "", false
		}
		// 仅含千/百但无货币前后缀的不视为金额（例如 "100百"）
		if prefix == "" && suffix == "" && (scaleStr == "千" || scaleStr == "百" || scaleStr == "仟" || scaleStr == "佰") {
			return "", false
		}
		val, ok := parseArabicNumber(numStr)
		if !ok || val <= 0 {
			return "", false
		}
		total := val * scaleMultiplier(scaleStr)
		suffix = normalizeCurrencySuffix(suffix)
		hasExplicitScale := (scaleStr == "万" || scaleStr == "萬" || scaleStr == "亿" || scaleStr == "億") && strings.Contains(numStr, ".")
		useChineseScale := shouldUseChineseScale(prefix, suffix, scaleStr != "")
		numFormatted, unit := formatGeneralizedNumber(total, hasExplicitScale, useChineseScale)
		return "约" + prefix + numFormatted + unit + suffix, true
	}

	// 3. 尝试中文数字金额
	if m := chineseAmountRe.FindStringSubmatch(trimmed); m != nil {
		prefix := strings.TrimSpace(m[1])
		cnStr := strings.TrimSpace(m[2])
		suffix := strings.TrimSpace(m[3])
		if prefix == "" && suffix == "" && !strings.ContainsAny(cnStr, "万萬亿億") {
			return "", false
		}
		total, ok := parseChineseNumber(cnStr)
		if !ok || total <= 0 {
			return "", false
		}
		suffix = normalizeCurrencySuffix(suffix)
		if prefix == "" && suffix == "" {
			suffix = "元"
		}
		numFormatted, unit := formatGeneralizedNumber(total, false, true)
		return "约" + prefix + numFormatted + unit + suffix, true
	}

	return "", false
}

func parseArabicNumber(s string) (float64, bool) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if cleaned == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(cleaned, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func scaleMultiplier(scale string) float64 {
	switch scale {
	case "亿", "億":
		return 1e8
	case "万", "萬":
		return 1e4
	case "千", "仟":
		return 1e3
	case "百", "佰":
		return 1e2
	default:
		return 1
	}
}

func normalizeCurrencySuffix(suffix string) string {
	switch suffix {
	case "元整", "元正", "圆整", "圆正", "圆":
		return "元"
	default:
		return suffix
	}
}

func shouldUseChineseScale(prefix, suffix string, hasScale bool) bool {
	if hasScale {
		return true
	}
	if suffix == "元" || suffix == "块" || suffix == "圆" {
		return true
	}
	if prefix == "人民币" || prefix == "RMB" || prefix == "CNY" {
		return true
	}
	return false
}

func formatGeneralizedNumber(amount float64, hasExplicitDecimalScale bool, useChineseScale bool) (string, string) {
	if useChineseScale {
		if amount >= 1e8 {
			scaled := amount / 1e8
			rounded := roundScaledMagnitude(scaled, hasExplicitDecimalScale)
			return formatFloatClean(rounded), "亿"
		}
		if amount >= 1e4 {
			scaled := amount / 1e4
			rounded := roundScaledMagnitude(scaled, hasExplicitDecimalScale)
			return formatFloatClean(rounded), "万"
		}
	}

	// 小于 1 万或外币符号直接按十位/百位/千位取整
	if amount >= 1e4 {
		rounded := roundToStep(amount, 1000)
		return formatFloatClean(rounded), ""
	}
	if amount >= 1000 {
		rounded := roundToStep(amount, 100)
		return formatFloatClean(rounded), ""
	}
	if amount >= 100 {
		rounded := roundToStep(amount, 10)
		return formatFloatClean(rounded), ""
	}
	if amount >= 10 {
		rounded := roundToStep(amount, 5)
		return formatFloatClean(rounded), ""
	}
	rounded := math.Max(1, math.Round(amount))
	return formatFloatClean(rounded), ""
}

func roundScaledMagnitude(scaled float64, hasExplicitDecimalScale bool) float64 {
	switch {
	case scaled < 10:
		if hasExplicitDecimalScale {
			return roundToStep(scaled, 0.5)
		}
		return math.Max(1, math.Round(scaled))
	case scaled < 100:
		return roundToStep(scaled, 5)
	case scaled < 1000:
		return roundToStep(scaled, 50)
	default:
		return roundToStep(scaled, 500)
	}
}

func roundToStep(val, step float64) float64 {
	if step <= 0 {
		return val
	}
	return math.Round(val/step) * step
}

func formatFloatClean(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return strconv.FormatInt(int64(math.Round(v)), 10)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

var chineseDigitMap = map[rune]float64{
	'零': 0, '〇': 0,
	'一': 1, '壹': 1,
	'二': 2, '两': 2, '贰': 2,
	'三': 3, '叁': 3,
	'四': 4, '肆': 4,
	'五': 5, '伍': 5,
	'六': 6, '陆': 6,
	'七': 7, '柒': 7,
	'八': 8, '捌': 8,
	'九': 9, '玖': 9,
}

var chineseSmallUnitMap = map[rune]float64{
	'十': 10, '拾': 10,
	'百': 100, '佰': 100,
	'千': 1000, '仟': 1000,
}

func parseChineseNumber(s string) (float64, bool) {
	var total, section, current float64
	hasValid := false
	for _, r := range s {
		if d, ok := chineseDigitMap[r]; ok {
			current = d
			hasValid = true
			continue
		}
		if u, ok := chineseSmallUnitMap[r]; ok {
			hasValid = true
			if current == 0 {
				current = 1
			}
			section += current * u
			current = 0
			continue
		}
		switch r {
		case '万', '萬':
			hasValid = true
			section += current
			if section == 0 {
				section = 1
			}
			total += section * 1e4
			section = 0
			current = 0
		case '亿', '億':
			hasValid = true
			section += current
			if section == 0 {
				section = 1
			}
			total = (total + section) * 1e8
			section = 0
			current = 0
		default:
			return 0, false
		}
	}
	if !hasValid {
		return 0, false
	}
	return total + section + current, true
}
