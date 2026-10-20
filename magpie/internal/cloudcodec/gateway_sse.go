package cloudcodec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// readSSE walks a server-sent event stream, calling fn with each event's
// name and data. Data lines of one event are joined with newlines.
func ReadSSE(r io.Reader, fn func(event, data string) error) error {
	return ReadSSEAlive(r, fn, nil)
}

// readSSEAlive is readSSE that also calls alive, when not nil, after each
// event and each comment: every sign that the stream is alive, a
// keepalive (": keepalive") as much as an answer.
func ReadSSEAlive(r io.Reader, fn func(event, data string) error, alive func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var event string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		if err == nil && alive != nil {
			alive()
		}
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			if alive != nil {
				alive()
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}

// sseWriter writes events to a client and flushes each one.
type SseWriter struct {
	w     http.ResponseWriter
	f     http.Flusher
	Begun bool
	wrote time.Time // when the client was last written to
}

func NewSSEWriter(w http.ResponseWriter) *SseWriter {
	f, _ := w.(http.Flusher)
	return &SseWriter{w: w, f: f}
}

func (s *SseWriter) Begin() {
	if s.Begun {
		return
	}
	s.Begun = true
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
	s.wrote = time.Now()
}

func (s *SseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// quiet is how long the client has had nothing from s; a stream not yet
// begun has been quiet for ever.
func (s *SseWriter) Quiet() time.Duration {
	if !s.Begun {
		return 1<<63 - 1
	}
	return time.Since(s.wrote)
}

// comment writes an SSE comment, which every event stream reader skips:
// no event, only the news that the stream is alive. No blank line after
// it: openai-go v2 (Crush) reads a blank line as an event even with no
// data, and fails on it ("unexpected end of JSON input").
func (s *SseWriter) comment(text string) {
	s.Begin()
	io.WriteString(s.w, ": "+text+"\n")
	s.flush()
	s.wrote = time.Now()
}

// event writes one event; a JSON-marshallable value or a raw string.
func (s *SseWriter) Event(name string, v any) {
	s.Begin()
	var b bytes.Buffer
	if name != "" {
		b.WriteString("event: ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	switch x := v.(type) {
	case string:
		b.WriteString(x)
	case []byte:
		b.Write(x)
	case json.RawMessage:
		b.Write(x)
	default:
		j, _ := json.Marshal(x)
		b.Write(j)
	}
	b.WriteString("\n\n")
	s.w.Write(b.Bytes())
	s.flush()
	s.wrote = time.Now()
}
