package routerredact

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// These synthetic values exercise the shapes evidenced by the fixed installed
// host sources. Full redacted capture replay lives in host_replay_test.go.
const wireHostFixture = `{
 "$comment":"maintenance comment",
 "messages":[
  {"role":"system","content":"MutableSecret system","agent":"custom system agent"},
  {"role":"user","content":"MutableSecret user","agent":"custom user agent"},
  {"role":"assistant","content":null,"agent":"custom assistant agent","messageId":"message-A","model":"history-model","requestModelId":"request-model","requestModelName":"model display","traceId":"trace-A","conversationRequestId":"conversation-A","reasoning":"MutableSecret reasoning","argumentsDisplayText":"MutableSecret display","tool_calls":[{"id":"call-A","type":"function","function":{"name":"run","arguments":"{\"path\":\"MutableSecret argument\"}"}}],"usage":{"inputTokens":1.25,"outputTokens":2,"totalTokens":3.25,"requests":1,"inputTokensDetails":{"cached_tokens":0},"outputTokensDetails":[{"reasoning_tokens":1}],"requestUsageEntries":[{"inputTokens":1.25,"outputTokens":2,"totalTokens":3.25,"endpoint":"history-endpoint"}]},"rawUsage":{"vendor_key":{"note":"vendor note","number":9007199254740993,"enabled":true,"empty":null,"items":[1,"vendor item"]}}},
  {"role":"tool","tool_call_id":"call-A","content":"MutableSecret result","agent":"custom tool agent","messageId":"message-A","model":"history-model","requestModelId":"request-model","requestModelName":"model display","traceId":"trace-A","conversationRequestId":"conversation-A"}
 ]
}`

func TestWireHostSupportedFieldsPreserveStructureAndScanContent(t *testing.T) {
	doc, err := parseRequest(Chat, []byte(wireHostFixture))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := doc.render(doc.texts)
	if err != nil {
		t.Fatal(err)
	}
	texts := append([]string(nil), doc.texts...)
	for i, text := range texts {
		texts[i] = strings.ReplaceAll(text, "MutableSecret", "<SECRET:1>")
	}
	processed, err := doc.render(texts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(processed, []byte("MutableSecret")) {
		t.Fatal("known ordinary host text escaped scanning")
	}
	// Only the exact ordinary text was changed. Restore it through the same
	// protocol text locator and prove IDs, metadata, numeric precision and
	// dynamic vendor object structure are all retained.
	again, err := parseRequest(Chat, processed)
	if err != nil {
		t.Fatal(err)
	}
	undo := append([]string(nil), again.texts...)
	for i, text := range undo {
		undo[i] = strings.ReplaceAll(text, "<SECRET:1>", "MutableSecret")
	}
	roundtrip, err := again.render(undo)
	if err != nil {
		t.Fatal(err)
	}
	wireAssertJSONEqual(t, canonical, roundtrip)
}

func TestWireHostIdentityAndAccountingStringsAreProtected(t *testing.T) {
	const secret = "SyntheticHostSecret"
	fields := []string{
		`"agent":"SyntheticHostSecret"`,
		`"messageId":"SyntheticHostSecret"`,
		`"model":"SyntheticHostSecret"`,
		`"requestModelId":"SyntheticHostSecret"`,
		`"requestModelName":"SyntheticHostSecret"`,
		`"traceId":"SyntheticHostSecret"`,
		`"conversationRequestId":"SyntheticHostSecret"`,
		`"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"requestUsageEntries":[{"inputTokens":1,"outputTokens":2,"totalTokens":3,"endpoint":"SyntheticHostSecret"}]}`,
		`"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"inputTokensDetails":{"SyntheticHostSecret":1}}`,
		`"rawUsage":{"vendor":{"text":"SyntheticHostSecret"}}`,
		`"rawUsage":{"SyntheticHostSecret":{"number":1}}`,
		`"rawUsage":{"vendor":[null,true,1,"SyntheticHostSecret"]}`,
	}
	for _, field := range fields {
		wireAssertProtectedHostSecret(t, []byte(`{"messages":[{"role":"assistant","content":"ordinary",`+field+`}]}`), secret)
	}
	wireAssertProtectedHostSecret(t, []byte(`{"$comment":"SyntheticHostSecret","messages":[{"role":"user","content":"ordinary"}]}`), secret)
}

func wireAssertProtectedHostSecret(t *testing.T, body []byte, secret string) {
	t.Helper()
	doc, err := parseRequest(Chat, body)
	if err != nil {
		t.Fatalf("evidenced host field shape rejected: %v", err)
	}
	texts := append([]string(nil), doc.texts...)
	found := false
	for i, text := range texts {
		if text == secret {
			found = true
			texts[i] = "<SECRET:1>"
		}
	}
	if !found {
		t.Fatal("host key/string bypassed extraction")
	}
	if out, err := doc.render(texts); err == nil || err.Error() != "wire_unsupported" || len(out) != 0 {
		t.Fatalf("host identity/accounting was changed or leaked: error=%v", err)
	}
}

func TestWireHostCacheIdentityIncludesAccountingNumbersAndShape(t *testing.T) {
	for _, fields := range [][2]string{
		{`"rawUsage":{"vendor":{"count":1,"note":"same"}}`, `"rawUsage":{"vendor":{"count":2,"note":"same"}}`},
		{`"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}`, `"usage":{"inputTokens":2,"outputTokens":2,"totalTokens":4}`},
		{`"rawUsage":{"vendor":{"note":"same"}}`, `"rawUsage":{"vendor":[{"note":"same"}]}`},
	} {
		parse := func(field string) *textDocument {
			doc, err := parseRequest(Chat, []byte(`{"messages":[{"role":"assistant","content":"same",`+field+`}]}`))
			if err != nil {
				t.Fatal(err)
			}
			return doc
		}
		a, b := parse(fields[0]), parse(fields[1])
		if !reflect.DeepEqual(a.texts, b.texts) {
			t.Fatal("fixture should retain identical text leaves")
		}
		if reflect.DeepEqual(a.identities, b.identities) {
			t.Fatal("changed host accounting reused cached history identity")
		}
		if repeated := parse(fields[0]); !reflect.DeepEqual(a.identities, repeated.identities) {
			t.Fatal("host identity depends on map iteration")
		}
	}
}

func TestWireHostSupportDoesNotRelaxUnknownOrInvalidFields(t *testing.T) {
	for _, body := range []string{
		`{"$comment":null,"messages":[{"role":"user","content":"text"}]}`,
		`{"$comment":"note","$comment":"duplicate","messages":[{"role":"user","content":"text"}]}`,
		`{"$unknown":"note","messages":[{"role":"user","content":"text"}]}`,
		`{"agent":"top-level","messages":[{"role":"user","content":"text"}]}`,
		`{"messages":[{"role":"user","content":"text","agent":" "}]}`,
		`{"messages":[{"role":"developer","content":"text","agent":"name"}]}`,
		`{"messages":[{"role":"user","content":"text","messageId":"id"}]}`,
		`{"messages":[{"role":"assistant","content":"text","messageId":42}]}`,
		`{"messages":[{"role":"assistant","content":"text","unknownId":"id"}]}`,
		`{"messages":[{"role":"user","content":"text","rawUsage":{}}]}`,
		`{"messages":[{"role":"assistant","content":"text","rawUsage":[]}]}`,
		`{"messages":[{"role":"assistant","content":"text","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"unknown":1}}]}`,
		`{"messages":[{"role":"assistant","content":"text","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"requestUsageEntries":[{"inputTokens":1,"outputTokens":2,"totalTokens":3,"endpoint":42}]}}]}`,
		`{"metadata":{"note":"still opaque"},"messages":[{"role":"user","content":"text"}]}`,
	} {
		if _, err := parseRequest(Chat, []byte(body)); err == nil {
			t.Errorf("invalid host variant accepted: %s", body)
		}
	}
}
