package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerruntime"
)

type mockOrchestratorInvoker struct {
	handler func(reqBody []byte) (string, error)
}

func (m *mockOrchestratorInvoker) Require(p routerruntime.Purpose) error {
	return nil
}

func (m *mockOrchestratorInvoker) Invoke(ctx context.Context, p routerruntime.Purpose, body []byte, fn func(*http.Response) error) error {
	content, err := m.handler(body)
	if err != nil {
		return err
	}
	respPayload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"content": content,
				},
			},
		},
	})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(respPayload)),
	}
	return fn(resp)
}

func TestRuleAndDictionaryDetectors(t *testing.T) {
	for _, def := range defaultRuleDefinitions {
		if _, err := compileRuleDefinition(def); err != nil {
			t.Fatalf("default rule %q failed to compile: %v", def.Name, err)
		}
	}
	zhDefs := parseRuleDefinitionsYAML(rulesZhYAML)
	if len(zhDefs) == 0 {
		t.Fatal("expected non-empty rules parsed from rules.zh.yaml")
	}
	for _, def := range zhDefs {
		if _, err := compileRuleDefinition(def); err != nil {
			t.Fatalf("rules.zh.yaml rule %q failed to compile: %v", def.Name, err)
		}
	}

	rules := DefaultRuleDetector()
	if len(rules.Rules()) < len(zhDefs) {
		t.Fatalf("expected all rules to be compiled, got %d of %d", len(rules.Rules()), len(zhDefs))
	}
	dict := DefaultDictionaryDetector()
	if len(dict.Entries()) == 0 {
		t.Fatal("expected non-empty default dictionary from embedded YAML assets")
	}

	// Valid vs invalid 18-digit ID card checksum
	textID := "身份证号 11010519491231002X 有效，而 110105194912310021 校验位无效。"
	spans := rules.Detect(textID)
	foundValidID := false
	for _, s := range spans {
		if s.Value == "11010519491231002X" && CanonicalizeLabel(s.Label) == "ID_CARD" {
			foundValidID = true
		}
		if s.Value == "110105194912310021" && CanonicalizeLabel(s.Label) == "ID_CARD" {
			t.Fatalf("expected invalid checksum ID card to be rejected, got %+v", s)
		}
	}
	if !foundValidID {
		t.Fatalf("expected valid ID card 11010519491231002X to be detected, got %+v", spans)
	}

	// Dictionary matching & ASCII boundary check
	dictText := "项目代号 Project Aurora 与 科大讯飞，但 myProject AuroraX 不应命中。"
	dictSpans := dict.Detect(dictText)
	if len(dictSpans) != 2 {
		t.Fatalf("expected 2 dictionary spans, got %d: %+v", len(dictSpans), dictSpans)
	}
	if dictSpans[0].Value != "Project Aurora" || dictSpans[0].CanonicalLabel != "ORG" {
		t.Fatalf("unexpected first dictionary span: %+v", dictSpans[0])
	}
	if dictSpans[1].Value != "科大讯飞" || dictSpans[1].CanonicalLabel != "ORG" {
		t.Fatalf("unexpected second dictionary span: %+v", dictSpans[1])
	}
}

func TestOrchestratorCompoundSuppressionAndLocationMerge(t *testing.T) {
	invoker := &mockOrchestratorInvoker{
		handler: func(reqBody []byte) (string, error) {
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(reqBody, &req)
			sys := ""
			if len(req.Messages) > 0 {
				sys = req.Messages[0].Content
			}
			if strings.Contains(sys, "隐私漏检复核器") {
				return `{"entities":[]}`, nil
			}
			return `<think>分析实体</think>` + "```json\n" + `{
				"entities": [
					{"value": "张伟", "label": "NAME", "confidence": 0.96},
					{"value": "安徽省", "label": "ORG", "confidence": 0.88},
					{"value": "合肥市高新区望江西路800号", "label": "ADDRESS", "confidence": 0.93},
					{"value": "公司", "label": "COMPANY", "confidence": 0.90}
				]
			}` + "\n```", nil
		},
	}

	llm := NewLocalLLMDetector(LocalLLMDetectorOptions{
		Invoker:          invoker,
		ModelID:          "qwen3-4b",
		EnableReviewPass: true,
	})
	orch := DefaultOrchestrator(llm)
	input := "联系人张伟，电话 13812345678，地址在安徽省合肥市高新区望江西路800号，数据库连接 postgres://admin:SecretPass123@10.24.1.15:5432/core_db，合同金额为 88.6万元，参考标准 GB/T 35273-2020。"

	spans, err := orch.Detect(context.Background(), input)
	if err != nil {
		t.Fatalf("Orchestrator.Detect failed: %v", err)
	}

	byLabel := make(map[string][]string)
	for _, s := range spans {
		byLabel[s.Label] = append(byLabel[s.Label], s.Value)
		if s.Value == "10.24.1.15" {
			t.Fatalf("expected inner IP 10.24.1.15 inside CONNECTION_STRING to be suppressed, got span %+v", s)
		}
		if s.Value == "GB/T 35273-2020" {
			t.Fatalf("expected PUBLIC_INFORMATION GB/T 35273-2020 to be allowed and filtered out, got %+v", s)
		}
		if s.Value == "公司" {
			t.Fatalf("expected generic org value '公司' to be filtered out, got %+v", s)
		}
	}

	if got := byLabel["NAME"]; len(got) != 1 || got[0] != "张伟" {
		t.Fatalf("expected NAME=[张伟], got %+v (all spans: %+v)", got, spans)
	}
	if got := byLabel["PHONE"]; len(got) != 1 || got[0] != "13812345678" {
		t.Fatalf("expected PHONE=[13812345678], got %+v (all spans: %+v)", got, spans)
	}
	if got := byLabel["ADDRESS"]; len(got) != 1 || got[0] != "安徽省合肥市高新区望江西路800号" {
		t.Fatalf("expected merged ADDRESS=[安徽省合肥市高新区望江西路800号], got %+v (all spans: %+v)", got, spans)
	}
	if got := byLabel["SECRET"]; len(got) != 1 || !strings.HasPrefix(got[0], "postgres://admin:SecretPass123@10.24.1.15:5432/core_db") {
		t.Fatalf("expected SECRET for postgres URI, got %+v (all spans: %+v)", got, spans)
	}
	if got := byLabel["AMOUNT"]; len(got) != 1 || got[0] != "88.6万元" {
		t.Fatalf("expected AMOUNT=[88.6万元], got %+v (all spans: %+v)", got, spans)
	}
}

func TestOrchestratorPipelineEndToEndWithAmountGeneralization(t *testing.T) {
	versions := Versions{Model: "m-1", Prompt: "p-1", Config: "c-1"}
	invoker := &mockOrchestratorInvoker{
		handler: func(reqBody []byte) (string, error) {
			return `{"entities":[{"value":"李明","label":"NAME","confidence":0.95}]}`, nil
		},
	}
	llm := NewLocalLLMDetector(LocalLLMDetectorOptions{
		Invoker: invoker,
		ModelID: "qwen3-4b",
	})
	orch := DefaultOrchestrator(llm).WithVersions(versions)
	pipeline := New(Options{Extractor: orch})
	defer pipeline.Close()

	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"请通知李明（手机 13900001111），合同金额为 88.6万元，另一个李明也知悉。"}]}`)
	prepared, err := pipeline.Prepare(context.Background(), Input{
		RequestID:       "req-1",
		AppID:           "app-1",
		Protocol:        Chat,
		Operation:       "generation",
		SessionID:       "sess-1",
		SessionVerified: true,
		Versions:        versions,
		Body:            reqBody,
	})
	if err != nil {
		t.Fatalf("pipeline.Prepare failed: %v", err)
	}
	defer prepared.Close()

	redactedBytes, err := prepared.Body()
	if err != nil {
		t.Fatalf("prepared.Body failed: %v", err)
	}
	redactedStr := string(redactedBytes)
	if strings.Contains(redactedStr, "李明") || strings.Contains(redactedStr, "13900001111") || strings.Contains(redactedStr, "88.6万元") {
		t.Fatalf("expected sensitive values to be redacted/generalized, got: %s", redactedStr)
	}
	if !strings.Contains(redactedStr, "<NAME:1>") || !strings.Contains(redactedStr, "<PHONE:1>") {
		t.Fatalf("expected <NAME:1> and <PHONE:1> placeholders, got: %s", redactedStr)
	}
	if strings.Contains(redactedStr, "<NAME:2>") {
		t.Fatalf("expected identical entity '李明' to reuse <NAME:1>, got: %s", redactedStr)
	}
	if !strings.Contains(redactedStr, "约90万元") {
		t.Fatalf("expected AMOUNT '88.6万元' to be generalized to '约90万元', got: %s", redactedStr)
	}
	if strings.Contains(redactedStr, "<AMOUNT:") {
		t.Fatalf("expected AMOUNT not to generate reversible placeholder, got: %s", redactedStr)
	}

	// Verify response restoration restores <NAME:1> and <PHONE:1> while leaving generalized amount as-is
	cloudResp := []byte(`{"choices":[{"message":{"role":"assistant","content":"已通知 <NAME:1>（<PHONE:1>），确认预算约90万元。"}}]}`)
	restored, err := prepared.RestoreResponse(Chat, cloudResp)
	if err != nil {
		t.Fatalf("RestoreResponse failed: %v", err)
	}
	if !strings.Contains(string(restored), "已通知 李明（13900001111），确认预算约90万元。") {
		t.Fatalf("unexpected restored response: %s", string(restored))
	}
}
