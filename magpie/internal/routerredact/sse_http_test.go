package routerredact

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPSSECompositionRestoresThreeProtocolToolValues(t *testing.T) {
	for _, protocol := range []Protocol{Chat, Responses, Anthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var stream strings.Builder
			arguments := `{"person":"<PERSON:1>","nested":[true,42,null]}`
			for i := range arguments {
				stream.WriteString(streamTestEvent(streamTestToolDelta(protocol, 0, arguments[i:i+1])))
			}
			stream.WriteString(streamTestEnd(protocol, false))
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if bytes.Contains(raw, []byte("Alice")) || !bytes.Contains(raw, []byte("<PERSON:1>")) {
					t.Error("SSE upstream got incorrect request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, value := range []byte(stream.String()) {
					_, _ = w.Write([]byte{value})
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()
			in := testInput("Alice")
			in.Protocol = protocol
			if protocol == Responses {
				in.Body = []byte(`{"model":"test","input":"Alice","stream":true}`)
			}
			if protocol == Anthropic {
				in.Body = []byte(`{"model":"test","messages":[{"role":"user","content":"Alice"}],"stream":true}`)
			}
			pipeline := New(Options{Extractor: synthetic("Alice")})
			defer pipeline.Close()
			module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				input := in
				input.Body = raw
				prepared, err := pipeline.Prepare(r.Context(), input)
				if err != nil {
					t.Error(err)
					http.Error(w, "prepare_failed", 503)
					return
				}
				defer prepared.Close()
				body, err := prepared.Body()
				if err != nil {
					t.Error(err)
					return
				}
				request, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream.URL, bytes.NewReader(body))
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Error(err)
					return
				}
				defer response.Body.Close()
				w.Header().Set("Content-Type", "text/event-stream")
				if err := prepared.RestoreSSE(r.Context(), protocol, response.Body, w); err != nil {
					t.Error(err)
				}
			}))
			defer module.Close()
			response, err := http.Post(module.URL, "application/json", bytes.NewReader(in.Body))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			_, tools, _ := streamTestCollect(t, protocol, string(raw))
			var tool map[string]any
			if json.Unmarshal([]byte(tools["0"]), &tool) != nil || tool["person"] != "Alice" {
				t.Fatal("HTTP tool did not receive restored arguments")
			}
			values := tool["nested"].([]any)
			if values[0] != true || values[1] != float64(42) || values[2] != nil {
				t.Fatal("HTTP tool argument types changed")
			}
		})
	}
}
