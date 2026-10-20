package routerredact

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCountTokensUsesPreparedTextAndKeepsCountSemantics(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte("Alice")) || !bytes.Contains(raw, []byte("<PERSON:1>")) {
			t.Error("count_tokens did not use processed body")
		}
		_, _ = io.WriteString(w, `{"input_tokens":17}`)
	}))
	defer upstream.Close()
	in := testInput()
	in.Protocol = Anthropic
	in.Operation = "count_tokens"
	in.Body = []byte(`{"model":"test","system":"Alice","messages":[{"role":"user","content":"Alice"}]}`)
	p := New(Options{Extractor: synthetic("Alice")})
	defer p.Close()
	request := httptest.NewRequest(http.MethodPost, "http://module.local", bytes.NewReader(in.Body))
	response := httptest.NewRecorder()
	compositionFixture(p, upstream.URL, in).ServeHTTP(response, request)
	if response.Code != 200 || calls.Load() != 1 || response.Body.String() != `{"input_tokens":17}` {
		t.Fatal("count semantics changed", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "redaction") {
		t.Fatal("private model response statistics")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	response = httptest.NewRecorder()
	compositionFixture(p, upstream.URL, in).ServeHTTP(response, request.WithContext(canceled))
	if calls.Load() != 1 || response.Code != 503 {
		t.Fatal("canceled count_tokens dispatched")
	}
}
