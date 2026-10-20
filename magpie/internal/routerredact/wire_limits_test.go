package routerredact

import (
	"encoding/json"
	"strings"
	"testing"
)

// This test deliberately uses only baseline-supported schema/argument input
// and existing document accessors, so an old-wire overlay must fail behavior.
func TestWireIdentityMemoryBound(t *testing.T) {
	key := strings.Repeat("large-business-key-", 450)
	leaves := make([]any, 1000)
	for i := range leaves {
		leaves[i] = "leaf"
	}
	args, err := json.Marshal(map[string]any{key: leaves})
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]map[string]any{
		"arguments": {"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-memory", "type": "function", "function": map[string]any{"name": "run", "arguments": string(args)}}}}}},
		"schema":    {"messages": []any{map[string]any{"role": "user", "content": "safe"}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "run", "parameters": map[string]any{"type": "object", "properties": map[string]any{key: map[string]any{"enum": leaves}}}}}}},
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := parseRequest(Chat, body)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, identity := range doc.identities {
				total += len(identity)
			}
			if total > len(doc.texts)*256 {
				t.Fatalf("cache identity storage multiplied a business key by its descendants: %d bytes for %d texts", total, len(doc.texts))
			}
			if _, err := doc.render(doc.texts); err != nil {
				t.Fatal("bounded identity changed supported rendering", err)
			}
		})
	}
}

func TestWireHostIdentityMemoryBound(t *testing.T) {
	key := strings.Repeat("large-vendor-key-", 500)
	leaves := make([]any, 1000)
	for i := range leaves {
		leaves[i] = "vendor text"
	}
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": "safe", "rawUsage": map[string]any{key: leaves}}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseRequest(Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, identity := range doc.identities {
		total += len(identity)
	}
	if total > len(doc.texts)*256 {
		t.Fatalf("vendor field amplified identities: %d bytes for %d texts", total, len(doc.texts))
	}
}

func TestWireCollectionBudgetsStopBeforeRender(t *testing.T) {
	for _, protected := range []bool{false, true} {
		doc := &textDocument{}
		for i := 0; i < 8200; i++ {
			if protected {
				doc.protectAt("small text", "test-control")
			} else {
				doc.text("small text")
			}
		}
		if len(doc.texts) > 8192 || len(doc.identities) != len(doc.texts) {
			t.Fatal("field budget retained excess text or desynchronized indices")
		}
		if _, err := doc.render(doc.texts); err == nil || err.Error() != "redaction_input_limit" {
			t.Fatal("field collection failure did not remain sticky", err)
		}
	}
	for _, protected := range []bool{false, true} {
		doc := &textDocument{}
		if protected {
			doc.protectAt(strings.Repeat("x", 8<<20), "test-control")
			doc.protectAt("overflow", "test-control")
		} else {
			doc.text(strings.Repeat("x", 8<<20))
			doc.text("overflow")
		}
		if len(doc.texts) != 1 {
			t.Fatal("text budget retained excess bytes")
		}
		if _, err := doc.render(doc.texts); err == nil || err.Error() != "redaction_input_limit" {
			t.Fatal("text collection failure did not remain sticky", err)
		}
	}
}

func TestWireParseRejectsExcessTextPositions(t *testing.T) {
	leaves := make([]any, 8200)
	for i := range leaves {
		leaves[i] = "small"
	}
	args, err := json.Marshal(map[string]any{"values": leaves})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []map[string]any{
		{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-budget", "type": "function", "function": map[string]any{"name": "run", "arguments": string(args)}}}}}},
		{"messages": []any{map[string]any{"role": "user", "content": "safe"}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "run", "parameters": map[string]any{"enum": leaves}}}}},
		{"messages": []any{map[string]any{"role": "assistant", "content": "safe", "rawUsage": map[string]any{"values": leaves}}}},
	} {
		body, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if doc, err := parseRequest(Chat, body); err == nil || err.Error() != "redaction_input_limit" || doc != nil {
			t.Fatal("parse returned an over-budget document", err)
		}
	}
}

func TestWireResponseCollectionBudgets(t *testing.T) {
	snapshot := &Snapshot{}
	defer snapshot.Close()
	for _, count := range []int{8192, 8193} {
		blocks := make([]any, count)
		for i := range blocks {
			blocks[i] = map[string]any{"type": "text", "text": ""}
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": blocks}}}})
		restored, err := snapshot.RestoreResponse(Chat, body)
		if count == 8192 {
			if err != nil {
				t.Fatal("response position boundary rejected", err)
			}
			wireAssertJSONEqual(t, body, restored)
		} else if err == nil || err.Error() != "redaction_input_limit" || len(restored) != 0 {
			t.Fatal("over-limit response returned content", err)
		}
	}
	// The request's 8 MiB collector budget must not silently narrow the
	// separately documented non-streaming response budget of 32 MiB.
	text := strings.Repeat("x", (8<<20)+1)
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}}})
	restored, err := snapshot.RestoreResponse(Chat, body)
	if err != nil {
		t.Fatal("response incorrectly uses request text budget", err)
	}
	wireAssertJSONEqual(t, body, restored)
}
