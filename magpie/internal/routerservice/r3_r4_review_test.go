package routerservice

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type r3ChunkReader struct {
	first   bool
	waiting chan struct{}
	release chan struct{}
}

func (r *r3ChunkReader) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		return copy(p, []byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")), nil
	}
	close(r.waiting)
	<-r.release
	return 0, io.EOF
}
func (r *r3ChunkReader) Close() error { return nil }

func TestR3LocalSSEForwardsBeforeEOF(t *testing.T) {
	reader := &r3ChunkReader{waiting: make(chan struct{}), release: make(chan struct{})}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
	w := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() { done <- writeLocalResponse(w, resp, "chat", []byte(`{"stream":true}`), modelWriterForTest(w)) }()
	<-reader.waiting
	if w.Body.Len() == 0 {
		t.Fatal("first complete SSE event withheld until upstream EOF")
	}
	close(reader.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestR3RedactionSSEUsesOneContinuousStream(t *testing.T) {
	w := httptest.NewRecorder()
	var calls int
	restore := func(_ context.Context, _ string, reader io.Reader, writer io.Writer) error {
		calls++
		body, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		_, err = writer.Write(append([]byte("restored:"), body...))
		return err
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- restore(context.Background(), "chat", reader, w)
	}()
	_, _ = writer.Write([]byte("data: first\n\n"))
	_, _ = writer.Write([]byte("data: second\n\n"))
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Contains(w.Body.Bytes(), []byte("data: first")) || !bytes.Contains(w.Body.Bytes(), []byte("data: second")) {
		t.Fatalf("continuous stream was not restored once: calls=%d body=%q", calls, w.Body.String())
	}
}

func TestR4LocalToolCallsSurviveProtocolConversion(t *testing.T) {
	body := `{"id":"local","choices":[{"message":{"role":"assistant","content":"working","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`
	for _, protocol := range []string{"responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			w := httptest.NewRecorder()
			if err := writeLocalResponse(w, resp, protocol, []byte(`{"stream":false}`), modelWriterForTest(w)); err != nil {
				t.Fatal(err)
			}
			out := w.Body.String()
			wants := []string{"call-1", "read", "a.txt"}
			if protocol == "responses" {
				wants = append(wants, "function_call")
			} else {
				wants = append(wants, "tool_use")
			}
			for _, want := range wants {
				if !strings.Contains(out, want) {
					t.Fatalf("converted %s response lost %q: %s", protocol, want, out)
				}
			}
			if !strings.Contains(out, "usage") || (protocol == "responses" && !strings.Contains(out, "function_call")) || (protocol == "anthropic" && !strings.Contains(out, "tool_use")) {
				t.Fatalf("converted %s response lost end/usage semantics: %s", protocol, out)
			}
		})
	}
}
