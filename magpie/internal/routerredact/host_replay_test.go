package routerredact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type hostReplayFixture struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
	body   []byte
}

func hostReplayFixtures(t *testing.T) []hostReplayFixture {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join("testdata", "round2-hosts", "manifest.json"))
	if err != nil {
		t.Fatal("host fixture manifest unavailable")
	}
	var fixtures []hostReplayFixture
	if json.Unmarshal(manifest, &fixtures) != nil || len(fixtures) != 8 {
		t.Fatal("host fixture manifest invalid")
	}
	for i := range fixtures {
		fixture := &fixtures[i]
		fixture.body, err = os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(fixture.Source)))
		if err != nil {
			t.Fatalf("host fixture unavailable: %s", fixture.ID)
		}
		hash := sha256.Sum256(fixture.body)
		if hex.EncodeToString(hash[:]) != fixture.SHA256 {
			t.Fatalf("host fixture provenance changed: %s", fixture.ID)
		}
	}
	return fixtures
}

func hostReplayInput(fixture hostReplayFixture) Input {
	in := testInput()
	in.RequestID, in.AppID = "host-replay", "fixture-host"
	in.SessionID, in.SessionVerified = "", false
	in.Body = append([]byte(nil), fixture.body...)
	return in
}

// Normalize only actual function-call arguments. Their JSON string encoding may
// change whitespace/key order while types, property names and values stay equal.
func hostReplayDocument(body []byte) (map[string]any, error) {
	v, err := wireDecode(body, 8<<20)
	if err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, fail("wire_invalid")
	}
	for _, value := range doc["messages"].([]any) {
		message := value.(map[string]any)
		calls, _ := message["tool_calls"].([]any)
		for _, value := range calls {
			function := value.(map[string]any)["function"].(map[string]any)
			arguments, ok := function["arguments"].(string)
			if !ok {
				return nil, fail("wire_invalid")
			}
			function["arguments"], err = wireDecode([]byte(arguments), 8<<20)
			if err != nil {
				return nil, err
			}
		}
	}
	return doc, nil
}

// This is also the stable old-wire overlay negative check. Safe, already masked
// captures need no recognized entities; Complete:true is an explicit synthetic
// acknowledgement, never production recognition or host acceptance evidence.
func TestHostReplayRoundTwoFixtures(t *testing.T) {
	for _, fixture := range hostReplayFixtures(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			p := New(Options{Extractor: synthetic()})
			defer p.Close()
			prepared, err := p.Prepare(context.Background(), hostReplayInput(fixture))
			if err != nil {
				t.Fatalf("safe host fixture blocked: %s", SafeFailureCode(err.Error()))
			}
			defer prepared.Close()
			body, err := prepared.Body()
			if err != nil {
				t.Fatal("processed host body unavailable")
			}
			want, wantErr := hostReplayDocument(fixture.body)
			got, gotErr := hostReplayDocument(body)
			if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
				t.Fatal("host request fields, types or call association changed")
			}
		})
	}
}

// Isolate each former rejection so a preceding agent field cannot hide a
// still-broken history or statistics field in a complete captured fixture.
func TestHostReplayKnownFieldShapes(t *testing.T) {
	variants := map[string]string{
		"$comment": `{"$comment":"synthetic installed-agent options","messages":[{"role":"user","content":"safe"}]}`,
		"agent":    `{"messages":[{"role":"user","content":"safe","agent":"custom-fixture-agent"}]}`,
		"usage":    `{"messages":[{"role":"assistant","content":"safe","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"requestUsageEntries":[{"inputTokens":1,"outputTokens":2,"totalTokens":3,"endpoint":"fixture-endpoint"}]}}]}`,
		"rawUsage": `{"messages":[{"role":"assistant","content":"safe","rawUsage":{"vendor":{"count":9007199254740993,"kind":"fixture","ok":true,"none":null}}}]}`,
	}
	for _, name := range []string{"messageId", "model", "requestModelId", "requestModelName", "traceId", "conversationRequestId"} {
		variants[name] = `{"messages":[{"role":"assistant","content":"safe","` + name + `":"fixture-identity"}]}`
	}
	for label, body := range variants {
		t.Run(label, func(t *testing.T) {
			p := New(Options{Extractor: synthetic()})
			defer p.Close()
			fixture := hostReplayFixture{body: []byte(body)}
			prepared, err := p.Prepare(context.Background(), hostReplayInput(fixture))
			if err != nil {
				t.Fatalf("known host field shape blocked: %s", SafeFailureCode(err.Error()))
			}
			defer prepared.Close()
			gotBody, err := prepared.Body()
			if err != nil {
				t.Fatal("known host field body unavailable")
			}
			want, wantErr := hostReplayDocument(fixture.body)
			got, gotErr := hostReplayDocument(gotBody)
			if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
				t.Fatal("known host field changed value or type")
			}
		})
	}
}

func TestHostReplayHTTPProcessedRetry(t *testing.T) {
	const marker = "SYNTHETIC_HOST_SECRET_雪"
	for _, fixture := range hostReplayFixtures(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			// Alter exactly one ordinary text field; protected host identities,
			// captured tool history and every unknown-free field remain present.
			v, err := wireDecode(fixture.body, 8<<20)
			if err != nil {
				t.Fatal("fixture JSON invalid")
			}
			doc := v.(map[string]any)
			doc["messages"].([]any)[0].(map[string]any)["content"] = marker
			body, _ := json.Marshal(doc)
			fixture.body = body
			want, _ := hostReplayDocument(body)
			want["messages"].([]any)[0].(map[string]any)["content"] = "<PERSON:1>"
			var calls atomic.Int32
			var firstBody atomic.Value
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, readErr := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
				got, parseErr := hostReplayDocument(raw)
				if readErr != nil || parseErr != nil || bytes.Contains(raw, []byte(marker)) || !reflect.DeepEqual(want, got) {
					t.Error("outbound host request leaked text or changed structure")
				}
				if calls.Add(1) == 1 {
					firstBody.Store(bytes.Clone(raw))
					w.WriteHeader(503)
					return
				}
				if !bytes.Equal(firstBody.Load().([]byte), raw) {
					t.Error("retry changed processed host body")
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"<PERSON:1>","tool_calls":[{"id":"reply_call","type":"function","function":{"name":"deliver","arguments":"{\"person\":\"<PERSON:1>\",\"n\":9007199254740993,\"ok\":true,\"nil\":null}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
			}))
			defer upstream.Close()
			var extractions atomic.Int32
			p := New(Options{Extractor: ExtractorFunc(func(ctx context.Context, extraction Extraction) (Result, error) {
				extractions.Add(1)
				return synthetic(marker)(ctx, extraction)
			})})
			defer p.Close()
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "http://host-fixture.local", bytes.NewReader(body))
			compositionFixture(p, upstream.URL, hostReplayInput(fixture)).ServeHTTP(w, r)
			if w.Code != 200 || calls.Load() != 2 || extractions.Load() != 1 {
				t.Fatalf("host HTTP composition failed: status=%d attempts=%d extractions=%d", w.Code, calls.Load(), extractions.Load())
			}
			response, err := wireDecode(w.Body.Bytes(), 8<<20)
			if err != nil {
				t.Fatal("restored host response invalid")
			}
			root := response.(map[string]any)
			choice := root["choices"].([]any)[0].(map[string]any)
			message := choice["message"].(map[string]any)
			call := message["tool_calls"].([]any)[0].(map[string]any)
			function := call["function"].(map[string]any)
			arguments, err := wireDecode([]byte(function["arguments"].(string)), 1<<20)
			wantArguments := map[string]any{"person": marker, "n": json.Number("9007199254740993"), "ok": true, "nil": nil}
			if err != nil || message["content"] != marker || call["id"] != "reply_call" || function["name"] != "deliver" || choice["finish_reason"] != "tool_calls" || !reflect.DeepEqual(arguments, wantArguments) {
				t.Fatal("restored host response lost text, types or call association")
			}
			statistics, _ := json.Marshal(p.Stats())
			if bytes.Contains(statistics, []byte(marker)) || bytes.Contains(statistics, []byte("PERSON")) {
				t.Fatal("host processing statistics exposed private data")
			}
		})
	}
}

func TestHostReplayProtectedAndInvalidZeroOutbound(t *testing.T) {
	const marker = "SYNTHETIC_HOST_SECRET"
	variants := map[string]string{
		"comment sensitive":          `{"$comment":"` + marker + `","messages":[{"role":"user","content":"safe"}]}`,
		"agent sensitive":            `{"messages":[{"role":"user","content":"safe","agent":"` + marker + `"}]}`,
		"usage endpoint sensitive":   `{"messages":[{"role":"assistant","content":"safe","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"requestUsageEntries":[{"inputTokens":1,"outputTokens":2,"totalTokens":3,"endpoint":"` + marker + `"}]}}]}`,
		"usage key sensitive":        `{"messages":[{"role":"assistant","content":"safe","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"inputTokensDetails":{"` + marker + `":1}}}]}`,
		"raw usage string sensitive": `{"messages":[{"role":"assistant","content":"safe","rawUsage":{"vendor":{"value":"` + marker + `","ok":true}}}]}`,
		"raw usage key sensitive":    `{"messages":[{"role":"assistant","content":"safe","rawUsage":{"vendor":{"` + marker + `":1}}}]}`,
		"comment wrong type":         `{"$comment":{},"messages":[{"role":"user","content":"safe"}]}`,
		"comment duplicate":          `{"$comment":"safe","$comment":"safe","messages":[{"role":"user","content":"safe"}]}`,
		"root unknown neighbor":      `{"$comment":"safe","host_unknown":true,"messages":[{"role":"user","content":"safe"}]}`,
		"identity wrong type":        `{"messages":[{"role":"assistant","content":"safe","messageId":1}]}`,
		"identity wrong role":        `{"messages":[{"role":"user","content":"safe","messageId":"safe"}]}`,
		"message unknown neighbor":   `{"messages":[{"role":"assistant","content":"safe","messageId":"safe","host_unknown":true}]}`,
		"usage wrong role":           `{"messages":[{"role":"user","content":"safe","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}}]}`,
		"usage unknown member":       `{"messages":[{"role":"assistant","content":"safe","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"unknown":1}}]}`,
		"raw usage wrong type":       `{"messages":[{"role":"assistant","content":"safe","rawUsage":[]}]}`,
		"opaque metadata":            `{"metadata":{"note":"safe"},"messages":[{"role":"user","content":"safe"}]}`,
	}
	for _, name := range []string{"messageId", "model", "requestModelId", "requestModelName", "traceId", "conversationRequestId"} {
		variants[name+" sensitive"] = `{"messages":[{"role":"assistant","content":"safe","` + name + `":"` + marker + `"}]}`
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	for label, body := range variants {
		t.Run(label, func(t *testing.T) {
			p := New(Options{Extractor: synthetic(marker)})
			defer p.Close()
			in := testInput()
			in.Body, in.SessionVerified = []byte(body), false
			r := httptest.NewRequest(http.MethodPost, "http://host-fixture.local", strings.NewReader(body))
			w := httptest.NewRecorder()
			compositionFixture(p, upstream.URL, in).ServeHTTP(w, r)
			if calls.Load() != 0 || w.Code != 503 {
				t.Fatalf("protected/invalid host input reached upstream: status=%d attempts=%d", w.Code, calls.Load())
			}
			statistics, _ := json.Marshal(p.Stats())
			if bytes.Contains(w.Body.Bytes(), []byte(marker)) || bytes.Contains(statistics, []byte(marker)) {
				t.Fatal("blocked host input exposed private data")
			}
		})
	}
}
