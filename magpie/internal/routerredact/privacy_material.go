package routerredact

import (
	_ "embed"
	"encoding/json"
)

// These are reviewed materials for the versioned adapter. Embedding them does
// not register a runtime capability or accept any model's recognition quality.
//
//go:embed privacy_assets/prompt.zh.txt
var privacyPrompt string

//go:embed privacy_assets/labels.json
var privacyLabels string

//go:embed privacy_assets/schema.json
var privacySchema string

//go:embed privacy_assets/examples.json
var privacyExamples string

// PrivacyPrompt returns the adapted multi-field recognition instructions.
func PrivacyPrompt() string { return privacyPrompt }

// PrivacyLabels returns an independent fixed label-to-placeholder-category map.
// Every valid label, including public_information, produces reversible masking;
// acceptance, span validation and protected-field blocking remain caller-owned.
func PrivacyLabels() map[string]string {
	labels := make(map[string]string, 49)
	if err := json.Unmarshal([]byte(privacyLabels), &labels); err != nil {
		panic("invalid embedded privacy label material")
	}
	return labels
}

// PrivacySchema returns the strict model-content schema. The parser additionally
// checks field membership/completeness, exact code-point spans and finite values.
func PrivacySchema() string { return privacySchema }

// PrivacyExamplesJSON returns a private copy of the 15 corrected source examples.
// They verify materials and contracts; they are not model-quality evidence.
func PrivacyExamplesJSON() []byte { return []byte(privacyExamples) }
