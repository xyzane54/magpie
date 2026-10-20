package routerredact

import "testing"

func TestGeneralizeAmountValue(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
		ok       bool
	}{
		{"88.6万元", "约90万元", true},
		{"12,580元", "约1万元", true},
		{"人民币358万元", "约人民币350万元", true},
		{"$3,520", "约$3500", true},
		{"1.6万元", "约1.5万元", true},
		{"3.6亿元", "约3.5亿元", true},
		{"伍拾万元整", "约50万元", true},
		{"80-120万元", "约80-100万元", true},
		{"2026-05-01", "", false},
		{"2026年5月1日", "", false},
		{"50%", "", false},
		{"12.5%", "", false},
		{"3:2", "", false},
		{"v1.2.3", "", false},
		{"约90万元", "", false},
	}

	for _, tc := range cases {
		got, ok := GeneralizeAmountValue(tc.raw)
		if ok != tc.ok {
			t.Fatalf("GeneralizeAmountValue(%q) ok = %v, want %v (got %q)", tc.raw, ok, tc.ok, got)
		}
		if got != tc.expected {
			t.Fatalf("GeneralizeAmountValue(%q) = %q, want %q", tc.raw, got, tc.expected)
		}
	}
}
