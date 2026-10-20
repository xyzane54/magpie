package routercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/routerrequest"
)

func TestInstalledHostFieldContractAcrossValidationRoutingAndNativeCloud(t *testing.T) {
	raw, err := os.ReadFile("testdata/host-fields/installed-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			ID, Host string
			Body     json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil || len(fixtures.Cases) == 0 {
		t.Fatal("invalid installed host field contract")
	}
	for _, fixture := range fixtures.Cases {
		t.Run(fixture.ID, func(t *testing.T) {
			if err := ValidateInput("chat", fixture.Body); err != nil {
				t.Fatalf("installed %s field rejected at cloud input: %v", fixture.Host, err)
			}
			p, err := routerrequest.Normalize(routerrequest.Input{RequestID: "req-field", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: fixture.Body})
			if err != nil || !bytes.Equal(p.ExecutionBody(), fixture.Body) {
				t.Fatalf("installed field rejected or changed before judgment: %v", err)
			}
			calls := 0
			e := Executor{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wire, _ := io.ReadAll(r.Body)
				var got, want map[string]any
				_ = json.Unmarshal(wire, &got)
				_ = json.Unmarshal(fixture.Body, &want)
				want["model"] = "pinned"
				if !reflect.DeepEqual(got, want) {
					t.Error("installed host field was silently dropped or rewritten")
				}
				return response(200, whole("chat", false)), nil
			})}
			result, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", fixture.Body, configured("http://fixture.invalid/v1", "chat")})
			if err != nil || calls != 1 || result.Attempts != 1 || !result.OutputCommitted {
				t.Fatalf("installed host field failed native execution: %v", err)
			}
			for _, target := range []string{"responses", "anthropic"} {
				calls = 0
				result, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", fixture.Body, configured("http://fixture.invalid/v1", target)})
				failure, ok := err.(*Error)
				if !ok || !strings.HasPrefix(failure.Code, "capability_") || calls != 0 || result.Attempts != 0 || result.OutputCommitted {
					t.Fatalf("host field escaped lossless conversion boundary to %s: calls=%d error=%v", target, calls, err)
				}
			}
		})
	}
}

func TestInstalledHostFieldsRetainTypeRoleAndStatelessBoundary(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"text"}],"frequency_penalty":"0.2"}`,
		`{"messages":[{"role":"user","content":"text"}],"presence_penalty":null}`,
		`{"messages":[{"role":"user","content":"text"}],"seed":1.5}`,
		`{"messages":[{"role":"user","content":"text"}],"seed":9223372036854775808}`,
		`{"messages":[{"role":"user","content":"text"}],"user":42}`,
		`{"messages":[{"role":"user","content":"text"}],"verbosity":"arbitrary"}`,
		`{"messages":[{"role":"user","content":"text"}],"store":true}`,
		`{"messages":[{"role":"user","content":"text"}],"prompt_cache_retention":"forever"}`,
		`{"messages":[{"role":"user","content":"text"}],"promptCacheKey":{}}`,
		`{"messages":[{"role":"user","content":"text"}],"reasoningSummary":true}`,
		`{"messages":[{"role":"user","content":"text","skipRun":"true"}]}`,
		`{"messages":[{"role":"developer","content":"text","skipRun":true}]}`,
		`{"messages":[{"role":"user","content":"text","isCompactInternal":1}]}`,
		`{"messages":[{"role":"assistant","content":"text","isMeta":true}]}`,
		`{"messages":[{"role":"assistant","content":"text","queuePosition":-1}]}`,
		`{"messages":[{"role":"assistant","content":"text","queueTotal":1.5}]}`,
		`{"messages":[{"role":"assistant","content":"text","exitCode":"0"}]}`,
		`{"messages":[{"role":"assistant","content":"text","hookEvent":null}]}`,
		`{"messages":[{"role":"assistant","content":"text","error":[]}]}`,
		`{"messages":[{"role":"user","content":"text","teammateMessage":true}]}`,
		`{"messages":[{"role":"user","content":"text","extra_fields":{}}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"text","text":"x","extra_fields":null}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"x","extra_fields":{}}]}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"refusal","refusal":42}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"refusal","refusal":"text"}]}]}`,
		`{"messages":[{"role":"user","content":"text","isMeta":true,"isMeta":false}]}`,
		`{"messages":[{"role":"assistant","content":"text","extra_fields":{"vendor":{"key":1,"key":2}}}]}`,
		`{"messages":[{"role":"user","content":"text","isMeta":true,"unknown_host_control":true}]}`,
	} {
		workBuddyAssertInvalidMetadataZeroOutbound(t, []byte(body))
		if _, err := routerrequest.Normalize(routerrequest.Input{RequestID: "req-invalid", AppID: "workbuddy", Protocol: "chat", Operation: "generate", OriginalBody: []byte(body)}); err == nil {
			t.Fatal("invalid host field reached routing judgment")
		}
	}
	// Exact integer/control values are forwarded; they are not normalized to
	// floats, silently dropped, or replaced by defaults.
	for _, seed := range []string{"-9223372036854775808", "0", "9223372036854775807"} {
		if err := ValidateInput("chat", []byte(`{"messages":[{"role":"user","content":"text"}],"seed":`+seed+`}`)); err != nil {
			t.Fatal(err)
		}
	}
}
