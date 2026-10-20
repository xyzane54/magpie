package routerredact

import "testing"

func TestWireToolResultScansAllMetadataStringsAndKeys(t *testing.T) {
	for _, field := range []string{
		`"toolResult":{"content":"SyntheticHostSecret","title":"result","renderer":{"type":"text"}}`,
		`"toolResult":{"content":"result","title":"SyntheticHostSecret"}`,
		`"toolResult":{"nested":[{"SyntheticHostSecret":true}]}`,
		`"toolResult":{"nested":[{"value":"SyntheticHostSecret"}]}`,
	} {
		wireAssertProtectedHostSecret(t, []byte(`{"messages":[{"role":"tool","tool_call_id":"call_a","content":"safe",`+field+`}]}`), "SyntheticHostSecret")
	}
}
