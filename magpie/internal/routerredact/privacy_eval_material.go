package routerredact

import (
	_ "embed"
	"encoding/json"
)

//go:embed testdata/privacy-eval.json
var privacyEvaluationMaterial []byte

// PrivacyEvaluationCases returns the fixed W5 synthetic annotation set bound
// to this specification. It is an explicit diagnostic input; production
// request handling never loads or scans this material.
func PrivacyEvaluationCases(spec *PrivacySpecification) ([]PrivacyCase, error) {
	if spec == nil || spec.Versions() == (Versions{}) {
		return nil, fail("redaction_unavailable")
	}
	var cases []PrivacyCase
	if err := json.Unmarshal(privacyEvaluationMaterial, &cases); err != nil || len(cases) != 56 {
		return nil, fail("redaction_specification_failed")
	}
	for i := range cases {
		cases[i].Versions = spec.Versions()
	}
	return cases, nil
}
