package routerredact

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestInstalledHostFieldContractPrivacyPreservesEveryField(t *testing.T) {
	raw, err := os.ReadFile("../routercloud/testdata/host-fields/installed-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			ID   string
			Body json.RawMessage
		}
	}
	if json.Unmarshal(raw, &fixtures) != nil || len(fixtures.Cases) == 0 {
		t.Fatal("invalid host field contract")
	}
	for _, fixture := range fixtures.Cases {
		t.Run(fixture.ID, func(t *testing.T) {
			doc, err := parseRequest(Chat, fixture.Body)
			if err != nil {
				t.Fatal(err)
			}
			out, err := doc.render(doc.texts)
			if err != nil {
				t.Fatal(err)
			}
			// Function arguments are JSON strings and the privacy seam may
			// canonicalize their encoding. Compare their parsed values while
			// retaining exact comparisons for every other field/string.
			want, wantErr := hostReplayDocument(fixture.Body)
			got, gotErr := hostReplayDocument(out)
			if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
				t.Fatal("host field/type/call linkage changed during privacy preparation")
			}
		})
	}
}

func TestInstalledHostMetadataNeverBypassesPrivacyScan(t *testing.T) {
	for _, field := range []string{
		`"user":"SyntheticHostSecret","messages":[{"role":"user","content":"safe"}]`,
		`"promptCacheKey":"SyntheticHostSecret","messages":[{"role":"user","content":"safe"}]`,
		`"messages":[{"role":"user","content":"safe","teammateMessage":{"nested":[{"SyntheticHostSecret":1}]}}]`,
		`"messages":[{"role":"assistant","content":"safe","permissionDecisionReason":"SyntheticHostSecret"}]`,
		`"messages":[{"role":"assistant","content":"safe","error":{"message":"SyntheticHostSecret"}}]`,
		`"messages":[{"role":"assistant","content":"safe","extra_fields":{"vendor":"SyntheticHostSecret"}}]`,
		`"messages":[{"role":"assistant","content":[{"type":"text","text":"safe","extra_fields":{"vendor":"SyntheticHostSecret"}}]}]`,
	} {
		wireAssertProtectedHostSecret(t, []byte(`{`+field+`}`), "SyntheticHostSecret")
	}
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"refusal","refusal":"SyntheticHostSecret"}]}]}`)
	doc, err := parseRequest(Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	texts := append([]string(nil), doc.texts...)
	for i, value := range texts {
		if value == "SyntheticHostSecret" {
			texts[i] = "<SECRET:1>"
		}
	}
	out, err := doc.render(texts)
	if err != nil || bytes.Contains(out, []byte("SyntheticHostSecret")) || !bytes.Contains(out, []byte("<SECRET:1>")) {
		t.Fatal("refusal text escaped privacy processing")
	}
}
