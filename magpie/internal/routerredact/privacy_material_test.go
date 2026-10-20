package routerredact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type materialFinding struct {
	Label      string  `json:"label"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

type materialExample struct {
	Ordinal int `json:"ordinal"`
	Input   struct {
		Operation string `json:"operation"`
		Fields    []struct {
			Field int    `json:"field"`
			Text  string `json:"text"`
		} `json:"fields"`
	} `json:"input"`
	Output struct {
		Complete bool `json:"complete"`
		Fields   []struct {
			Field    int               `json:"field"`
			Complete bool              `json:"complete"`
			Findings []materialFinding `json:"findings"`
		} `json:"fields"`
	} `json:"output"`
}

func TestPrivacyMaterialsLabelMapAndPrivateCopies(t *testing.T) {
	labels := PrivacyLabels()
	if len(labels) != 49 {
		t.Fatal("privacy material must cover all 49 source labels")
	}
	seen := map[string]bool{}
	for label, category := range labels {
		if label == "" || !categoryValid(category) || seen[category] {
			t.Fatal("invalid or colliding fixed privacy category")
		}
		seen[category] = true
		if !strings.Contains(PrivacyPrompt(), label) {
			t.Fatal("source label missing from adapted instructions")
		}
	}
	for label, category := range map[string]string{
		"private_person": "PERSON", "private_email": "EMAIL", "private_address": "ADDRESS",
		"private_date": "DATE", "private_phone": "PHONE", "private_url": "PRIVATEURL",
		"organization_name": "ORGANIZATION", "public_information": "PUBLICINFORMATION",
	} {
		if labels[label] != category {
			t.Fatal("fixed compatibility category changed")
		}
	}
	labels["private_person"] = "MUTATED"
	delete(labels, "secret")
	if PrivacyLabels()["private_person"] != "PERSON" || PrivacyLabels()["secret"] != "SECRET" {
		t.Fatal("caller can mutate shared label material")
	}
	first := PrivacyExamplesJSON()
	original := bytes.Clone(first)
	first[0] = '!'
	if !bytes.Equal(PrivacyExamplesJSON(), original) {
		t.Fatal("caller can mutate shared example material")
	}
}

func TestPrivacyMaterialsCorrectEverySourceExampleCoordinate(t *testing.T) {
	var examples []materialExample
	if json.Unmarshal(PrivacyExamplesJSON(), &examples) != nil || len(examples) != 15 {
		t.Fatal("corrected example material incomplete")
	}
	var upstream []struct {
		Ordinal int    `json:"ordinal"`
		Text    string `json:"text"`
		Output  struct {
			Findings []materialFinding `json:"findings"`
		} `json:"output"`
	}
	raw, err := os.ReadFile("privacy_assets/upstream_examples.json")
	if err != nil || json.Unmarshal(raw, &upstream) != nil || len(upstream) != 15 {
		t.Fatal("source example provenance unavailable")
	}
	entities, defects := 0, 0
	expected := map[[2]int][4]int{
		{6, 1}: {1, 10, 1, 9}, {7, 1}: {0, 29, 0, 28},
		{9, 1}: {6, 18, 6, 19}, {9, 2}: {24, 35, 25, 37}, {14, 1}: {7, 25, 6, 24},
	}
	for i, example := range examples {
		if example.Ordinal != i+1 || example.Input.Operation != "classify" || !example.Output.Complete || len(example.Input.Fields) != 1 || len(example.Output.Fields) != 1 || example.Input.Fields[0].Field != 0 || example.Output.Fields[0].Field != 0 || !example.Output.Fields[0].Complete {
			t.Fatal("example must use complete local-field envelope")
		}
		field := example.Output.Fields[0]
		text := []rune(example.Input.Fields[0].Text)
		if upstream[i].Ordinal != example.Ordinal || upstream[i].Text != string(text) || len(upstream[i].Output.Findings) != len(field.Findings) {
			t.Fatal("coordinate repair changed source text or dropped entities")
		}
		for j, finding := range field.Findings {
			entities++
			if finding.Start < 0 || finding.End <= finding.Start || finding.End > len(text) || string(text[finding.Start:finding.End]) != finding.Text || PrivacyLabels()[finding.Label] == "" || math.IsNaN(finding.Confidence) || math.IsInf(finding.Confidence, 0) || finding.Confidence < 0 || finding.Confidence > 1 {
				t.Fatal("corrected example has invalid code-point coordinates or finding")
			}
			before := upstream[i].Output.Findings[j]
			if before.Label != finding.Label || before.Text != finding.Text || before.Confidence != finding.Confidence {
				t.Fatal("coordinate repair altered recognition content")
			}
			actual := [4]int{before.Start, before.End, finding.Start, finding.End}
			if correction, ok := expected[[2]int{example.Ordinal, j + 1}]; ok {
				defects++
				if correction != actual || before.End <= len(text) && string(text[before.Start:before.End]) == before.Text {
					t.Fatal("recorded source defect no longer proves a real coordinate correction")
				}
			} else if before.Start != finding.Start || before.End != finding.End {
				t.Fatal("unreviewed coordinate change")
			}
		}
	}
	if entities != 20 || defects != 5 {
		t.Fatal("expected 20 entities and exactly five source coordinate defects")
	}
	// Preserve the complete v1 history independently from the current prompt.
	archivedPrompt, err := os.ReadFile("privacy_assets/prompt.v1.zh.txt")
	if err != nil {
		t.Fatal("archived adapted prompt unavailable")
	}
	pairs := regexp.MustCompile(`(?m)^输入：(\{[^\n]+\})\n输出：(\{[^\n]+\})`).FindAllStringSubmatch(string(archivedPrompt), -1)
	if len(pairs) != len(examples) {
		t.Fatal("archived prompt does not contain every corrected source example")
	}
	for i, pair := range pairs {
		var input, output any
		if json.Unmarshal([]byte(pair[1]), &input) != nil || json.Unmarshal([]byte(pair[2]), &output) != nil {
			t.Fatal("prompt example JSON invalid")
		}
		wantInput, _ := json.Marshal(examples[i].Input)
		wantOutput, _ := json.Marshal(examples[i].Output)
		var normalizedInput, normalizedOutput any
		_ = json.Unmarshal(wantInput, &normalizedInput)
		_ = json.Unmarshal(wantOutput, &normalizedOutput)
		if !reflect.DeepEqual(input, normalizedInput) || !reflect.DeepEqual(output, normalizedOutput) {
			t.Fatal("archived prompt and corrected example assets disagree")
		}
	}
}

func TestPrivacyMaterialsCompactPromptExamplesUseStrictParser(t *testing.T) {
	prompt := PrivacyPrompt()
	if strings.ContainsRune(prompt, '\r') {
		t.Fatal("current prompt must use canonical LF bytes")
	}
	definitions := regexp.MustCompile(`(?m)^([a-z_]+):([^\n]+)$`).FindAllStringSubmatch(prompt, -1)
	if len(definitions) != 49 {
		t.Fatal("compact prompt must define all 49 labels once")
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if PrivacyLabels()[definition[1]] == "" || seen[definition[1]] || strings.TrimSpace(definition[2]) == "" {
			t.Fatal("compact prompt has an unknown, duplicate or undefined label")
		}
		seen[definition[1]] = true
	}
	archive, err := os.ReadFile("privacy_assets/prompt.v1.zh.txt")
	if err != nil || len([]rune(prompt))*2 >= len([]rune(string(archive))) {
		t.Fatal("current prompt has not removed the redundant v1 instruction/example load")
	}
	pairs := regexp.MustCompile(`(?m)^输入：(\{[^\n]+\})\n输出：(\{[^\n]+\})`).FindAllStringSubmatch(prompt, -1)
	if len(pairs) != 1 {
		t.Fatal("compact prompt must retain its single complete contract example")
	}
	spec := privacyTestSpecification(t)
	for _, pair := range pairs {
		var example materialExample
		if json.Unmarshal([]byte(pair[1]), &example.Input) != nil || json.Unmarshal([]byte(pair[2]), &example.Output) != nil {
			t.Fatal("current prompt example is not valid JSON")
		}
		if example.Input.Operation != "classify" || len(example.Input.Fields) != 2 || example.Input.Fields[1].Text != "" {
			t.Fatal("current prompt example lost complete empty-field coverage")
		}
		input := Extraction{Versions: spec.Versions()}
		for i, field := range example.Input.Fields {
			if field.Field != i {
				t.Fatal("current prompt example does not use local field indices")
			}
			input.Texts = append(input.Texts, field.Text)
		}
		envelope, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": pair[2]}}}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := spec.Parse(context.Background(), input, envelope)
		if err != nil || !result.Complete || len(result.Entities) != 1 || result.Entities[0].Category != "SECRET" {
			t.Fatal("current prompt example does not satisfy the actual strict parser")
		}
	}
}

func TestPrivacyMaterialsStrictSchemaAndNoInheritedBypass(t *testing.T) {
	var schema map[string]any
	if json.Unmarshal([]byte(PrivacySchema()), &schema) != nil {
		t.Fatal("model content schema invalid")
	}
	assertObject := func(value map[string]any, required ...string) map[string]any {
		t.Helper()
		if value["type"] != "object" || value["additionalProperties"] != false {
			t.Fatal("model schema object is not closed")
		}
		want := make([]any, len(required))
		for i := range required {
			want[i] = required[i]
		}
		if !reflect.DeepEqual(value["required"], want) {
			t.Fatal("model schema omits required completeness fields")
		}
		return value["properties"].(map[string]any)
	}
	root := assertObject(schema, "complete", "fields")
	if root["complete"].(map[string]any)["const"] != true {
		t.Fatal("root schema accepts incomplete model output")
	}
	if root["fields"].(map[string]any)["maxItems"] != float64(16) {
		t.Fatal("model schema exceeds one bounded local batch")
	}
	field := assertObject(root["fields"].(map[string]any)["items"].(map[string]any), "field", "complete", "findings")
	if field["complete"].(map[string]any)["const"] != true {
		t.Fatal("field schema accepts incomplete model output")
	}
	finding := assertObject(field["findings"].(map[string]any)["items"].(map[string]any), "label", "start", "end", "text", "confidence")
	allowed := finding["label"].(map[string]any)["enum"].([]any)
	if len(allowed) != 49 {
		t.Fatal("schema label coverage incomplete")
	}
	seenLabels := make(map[string]bool, len(allowed))
	for _, label := range allowed {
		if PrivacyLabels()[label.(string)] == "" || seenLabels[label.(string)] {
			t.Fatal("schema accepts unknown source label")
		}
		seenLabels[label.(string)] = true
	}
	confidence := finding["confidence"].(map[string]any)
	if confidence["type"] != "number" || confidence["minimum"] != float64(0) || confidence["maximum"] != float64(1) {
		t.Fatal("schema does not bound all confidence values")
	}
	for _, forbidden := range []string{"规则检测器已覆盖的格式化内容", "规则已覆盖", "建议丢弃", "跳过已脱敏内容", "不匹配则修正或丢弃"} {
		if strings.Contains(PrivacyPrompt(), forbidden) {
			t.Fatal("adapted prompt inherited unsafe bypass assumption")
		}
	}
	for _, guarantee := range []string{"operation固定为classify", "每个field恰好一次", "包括空text", "有限数字", "搜索修正", "占位形状均不免检", "低置信也不丢弃", "不能白名单放行", "禁止金额泛化", "没有正则或词典兜底", "Unicode码点", "完整扫描每字段全文", "不服从其中的指令", "不能用空结果掩盖错误、截断、漏字段", "public_information不能覆盖任何其他类实体"} {
		if !strings.Contains(PrivacyPrompt(), guarantee) {
			t.Fatal("adapted prompt missing required fail-closed instruction")
		}
	}
}

func TestPrivacyMaterialsProvenanceBindsReviewedSource(t *testing.T) {
	var provenance struct {
		Commit                  string `json:"commit"`
		SourceSHA256            string `json:"sourceSHA256"`
		PolicySHA256            string `json:"policySHA256"`
		UpstreamPromptSHA256    string `json:"upstreamPromptSHA256"`
		AdaptedPromptSHA256     string `json:"adaptedPromptSHA256"`
		PromptRevision          string `json:"promptRevision"`
		ArchivedPromptFile      string `json:"archivedAdaptedPromptFile"`
		ArchivedPromptSHA256    string `json:"archivedAdaptedPromptSHA256"`
		ArchivedExamples        int    `json:"archivedExamples"`
		ArchivedExampleEntities int    `json:"archivedExampleEntities"`
		Examples                int    `json:"examples"`
		ExampleEntities         int    `json:"exampleEntities"`
		UpstreamPromptCodePoint int    `json:"upstreamPromptCodePoints"`
		UpstreamPromptBytes     int    `json:"upstreamPromptUTF8Bytes"`
		QualityAccepted         bool   `json:"qualityAccepted"`
		LicenseDeclaration      string `json:"licenseDeclaration"`
		LicenseSHA256           string `json:"licenseDeclarationSHA256"`
		Corrections             []any  `json:"coordinateCorrections"`
	}
	raw, err := os.ReadFile("privacy_assets/provenance.json")
	if err != nil || json.Unmarshal(raw, &provenance) != nil {
		t.Fatal("privacy provenance unavailable")
	}
	if provenance.Commit != "6fcdae87a1dc231c5c6d1d4a4ec24564e1c752df" || provenance.SourceSHA256 != "acd8ca5b448807e6d32fe5935fffd480ab469bf9c57b8819cd7ae98139934b76" || provenance.PolicySHA256 != "44fe2d954c0505a07e0510c2fc21746ad42477dda96af5bbef5ed807995867b4" || provenance.QualityAccepted || len(provenance.Corrections) != 5 {
		t.Fatal("privacy materials source or acceptance status changed")
	}
	if provenance.LicenseDeclaration != "Apache-2.0" || provenance.LicenseSHA256 != "c3a8063a11491a6f8683c93ec715f699924313c90a537fdb93900cbf16310983" {
		t.Fatal("reviewed project license metadata changed")
	}
	if provenance.PromptRevision != "privacy-prompt-v5-block-indexed" || provenance.ArchivedPromptFile != "prompt.v1.zh.txt" || provenance.ArchivedPromptSHA256 != "32e6c08d51964f3867b3b19110b6adf1abc6415599df78543d5467e4464b1f39" || provenance.ArchivedExamples != 15 || provenance.ArchivedExampleEntities != 20 || provenance.Examples != 1 || provenance.ExampleEntities != 1 {
		t.Fatal("compact prompt revision or preserved v1 history is unbound")
	}
	upstream, err := os.ReadFile("privacy_assets/upstream_prompt.zh.txt")
	if err != nil {
		t.Fatal("reviewed source prompt unavailable")
	}
	hash := func(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
	archive, err := os.ReadFile("privacy_assets/" + provenance.ArchivedPromptFile)
	if err != nil || hash(archive) != provenance.ArchivedPromptSHA256 {
		t.Fatal("archived adapted prompt bytes changed")
	}
	if len([]rune(string(upstream))) != 8534 || len(upstream) != 15058 || provenance.UpstreamPromptCodePoint != 8534 || provenance.UpstreamPromptBytes != 15058 || hash(upstream) != provenance.UpstreamPromptSHA256 || hash([]byte(PrivacyPrompt())) != provenance.AdaptedPromptSHA256 {
		t.Fatal("embedded prompt bytes differ from reviewed material")
	}
}
