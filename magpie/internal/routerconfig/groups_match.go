package routerconfig

import (
	"fmt"
	"strings"
	"unicode"
)

func normalizeClaudeVersion(s string) string {
	s = strings.ReplaceAll(s, ".", "-")
	if strings.HasSuffix(s, "-4-0") {
		s = strings.TrimSuffix(s, "-0")
	}
	for _, family := range []string{"opus", "sonnet", "haiku"} {
		oldPrefix := "claude-3-" + family
		if strings.HasPrefix(s, oldPrefix) {
			return s
		}
		for _, ver := range []string{"4-6", "4-5", "4-1", "4"} {
			v := "claude-" + ver + "-" + family
			if s == v || strings.HasPrefix(s, v+"-") {
				rest := strings.TrimPrefix(s, v)
				return "claude-" + family + "-" + ver + rest
			}
		}
	}
	return s
}

func stripDate(s string) string {
	if len(s) > 9 && s[len(s)-9] == '-' && isDigits(s[len(s)-8:]) {
		return s[:len(s)-9]
	}
	if len(s) > 11 && s[len(s)-11] == '-' && s[len(s)-6] == '-' && s[len(s)-3] == '-' &&
		isDigits(s[len(s)-10:len(s)-6]) && isDigits(s[len(s)-5:len(s)-3]) && isDigits(s[len(s)-2:]) {
		return s[:len(s)-11]
	}
	if len(s) > 5 && s[len(s)-5] == '-' && isDigits(s[len(s)-4:]) {
		mm := s[len(s)-4 : len(s)-2]
		if mm >= "01" && mm <= "12" {
			return s[:len(s)-5]
		}
	}
	return s
}

func isDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func hasControlChars(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

func validateGroupID(id string) error {
	if id == "" {
		return invalidGroup("group id is required")
	}
	if len(id) > maxGroupIDLen {
		return invalidGroup("group id exceeds maximum length of 128 characters")
	}
	if hasControlChars(id) {
		return invalidGroup("group id contains control characters")
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return invalidGroup("group id must not contain whitespace or control characters")
		}
	}
	return nil
}

func cleanGroupSlice(items []string, field string) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, raw := range items {
		if hasControlChars(raw) {
			return nil, invalidGroup(fmt.Sprintf("group %s contains invalid control characters", field))
		}
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
