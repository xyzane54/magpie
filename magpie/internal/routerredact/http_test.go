package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/routerruntime"
)

// compositionFixture is a module-level HTTP integration fixture. W4 still owns
// production routing and the cloud retry engine. The fixture has no bypass bit.
func compositionFixture(p *Pipeline, upstream string, in Input) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
		if err != nil {
			http.Error(w, "fixture_input", 400)
			return
		}
		in.Body = raw
		prepared, err := p.Prepare(r.Context(), in)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		defer prepared.Close()
		for attempt := 0; attempt < 2; attempt++ {
			body, err := prepared.Body()
			if err != nil {
				http.Error(w, "fixture_released", 500)
				return
			}
			request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, bytes.NewReader(body))
			if err != nil {
				http.Error(w, "fixture_transport", 500)
				return
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				http.Error(w, "fixture_transport", 502)
				return
			}
			raw, err = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
			response.Body.Close()
			if err != nil {
				http.Error(w, "fixture_response", 502)
				return
			}
			if response.StatusCode == 503 && attempt == 0 {
				continue
			}
			if in.Operation == "count_tokens" {
				w.WriteHeader(response.StatusCode)
				_, _ = w.Write(raw)
				return
			}
			restored, err := prepared.RestoreResponse(in.Protocol, raw)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(restored)
			return
		}
	}
}

func TestHTTPFailureBlocksEveryCloudComposition(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	cases := map[string]Options{
		"missing":             {},
		"runtime unavailable": {Extractor: NewRuntimeExtractor(&fakeRuntime{}, syntheticSpecification{})},
		"queue full":          {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, routerruntime.ErrQueueFull })},
		"execution failed":    {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, errors.New("Alice secret") })},
		"incomplete":          {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, nil })},
		"invalid": {Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) {
			return Result{true, []Entity{{0, 0, 1, "wrong", "PERSON"}}}, nil
		})},
		"timeout": {Budget: time.Millisecond, Extractor: ExtractorFunc(func(c context.Context, _ Extraction) (Result, error) { <-c.Done(); return Result{Complete: true}, nil })},
	}
	for reason, options := range cases {
		for _, branch := range []string{"normal", "judgment-failure", "capability-mismatch", "local-fallback", "same-target-retry", "count_tokens"} {
			t.Run(reason+"/"+branch, func(t *testing.T) {
				p := New(options)
				defer p.Close()
				in := testInput("Alice")
				if branch == "count_tokens" {
					in.Operation = "count_tokens"
				}
				request := httptest.NewRequest(http.MethodPost, "http://module.local", bytes.NewReader(in.Body))
				response := httptest.NewRecorder()
				compositionFixture(p, upstream.URL, in).ServeHTTP(response, request)
				if response.Code != 503 || calls.Load() != 0 {
					t.Fatal("failed redaction reached upstream", response.Code, calls.Load())
				}
				if strings.Contains(response.Body.String(), "Alice") || strings.Contains(response.Body.String(), "secret") {
					t.Fatal("error leaked source")
				}
			})
		}
	}
	p := New(Options{Extractor: synthetic("Alice")})
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := testInput("Alice")
	request := httptest.NewRequest(http.MethodPost, "http://module.local", bytes.NewReader(in.Body)).WithContext(ctx)
	response := httptest.NewRecorder()
	compositionFixture(p, upstream.URL, in).ServeHTTP(response, request)
	if calls.Load() != 0 || response.Code != 503 {
		t.Fatal("canceled request reached upstream")
	}
}

func TestHTTPSuccessRetryRestoresNestedToolArguments(t *testing.T) {
	var calls atomic.Int32
	var extractCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("Alice")) {
			t.Error("original reached cloud")
		}
		if !bytes.Contains(body, []byte("<PERSON:1>")) {
			t.Error("processed text absent")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Hello <PERSON:1>","tool_calls":[{"id":"call_1","type":"function","function":{"name":"deliver","arguments":"{\"nested\":{\"user\":\"<PERSON:1>\",\"signature\":\"<PERSON:1>\"},\"n\":9007199254740993,\"ok\":true,\"nil\":null}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":9}}`)
	}))
	defer upstream.Close()
	p := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
		extractCalls.Add(1)
		return synthetic("Alice")(c, in)
	})})
	defer p.Close()
	in := testInput("Alice")
	server := httptest.NewServer(compositionFixture(p, upstream.URL, in))
	defer server.Close()
	response, err := http.Post(server.URL, "application/json", bytes.NewReader(in.Body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || calls.Load() != 2 || extractCalls.Load() != 1 {
		t.Fatal("retry changed preparation", response.StatusCode, string(raw))
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil {
		t.Fatal("invalid response")
	}
	choice := result["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	call := message["tool_calls"].([]any)[0].(map[string]any)
	function := call["function"].(map[string]any)
	if message["content"] != "Hello Alice" || call["id"] != "call_1" || function["name"] != "deliver" || choice["finish_reason"] != "tool_calls" {
		t.Fatal("semantics changed")
	}
	args := function["arguments"].(string)
	if !strings.Contains(args, `"user":"Alice"`) || !strings.Contains(args, `"signature":"Alice"`) || !strings.Contains(args, `9007199254740993`) || !strings.Contains(args, `"ok":true`) {
		t.Fatal("tool params not restored", args)
	}
	stats, _ := json.Marshal(p.Stats())
	if bytes.Contains(stats, []byte("Alice")) || bytes.Contains(stats, []byte("PERSON")) {
		t.Fatal("private statistics")
	}
}
