package cloudcodec

import (
	"encoding/json"
	"strings"
	"testing"
)

// This is a positive cross-protocol check for the portable subset used by the
// local execution seam.  The router must not maintain a second converter that
// drops one of the tool results or the user's text after that result.
func TestCloudCodecAnthropicToChatPreservesToolHistoryAndControls(t *testing.T) {
	const source = `{
  "model":"client-model",
  "max_tokens":32,
  "tool_choice":{"type":"auto","disable_parallel_tool_use":true},
  "output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}}},
  "messages":[
    {"role":"user","content":"read both files"},
    {"role":"assistant","content":[
      {"type":"tool_use","id":"call-1","name":"read","input":{"path":"one.txt"}},
      {"type":"tool_use","id":"call-2","name":"read","input":{"path":"two.txt"}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"call-1","content":"first-result"},
      {"type":"tool_result","tool_use_id":"call-2","content":"second-result"},
      {"type":"text","text":"compare both results"}
    ]}
  ],
  "tools":[{"name":"read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]
}`
	c, err := NewCloudCodec("anthropic", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	converted, err := c.Request("chat", "local-model", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
		ParallelToolCalls *bool           `json:"parallel_tool_calls"`
		ResponseFormat    json.RawMessage `json:"response_format"`
	}
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	if request.ParallelToolCalls == nil || *request.ParallelToolCalls {
		t.Fatalf("disable_parallel_tool_use was not preserved: %s", converted)
	}
	if !strings.Contains(string(request.ResponseFormat), `"status"`) || !strings.Contains(string(request.ResponseFormat), `"json_schema"`) {
		t.Fatalf("structured output format was not preserved: %s", converted)
	}
	results := map[string]string{}
	calls := map[string]bool{}
	followup := false
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = true
		}
		if message.Role == "tool" {
			results[message.ToolCallID] = message.Content
		}
		if message.Role == "user" && message.Content == "compare both results" {
			followup = true
		}
	}
	if !calls["call-1"] || !calls["call-2"] || results["call-1"] != "first-result" || results["call-2"] != "second-result" || !followup {
		t.Fatalf("tool history or same-turn text was lost: %s", converted)
	}
}

func TestCloudCodecAnthropicIsErrorToolResultRejectsChatTranslation(t *testing.T) {
	const source = `{
  "model":"client-model",
  "max_tokens":32,
  "messages":[
    {"role":"user","content":"read the file"},
    {"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"path":"sample.txt"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","is_error":true,"content":"permission denied"}]}
  ],
  "tools":[{"name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]
}`
	c, err := NewCloudCodec("anthropic", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request("chat", "local-model", "example.test"); err == nil {
		t.Fatal("is_error tool result was silently converted to an ordinary Chat result")
	}
	// Native forwarding remains available to the existing cloud branch, where
	// Anthropic can express the failure marker without loss.
	if _, err := c.Request("anthropic", "cloud-model", "example.test"); err != nil {
		t.Fatalf("native request rejected: %v", err)
	}
}
