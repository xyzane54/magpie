package routercloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tokenCountRequest() Request {
	return Request{Protocol: "anthropic", Target: configured("https://upstream.test", "anthropic"), Body: []byte(`{
		"model":"incoming-selector",
		"system":[{"type":"text","text":"system instructions"}],
		"messages":[
			{"role":"user","content":"use run"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"run","input":{"n":9007199254740993}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"done"}]}
		],
		"tools":[{"name":"run","description":"a function","input_schema":{"type":"object","properties":{"image":{"type":"string"},"n":{"type":"integer"}}}}],
		"tool_choice":{"type":"auto"},
		"thinking":{"type":"disabled"},
		"output_config":{"effort":"high"}
	}`)}
}

func countError(t *testing.T, err error, status int) {
	t.Helper()
	var safe *Error
	if !errors.As(err, &safe) || safe.Status != status || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe or incorrect error: %#v", err)
	}
}

func TestPrepareTokenCountPinsModelAndPreservesInputs(t *testing.T) {
	in := tokenCountRequest()
	original := bytes.Clone(in.Body)
	body, err := prepareTokenCount(in)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]json.RawMessage
	json.Unmarshal(in.Body, &want)
	json.Unmarshal(body, &got)
	if string(got["model"]) != `"pinned"` || len(got) != len(want) || !bytes.Equal(in.Body, original) {
		t.Fatalf("target escaped or request changed: %s", body)
	}
	for key, raw := range want {
		if key == "model" {
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), got[key]) {
			t.Fatalf("count input %s changed: %s", key, got[key])
		}
	}
	delete(want, "model")
	in.Body, _ = json.Marshal(want)
	if body, err := prepareTokenCount(in); err != nil || !bytes.Contains(body, []byte(`"model":"pinned"`)) {
		t.Fatalf("omitted incoming model rejected: %s %v", body, err)
	}
}

func TestPrepareTokenCountRejectsUnsupportedProtocols(t *testing.T) {
	for _, source := range []string{"chat", "responses", "unknown"} {
		in := tokenCountRequest()
		in.Protocol = source
		body, err := prepareTokenCount(in)
		countError(t, err, http.StatusNotImplemented)
		if body != nil {
			t.Fatal("unsupported protocol produced an outgoing body")
		}
	}
	for _, target := range []string{"chat", "responses", "unknown"} {
		in := tokenCountRequest()
		in.Target.Provider.Config.Protocol = target
		body, err := prepareTokenCount(in)
		countError(t, err, http.StatusNotImplemented)
		if body != nil {
			t.Fatal("unsupported target produced an outgoing body")
		}
	}
}

func TestPrepareTokenCountRejectsExtraAndMediaFields(t *testing.T) {
	for _, extra := range []string{
		`"max_tokens":32`, `"stream":false`, `"stream":true`, `"temperature":0`, `"top_p":0.8`, `"stop_sequences":["end"]`,
		`"secret_option":"secret"`, `"speed":"fast"`,
		`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://secret.test"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","data":"secret"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"image","source":{"type":"url","url":"https://secret.test"}}]}]}]`,
		`"tools":[{"type":"web_search_20250305","name":"web_search"}]`,
	} {
		t.Run(extra, func(t *testing.T) {
			in := tokenCountRequest()
			var object, changed map[string]json.RawMessage
			json.Unmarshal(in.Body, &object)
			json.Unmarshal([]byte("{"+extra+"}"), &changed)
			for key, value := range changed {
				object[key] = value
			}
			in.Body, _ = json.Marshal(object)
			body, err := prepareTokenCount(in)
			countError(t, err, http.StatusBadRequest)
			if body != nil {
				t.Fatal("invalid input produced an outgoing body")
			}
		})
	}
	for _, invalid := range []string{`null`, `[]`, `{"messages":[]} {}`, `{"messages":[{"role":"user","content":"a"}],"messages":[{"role":"user","content":"secret"}]}`} {
		in := tokenCountRequest()
		in.Body = []byte(invalid)
		_, err := prepareTokenCount(in)
		countError(t, err, http.StatusBadRequest)
	}
}

func TestReplyTokenCountPreservesActualIntegerAndDropsOtherFields(t *testing.T) {
	for _, count := range []string{"0", "17", "9007199254740993", "92233720368547758080"} {
		w := httptest.NewRecorder()
		committed, err := replyTokenCount(w, "application/json; charset=utf-8", strings.NewReader(`{"input_tokens":`+count+`,"debug":"secret"}`))
		if err != nil || !committed || w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"input_tokens":`+count+`}` {
			t.Fatalf("actual count lost: %t %v %s", committed, err, w.Body.String())
		}
	}
}

func TestReplyTokenCountRejectsInvalidCountsWithoutOutput(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"input_tokens":-1}`, `{"input_tokens":null}`, `{"input_tokens":"3"}`, `{"input_tokens":true}`,
		`{"input_tokens":1.5}`, `{"input_tokens":1e0}`, `{"input_tokens":[]}`, `{"input_tokens":{}}`,
		`{"input_tokens":0} {}`, `{"input_tokens":0,"input_tokens":1}`, `{"input_tokens":`,
		`{"input_tokens":0,"error":{"message":"secret"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := httptest.NewRecorder()
			committed, err := replyTokenCount(w, "application/json", strings.NewReader(body))
			countError(t, err, http.StatusBadGateway)
			if committed || w.Body.Len() != 0 {
				t.Fatal("invalid count committed a response")
			}
		})
	}
	for _, contentType := range []string{"", "text/plain", "text/event-stream", "application/json; invalid"} {
		w := httptest.NewRecorder()
		committed, err := replyTokenCount(w, contentType, strings.NewReader(`{"input_tokens":0}`))
		countError(t, err, http.StatusBadGateway)
		if committed || w.Body.Len() != 0 {
			t.Fatal("non-JSON count committed a response")
		}
	}
}

func TestReplyTokenCountBodyLimit(t *testing.T) {
	base := `{"input_tokens":0,"padding":""}`
	body := strings.Replace(base, `"padding":""`, `"padding":"`+strings.Repeat("x", tokenCountReplyLimit-len(base))+`"`, 1)
	for _, extra := range []string{"", " "} {
		w := httptest.NewRecorder()
		committed, err := replyTokenCount(w, "application/json", strings.NewReader(body+extra))
		if extra == "" {
			if err != nil || !committed || w.Body.String() != `{"input_tokens":0}` {
				t.Fatalf("exact limit rejected: %t %v", committed, err)
			}
		} else {
			countError(t, err, http.StatusBadGateway)
			if committed || w.Body.Len() != 0 {
				t.Fatal("oversized count response committed output")
			}
		}
	}
}

type countReadError struct{ io.Reader }

func (r countReadError) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if err == io.EOF {
		return 0, errors.New("https://secret.test/?key=secret")
	}
	return n, err
}

type countWriteError struct {
	header http.Header
	short  bool
}

func (w *countWriteError) Header() http.Header { return w.header }
func (*countWriteError) WriteHeader(int)       {}
func (w *countWriteError) Write(b []byte) (int, error) {
	if w.short {
		return len(b) - 1, nil
	}
	return 0, errors.New("https://secret.test/?key=secret")
}

func TestReplyTokenCountReadAndWriteErrorsAreSafe(t *testing.T) {
	w := httptest.NewRecorder()
	committed, err := replyTokenCount(w, "application/json", countReadError{strings.NewReader(`{"input_tokens":0}`)})
	countError(t, err, http.StatusBadGateway)
	if committed || w.Body.Len() != 0 {
		t.Fatal("read error committed output")
	}
	for _, short := range []bool{false, true} {
		w := &countWriteError{header: http.Header{}, short: short}
		committed, err := replyTokenCount(w, "application/json", strings.NewReader(`{"input_tokens":0}`))
		countError(t, err, http.StatusBadGateway)
		if !committed {
			t.Fatal("write attempt was not recorded as committed")
		}
	}
}
