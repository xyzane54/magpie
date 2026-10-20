package routerredact

import (
	"regexp"
	"strings"
)

// SensitiveLabels 定义需要执行脱敏或泛化的标准敏感标签集合。
var SensitiveLabels = map[string]bool{
	"NAME":               true,
	"PHONE":              true,
	"EMAIL":              true,
	"WECHAT":             true,
	"QQ":                 true,
	"ID_CARD":            true,
	"PASSPORT":           true,
	"HM_PASS":            true,
	"TW_PASS":            true,
	"DRIVER_LICENSE":     true,
	"MILITARY_ID":        true,
	"BIRTH_DATE":         true,
	"BANK_CARD":          true,
	"SOCIAL_CREDIT_CODE": true,
	"SOCIAL_CREDIT_ID":   true,
	"CASE_NUMBER":        true,
	"WORK_ORDER":         true,
	"PLATE_NUMBER":       true,
	"ADDRESS":            true,
	"ORG":                true,
	"IP":                 true,
	"MAC":                true,
	"HOSTNAME":           true,
	"URL":                true,
	"SECRET":             true,
	"AMOUNT":             true,
	"DATABASE_URI":       true,
	"PRIVATE_KEY":        true,
	"CLOUD_KEY":          true,
	"OAUTH_SECRET":       true,
	"COOKIE_SESSION":     true,
	"VEHICLE_ID":         true,
	"MEDICAL_ID":         true,
	"TAX_ID":             true,
	"PROPERTY_ID":        true,
}

// AllowedPassThroughLabels 定义允许原样透传、不执行脱敏的标签集合。
var AllowedPassThroughLabels = map[string]bool{
	"DATE":               true,
	"TIME":               true,
	"DATETIME":           true,
	"TIMESTAMP":          true,
	"DURATION":           true,
	"PERCENT":            true,
	"PERCENTAGE":         true,
	"RATIO":              true,
	"VERSION":            true,
	"PORT":               true,
	"NUMBER":             true,
	"ORDINAL":            true,
	"QUANTITY":           true,
	"COUNT":              true,
	"AGE":                true,
	"PUBLIC_INFORMATION": true,
	"JOB_TITLE":          true,
}

// LLMIgnoredLabels 定义本地小模型输出中应当忽略的非敏感标签集合。
var LLMIgnoredLabels = map[string]bool{
	"DATE":               true,
	"AGE":                true,
	"TIME":               true,
	"DATETIME":           true,
	"TIMESTAMP":          true,
	"DURATION":           true,
	"PERCENT":            true,
	"PERCENTAGE":         true,
	"RATIO":              true,
	"VERSION":            true,
	"PORT":               true,
	"NUMBER":             true,
	"ORDINAL":            true,
	"QUANTITY":           true,
	"COUNT":              true,
	"PUBLIC_INFORMATION": true,
	"JOB_TITLE":          true,
}

// ReviewPassTargetLabels 定义本地小模型二次聚焦复核的目标标签集合。
var ReviewPassTargetLabels = map[string]bool{
	"NAME":    true,
	"ORG":     true,
	"ADDRESS": true,
}

// CompoundSecretLabels 定义复合密钥标签集合（其内部子串实体会被抑制）。
var CompoundSecretLabels = map[string]bool{
	"CONNECTION_STRING": true,
	"PRIVATE_KEY":       true,
	"JWT":               true,
	"COOKIE":            true,
	"API_KEY":           true,
	"PASSWORD":          true,
	"TOKEN":             true,
	"SECRET":            true,
	"URL":               true,
	"DATABASE_URI":      true,
	"CLOUD_KEY":         true,
	"OAUTH_SECRET":      true,
	"COOKIE_SESSION":    true,
}

// AdjacentMergeableLabels 定义相邻同源中文片段可合并的标签集合。
var AdjacentMergeableLabels = map[string]bool{
	"ADDRESS": true,
}

// CanonicalPriority 定义重叠区间消解时的标准标签优先级。
var CanonicalPriority = map[string]int{
	"SECRET":             100,
	"PRIVATE_KEY":        100,
	"DATABASE_URI":       98,
	"CLOUD_KEY":          96,
	"ID_CARD":            96,
	"BANK_CARD":          95,
	"OAUTH_SECRET":       94,
	"PASSPORT":           93,
	"HM_PASS":            93,
	"TW_PASS":            93,
	"DRIVER_LICENSE":     92,
	"MILITARY_ID":        92,
	"COOKIE_SESSION":     92,
	"SOCIAL_CREDIT_CODE": 90,
	"SOCIAL_CREDIT_ID":   90,
	"PHONE":              88,
	"EMAIL":              86,
	"WECHAT":             84,
	"TAX_ID":             84,
	"QQ":                 83,
	"PLATE_NUMBER":       82,
	"CASE_NUMBER":        80,
	"MEDICAL_ID":         80,
	"WORK_ORDER":         78,
	"PROPERTY_ID":        78,
	"IP":                 76,
	"MAC":                76,
	"VEHICLE_ID":         76,
	"HOSTNAME":           74,
	"URL":                73,
	"NAME":               72,
	"ADDRESS":            68,
	"ORG":                66,
	"AMOUNT":             64,
	"BIRTH_DATE":         62,
	"DATE":               60,
	"AGE":                58,
}

// SourcePriority 定义不同检测器来源的优先级。
var SourcePriority = map[string]int{
	"rule":              30,
	"rules":             30,
	"dictionary":        20,
	"dict":              20,
	"local_llm":         10,
	"local_llm_primary": 10,
	"local_llm_review":  10,
	"llm":               10,
}

// CanonicalLabelMap 将细粒度隐私标签与各类别名统一收敛为标准标签。
var CanonicalLabelMap = map[string]string{
	// Step 1.2 核心 39 -> 19 标准映射
	"NAME":               "NAME",
	"PERSON":             "NAME",
	"PHONE":              "PHONE",
	"MOBILE":             "PHONE",
	"LANDLINE":           "PHONE",
	"EMAIL":              "EMAIL",
	"WECHAT":             "WECHAT",
	"QQ":                 "QQ",
	"ID_CARD":            "ID_CARD",
	"PASSPORT":           "PASSPORT",
	"HM_PASS":            "HM_PASS",
	"TW_PASS":            "TW_PASS",
	"DRIVER_LICENSE":     "DRIVER_LICENSE",
	"MILITARY_ID":        "MILITARY_ID",
	"BIRTH_DATE":         "BIRTH_DATE",
	"AGE":                "AGE",
	"BANK_CARD":          "BANK_CARD",
	"SOCIAL_CREDIT_CODE": "SOCIAL_CREDIT_CODE",
	"CASE_NUMBER":        "CASE_NUMBER",
	"WORK_ORDER":         "WORK_ORDER",
	"PLATE_NUMBER":       "PLATE_NUMBER",
	"ADDRESS":            "ADDRESS",
	"ORG":                "ORG",
	"COMPANY":            "ORG",
	"SCHOOL":             "ORG",
	"GEO":                "ADDRESS",
	"IP":                 "IP",
	"IPV6":               "IP",
	"MAC":                "MAC",
	"HOSTNAME":           "HOSTNAME",
	"INTERNAL_DOMAIN":    "HOSTNAME",
	"URL":                "URL",
	"COOKIE":             "SECRET",
	"PASSWORD":           "SECRET",
	"API_KEY":            "SECRET",
	"PRIVATE_KEY":        "SECRET",
	"JWT":                "SECRET",
	"TOKEN":              "SECRET",
	"CONNECTION_STRING":  "SECRET",
	"SECRET":             "SECRET",
	"AMOUNT":             "AMOUNT",
	"FINANCIAL":          "AMOUNT",
	"CONTRACT_VALUE":     "AMOUNT",
	"SALARY":             "AMOUNT",
	"BANK_BALANCE":       "AMOUNT",
	"TAX_RECORD":         "AMOUNT",
	"DATE":               "DATE",

	// 兼容 rules.zh.yaml / rules.py / dictionary.yaml / 旧版标签别名
	"PER":                       "NAME",
	"USER":                      "NAME",
	"USERNAME":                  "NAME",
	"CONTACT":                   "PHONE",
	"CLIENT_NAME":               "NAME",
	"CUSTOMER_NAME":             "NAME",
	"EMPLOYEE_NAME":             "NAME",
	"PARTY_A_CONTACT":           "NAME",
	"PARTY_B_CONTACT":           "NAME",
	"PRIVATE_PERSON":            "NAME",
	"LEGAL_PARTICIPANT":         "NAME",
	"TEL":                       "PHONE",
	"TELEPHONE":                 "PHONE",
	"PHONE_NUMBER":              "PHONE",
	"CELLPHONE":                 "PHONE",
	"CONTACT_PHONE":             "PHONE",
	"PRIVATE_PHONE":             "PHONE",
	"PRIVATE_LANDLINE":          "PHONE",
	"MAIL":                      "EMAIL",
	"E_MAIL":                    "EMAIL",
	"EMAIL_ADDRESS":             "EMAIL",
	"PRIVATE_EMAIL":             "EMAIL",
	"PRIVATE_URL":               "URL",
	"ID":                        "ID_CARD",
	"ID_NUMBER":                 "ID_CARD",
	"IDENTITY_CARD":             "ID_CARD",
	"CHINA_ID":                  "ID_CARD",
	"NATIONAL_ID":               "ID_CARD",
	"CITIZEN_ID":                "ID_CARD",
	"SFZ":                       "ID_CARD",
	"GOVERNMENT_ID":             "ID_CARD",
	"RESIDENCE_PERMIT":          "ID_CARD",
	"SOCIAL_SECURITY":           "ID_CARD",
	"STUDENT_ID":                "ID_CARD",
	"PASSPORT_NO":               "PASSPORT",
	"PASSPORT_NUMBER":           "PASSPORT",
	"HK_MACAO_PASS":             "HM_PASS",
	"TAIWAN_PASS":               "TW_PASS",
	"SOCIAL_CREDIT_ID":          "SOCIAL_CREDIT_CODE",
	"USCC":                      "SOCIAL_CREDIT_CODE",
	"UNIFIED_CREDIT_ID":         "SOCIAL_CREDIT_CODE",
	"ORGANIZATION_IDENTIFIER":   "SOCIAL_CREDIT_CODE",
	"IDENTIFIER":                "ID_CARD",
	"CARD_NUMBER":               "BANK_CARD",
	"DEBIT_CARD":                "BANK_CARD",
	"CREDIT_CARD":               "BANK_CARD",
	"BANK_ACCOUNT":              "BANK_CARD",
	"ACCOUNT_NUMBER":            "BANK_CARD",
	"FINANCIAL_ACCOUNT":         "BANK_CARD",
	"ACCOUNT":                   "BANK_CARD",
	"LOGIN_ACCOUNT":             "SECRET",
	"IBAN":                      "BANK_CARD",
	"IBAN_SWIFT":                "BANK_CARD",
	"SECURITIES_ACCOUNT":        "BANK_CARD",
	"CRYPTO_WALLET":             "BANK_CARD",
	"MONEY":                     "AMOUNT",
	"CURRENCY":                  "AMOUNT",
	"FINANCIAL_AMOUNT":          "AMOUNT",
	"FINANCIALAMOUNT":           "AMOUNT",
	"UNPUBLISHED_REVENUE":       "AMOUNT",
	"PRICE":                     "AMOUNT",
	"FEE":                       "AMOUNT",
	"PAYMENT_AMOUNT":            "AMOUNT",
	"CONTRACT_AMOUNT":           "AMOUNT",
	"LOAN_AMOUNT":               "AMOUNT",
	"INVOICE_NUMBER":            "WORK_ORDER",
	"INVOICE_NO":                "WORK_ORDER",
	"TAX_ID":                    "SOCIAL_CREDIT_CODE",
	"TAX_NUMBER":                "SOCIAL_CREDIT_CODE",
	"PROPERTY_ID":               "WORK_ORDER",
	"PROPERTY_IDENTIFIER":       "WORK_ORDER",
	"REAL_ESTATE_NO":            "WORK_ORDER",
	"PROJECT_CODE":              "WORK_ORDER",
	"CONTRACT_NUMBER":           "WORK_ORDER",
	"UNIQUE_TRANSACTION_ID":     "WORK_ORDER",
	"FILE_PATH":                 "WORK_ORDER",
	"MEDICAL_RECORD_NUMBER":     "WORK_ORDER",
	"ORGANIZATION":              "ORG",
	"ENTERPRISE":                "ORG",
	"INSTITUTION":               "ORG",
	"PARTY_A":                   "ORG",
	"PARTY_B":                   "ORG",
	"CONTRACT_PARTY":            "ORG",
	"CUSTOMER_SUPPLIER":         "ORG",
	"RELATED_PARTY":             "ORG",
	"EMPLOYER":                  "ORG",
	"VENDOR":                    "ORG",
	"BANK_NAME":                 "ORG",
	"COURT":                     "ORG",
	"HOSPITAL":                  "ORG",
	"ORGANIZATION_NAME":         "ORG",
	"CUSTOMER_RECORD":           "ORG",
	"SUPPLIER_RECORD":           "ORG",
	"INTERNAL_CODENAME":         "ORG",
	"BUSINESS_CONFIDENTIAL":     "ORG",
	"ADDR":                      "ADDRESS",
	"LOCATION":                  "ADDRESS",
	"LOC":                       "ADDRESS",
	"ROOM":                      "ADDRESS",
	"BUILDING":                  "ADDRESS",
	"STREET_ADDRESS":            "ADDRESS",
	"GPE":                       "ADDRESS",
	"PHYSICAL_ADDRESS":          "ADDRESS",
	"PRIVATE_ADDRESS":           "ADDRESS",
	"ORGANIZATION_ADDRESS":      "ADDRESS",
	"GEOGRAPHIC_LOCATION":       "ADDRESS",
	"COORDINATES":               "ADDRESS",
	"CASE_NO":                   "CASE_NUMBER",
	"CASE_IDENTIFIER":           "CASE_NUMBER",
	"DOCKET_NUMBER":             "CASE_NUMBER",
	"LEGAL_CASE":                "CASE_NUMBER",
	"LICENSE_PLATE":             "PLATE_NUMBER",
	"CAR_PLATE":                 "PLATE_NUMBER",
	"VEHICLE_PLATE":             "PLATE_NUMBER",
	"VEHICLE_ID":                "PLATE_NUMBER",
	"VIN":                       "PLATE_NUMBER",
	"MEDICAL_ID":                "ID_CARD",
	"MEDICAL_RECORD":            "ID_CARD",
	"MEDICAL_RECORD_ID":         "ID_CARD",
	"PATIENT_ID":                "ID_CARD",
	"IPV4":                      "IP",
	"IP_ADDRESS":                "IP",
	"INTERNAL_IP":               "IP",
	"MAC_ADDRESS":               "MAC",
	"APIKEY":                    "SECRET",
	"ACCESS_TOKEN":              "SECRET",
	"REFRESH_TOKEN":             "SECRET",
	"BEARER_TOKEN":              "SECRET",
	"PASSWD":                    "SECRET",
	"PWD":                       "SECRET",
	"SECRET_KEY":                "SECRET",
	"WEBHOOK_SECRET":            "SECRET",
	"SIGNING_SECRET":            "SECRET",
	"HIGH_ENTROPY":              "SECRET",
	"DATABASE_URL":              "SECRET",
	"DSN":                       "SECRET",
	"DATABASE_URI":              "SECRET",
	"DB_URI":                    "SECRET",
	"REDIS_URI":                 "SECRET",
	"MONGO_URI":                 "SECRET",
	"SSH_KEY":                   "SECRET",
	"PEM_KEY":                   "SECRET",
	"CLOUD_KEY":                 "SECRET",
	"AWS_KEY":                   "SECRET",
	"AKIA":                      "SECRET",
	"ALIYUN_KEY":                "SECRET",
	"TENCENT_KEY":               "SECRET",
	"OAUTH_SECRET":              "SECRET",
	"CLIENT_SECRET":             "SECRET",
	"COOKIE_SESSION":            "SECRET",
	"SESSION_ID":                "SECRET",
	"SESSION_TOKEN":             "SECRET",
	"CREDENTIAL":                "SECRET",
	"RESTRICTED_MATERIAL":       "SECRET",
	"FINANCIAL_STATEMENT":       "SECRET",
	"SENSITIVE_CONTENT":         "SECRET",
	"CONTRACT_TERM":             "SECRET",
	"SENSITIVE_BUSINESS_METRIC": "SECRET",
	"PAYMENT_PERCENTAGE":        "SECRET",
	"LIABILITY_PERCENTAGE":      "SECRET",
	"PRIVATE_DATE":              "BIRTH_DATE",
	"BIRTHDATE":                 "BIRTH_DATE",
	"SIGN_DATE":                 "DATE",
	"TIME":                      "TIME",
	"TIMESTAMP":                 "TIME",
	"DURATION":                  "DURATION",
	"PERIOD":                    "DURATION",
	"TERM":                      "DURATION",
	"PUBLIC_INFORMATION":        "PUBLIC_INFORMATION",
	"JOB_TITLE":                 "JOB_TITLE",
}

var whitespaceCollapseRe = regexp.MustCompile(`\s+`)

// CanonicalizeLabel 将输入标签标准化为标准大类。
func CanonicalizeLabel(raw string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(raw))
	cleaned = strings.ReplaceAll(cleaned, "-", "_")
	cleaned = strings.ReplaceAll(cleaned, " ", "_")
	if mapped, ok := CanonicalLabelMap[cleaned]; ok {
		return mapped
	}
	return cleaned
}

// LabelPriority 返回标签的消歧优先级（先查原标签，再查标准标签，默认 50）。
func LabelPriority(label string) int {
	upper := strings.ToUpper(strings.TrimSpace(label))
	if p, ok := CanonicalPriority[upper]; ok {
		return p
	}
	canonical := CanonicalizeLabel(upper)
	if p, ok := CanonicalPriority[canonical]; ok {
		return p
	}
	return 50
}

// GetLabelPriority 为 LabelPriority 的兼容别名。
func GetLabelPriority(label string) int {
	return LabelPriority(label)
}

// NormalizeLabel 将原始标签名归一化为标准敏感标签，非敏感或未知标签返回空字符串。
func NormalizeLabel(rawLabel string) string {
	cleaned := strings.TrimSpace(rawLabel)
	if cleaned == "" {
		return ""
	}
	canonical := CanonicalizeLabel(cleaned)
	if AllowedPassThroughLabels[canonical] {
		return ""
	}
	if SensitiveLabels[canonical] {
		return canonical
	}
	return ""
}

// NormalizeEntityValue 对实体值做类别感知的归一化，保证同一实体的不同书写形式共享同一个占位符。
func NormalizeEntityValue(label, value string) string {
	canon := CanonicalizeLabel(label)
	if canon == "NAME" {
		return strings.TrimSpace(value)
	}
	compact := strings.TrimSpace(whitespaceCollapseRe.ReplaceAllString(value, " "))
	switch canon {
	case "PHONE", "ID_CARD", "BANK_CARD":
		normalized := strings.ReplaceAll(compact, " ", "")
		normalized = strings.ReplaceAll(normalized, "-", "")
		if canon == "ID_CARD" {
			return strings.ToUpper(normalized)
		}
		return normalized
	case "EMAIL", "MAC":
		return strings.ToLower(compact)
	default:
		return compact
	}
}

// PlaceholderKey 生成跨上下文复用同一占位符编号的唯一键。
func PlaceholderKey(label, value string) string {
	upper := strings.ToUpper(strings.TrimSpace(label))
	return upper + "::" + NormalizeEntityValue(upper, value)
}
