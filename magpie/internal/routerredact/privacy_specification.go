package routerredact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routermanifest"
)

const (
	PrivacySourceCommit       = "6fcdae87a1dc231c5c6d1d4a4ec24564e1c752df"
	PrivacyMaxFields          = 128
	PrivacyMaxFieldRunes      = 2048
	PrivacyMaxBatchFields     = 16
	PrivacyMaxBatchRunes      = 2048
	PrivacyMaxTotalRunes      = 8192
	PrivacyMaxInputBytes      = 32768
	PrivacyMaxBatches         = 16
	PrivacyMaxOutputTokens    = 4096
	PrivacyPositionFullRunes  = 128
	PrivacyPositionBlockRunes = 8
)

// ModelBinding identifies only entries in the fixed local artifact catalog.
// It neither selects nor starts a runtime, and makes no acceptance assertion.
type ModelBinding struct {
	ModelID, RuntimeID string
	// ContextTokens is supplied only by the trusted runtime Manager. Zero keeps
	// the fixed catalog's original serving cap; it changes no privacy policy.
	ContextTokens int
}

type PrivacyFingerprints struct {
	Source           string `json:"source"`
	Model            string `json:"model"`
	Prompt           string `json:"prompt"`
	Schema           string `json:"schema"`
	Config           string `json:"config"`
	PromptSchema     string `json:"promptSchema"`
	RuntimeArtifact  string `json:"runtimeArtifact"`
	ModelArtifact    string `json:"modelArtifact"`
	TemplateArtifact string `json:"templateArtifact"`
	Suite            string `json:"suite"`
}

// PrivacyRoutingVersions is a neutral value adapter for W4's independently
// committed six-field RoutingVersions. It imports no service/dispatch package.
type PrivacyRoutingVersions struct {
	LocalRuntimeFingerprint  string
	LocalModelFingerprint    string
	LocalTemplateFingerprint string
	RedactionPromptVersion   string
	RedactionSchemaVersion   string
	ConfigVersion            string
}

// PrivacySpecification contains immutable material/binding only. Requests and
// Unicode offset tables are local to Build/Parse; there is no shared input slot.
type PrivacySpecification struct {
	modelID      string
	versions     Versions
	fingerprints PrivacyFingerprints
	experimental bool
}

func privacyHash(value []byte) string {
	h := sha256.Sum256(value)
	return hex.EncodeToString(h[:])
}

func NewPrivacySpecification(binding ModelBinding) (*PrivacySpecification, error) {
	return newPrivacySpecification(binding, false)
}

// NewExperimentalPrivacySpecification preserves the fixed recognition material
// and strict parser, with an independently versioned 30-second trial policy.
// It makes no quality or capability acceptance assertion.
func NewExperimentalPrivacySpecification(binding ModelBinding) (*PrivacySpecification, error) {
	return newPrivacySpecification(binding, true)
}

func newPrivacySpecification(binding ModelBinding, experimental bool) (*PrivacySpecification, error) {
	catalog := routermanifest.Fixed()
	model, modelOK := catalog.Model(binding.ModelID)
	runtime, runtimeOK := catalog.Runtime(binding.RuntimeID)
	if !modelOK || !runtimeOK || (experimental && model.ID != "spark-x2.5-1.7B" && model.ID != "spark-x2.5-4B") {
		return nil, fail("redaction_unavailable")
	}
	if binding.ContextTokens != 0 {
		if routerconfig.ValidateContextTokens(binding.ContextTokens) != nil || model.NativeContextTokens <= 0 || binding.ContextTokens > model.NativeContextTokens {
			return nil, fail("redaction_unavailable")
		}
		model.ContextTokens = binding.ContextTokens
	}
	labels := PrivacyLabels()
	if len(labels) != 49 || PrivacyPrompt() == "" || PrivacySchema() == "" {
		return nil, fail("redaction_specification_failed")
	}
	categories := map[string]bool{}
	for _, category := range labels {
		if !categoryValid(category) || categories[category] {
			return nil, fail("redaction_specification_failed")
		}
		categories[category] = true
	}
	// Keep every engineering choice in this canonical versioned policy. Any
	// change invalidates cache identity and requires a new quality evaluation.
	policy := map[string]any{
		"revision": "privacy-go-v5", "source": PrivacySourceCommit,
		"responseFormat": "llama-b10828-json-object-schema-v1", "fieldCoverage": "exact-local-count",
		"positionGuide":     "unicode-complete-block-index-v2",
		"positionFullRunes": PrivacyPositionFullRunes, "positionBlockRunes": PrivacyPositionBlockRunes,
		"generationRepresentation": "strict-complete-objects-v1",
		"labels":                   labels, "actions": "mask-all-returned-labels", "amountGeneralization": false,
		"coordinateRecovery": false, "confidenceFiltering": false, "fieldSplitting": false,
		"maxFields": PrivacyMaxFields, "maxFieldRunes": PrivacyMaxFieldRunes,
		"maxBatchFields": PrivacyMaxBatchFields, "maxBatchRunes": PrivacyMaxBatchRunes,
		"maxTotalRunes": PrivacyMaxTotalRunes, "maxInputBytes": PrivacyMaxInputBytes,
		"maxBatches": PrivacyMaxBatches, "maxOutputBytes": 1 << 20,
		"maxOutputTokens": PrivacyMaxOutputTokens, "temperature": 0,
		"stream": false, "enableThinking": false, "queueAndExecutionSeconds": 15,
		"maxEntities": maxEntities, "maxEntityBytes": maxEntityBytes,
	}
	suitePrefix := "privacy-go-v5:"
	if experimental {
		policy["revision"] = "privacy-trial-v1"
		policy["experimental"] = true
		policy["qualityAccepted"] = false
		policy["queueAndExecutionSeconds"] = 30
		suitePrefix = "privacy-trial-v1:"
	}
	config, _ := json.Marshal(policy)
	promptSchema, _ := json.Marshal([]string{PrivacyPrompt(), PrivacySchema()})
	f := PrivacyFingerprints{Source: PrivacySourceCommit, Model: model.Fingerprint(runtime),
		Prompt: privacyHash([]byte(PrivacyPrompt())), Schema: privacyHash([]byte(PrivacySchema())),
		Config: privacyHash(config), PromptSchema: privacyHash(promptSchema),
		RuntimeArtifact: runtime.Artifact.SHA256, ModelArtifact: model.Artifact.SHA256, TemplateArtifact: model.Template.SHA256}
	f.Suite = suitePrefix + privacyHash([]byte(f.PromptSchema+":"+f.Config))
	return &PrivacySpecification{modelID: model.ID, versions: Versions{f.Model, f.PromptSchema, f.Config}, fingerprints: f, experimental: experimental}, nil
}

func (s *PrivacySpecification) Versions() Versions {
	if s == nil {
		return Versions{}
	}
	return s.versions
}
func (s *PrivacySpecification) Fingerprints() PrivacyFingerprints {
	if s == nil {
		return PrivacyFingerprints{}
	}
	return s.fingerprints
}
func (s *PrivacySpecification) RuntimeFingerprint() string { return s.Versions().Model }
func (s *PrivacySpecification) CapabilitySuite() string    { return s.Fingerprints().Suite }

// Experimental identifies the explicit trial policy, never a capability result.
func (s *PrivacySpecification) Experimental() bool { return s != nil && s.experimental }

// Budget bounds all full-field batches of one extraction, including queues.
func (s *PrivacySpecification) Budget() time.Duration {
	if s == nil {
		return 0
	}
	if s.experimental {
		return 30 * time.Second
	}
	return 15 * time.Second
}

func (s *PrivacySpecification) RoutingVersions() PrivacyRoutingVersions {
	f := s.Fingerprints()
	return PrivacyRoutingVersions{f.RuntimeArtifact, f.ModelArtifact, f.TemplateArtifact, f.Prompt, f.Schema, f.Config}
}

// ResolveVersions checks the full injected W4 tuple rather than combining
// unchecked cloud/judgment version strings into a plausible cache key.
func (s *PrivacySpecification) ResolveVersions(v PrivacyRoutingVersions) (Versions, error) {
	if s == nil || s.versions.Model == "" || s.versions.Prompt == "" || s.versions.Config == "" || v != s.RoutingVersions() {
		return Versions{}, fail("redaction_unavailable")
	}
	return s.Versions(), nil
}

// Batches never splits a source field. In particular, a name spanning the old
// source's 1500-character boundary remains visible in one input. A longer field
// is refused in full, rather than truncating it or claiming overlap proves recall.
func (s *PrivacySpecification) Batches(ctx context.Context, in Extraction) ([][]int, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.versions.Model == "" || s.versions.Prompt == "" || s.versions.Config == "" || in.Versions != s.Versions() {
		return nil, fail("redaction_unavailable")
	}
	if len(in.Texts) > PrivacyMaxFields {
		return nil, fail("redaction_input_limit")
	}
	batches := [][]int{{}}
	runes, bytes, batchRunes := 0, 0, 0
	for field, text := range in.Texts {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if !utf8.ValidString(text) {
			return nil, fail("redaction_invalid_input")
		}
		length := utf8.RuneCountInString(text)
		runes += length
		bytes += len(text)
		if length > PrivacyMaxFieldRunes || runes > PrivacyMaxTotalRunes || bytes > PrivacyMaxInputBytes {
			return nil, fail("redaction_input_limit")
		}
		last := len(batches) - 1
		if len(batches[last]) >= PrivacyMaxBatchFields || batchRunes+length > PrivacyMaxBatchRunes {
			if len(batches) >= PrivacyMaxBatches {
				return nil, fail("redaction_input_limit")
			}
			batches = append(batches, []int{})
			last++
			batchRunes = 0
		}
		batches[last] = append(batches[last], field)
		batchRunes += length
	}
	return batches, nil
}

func (s *PrivacySpecification) Build(ctx context.Context, in Extraction) ([]byte, error) {
	batches, err := s.Batches(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(batches) != 1 {
		return nil, fail("redaction_input_limit")
	}
	type field struct {
		Field     int    `json:"field"`
		Text      string `json:"text"`
		Positions string `json:"positions"`
	}
	fields := make([]field, len(in.Texts))
	for index, text := range in.Texts {
		var positions strings.Builder
		runes := []rune(text)
		step := 1
		if len(runes) > PrivacyPositionFullRunes {
			step = PrivacyPositionBlockRunes
		}
		for i := 0; i < len(runes); i += step {
			positions.WriteString(strconv.Itoa(i))
			positions.WriteByte(':')
			quoted, _ := json.Marshal(string(runes[i:min(i+step, len(runes))]))
			positions.Write(quoted)
			positions.WriteByte(' ')
		}
		positions.WriteString("end=")
		positions.WriteString(strconv.Itoa(len(runes)))
		fields[index] = field{index, text, positions.String()}
	}
	user, err := json.Marshal(struct {
		Operation string  `json:"operation"`
		Fields    []field `json:"fields"`
	}{"classify", fields})
	if err != nil {
		return nil, fail("redaction_specification_failed")
	}
	var schema map[string]any
	if json.Unmarshal([]byte(PrivacySchema()), &schema) != nil {
		return nil, fail("redaction_specification_failed")
	}
	// b10828's schema-constrained JSON format constrains structure only. The
	// strict parser still checks labels, complete field membership and spans.
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, fail("redaction_specification_failed")
	}
	fieldSchema, ok := properties["fields"].(map[string]any)
	if !ok {
		return nil, fail("redaction_specification_failed")
	}
	fieldSchema["minItems"], fieldSchema["maxItems"] = len(fields), len(fields)
	return json.Marshal(map[string]any{
		"model": s.modelID, "temperature": 0, "stream": false,
		"max_tokens":           PrivacyMaxOutputTokens,
		"response_format":      map[string]any{"type": "json_object", "schema": schema},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"messages":             []any{map[string]any{"role": "system", "content": PrivacyPrompt()}, map[string]any{"role": "user", "content": string(user)}},
	})
}
