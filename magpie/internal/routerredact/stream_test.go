package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func streamTestEvent(v any) string {
	b, _ := wireMarshal(v)
	return "data: " + string(b) + "\n\n"
}

func streamTestTextDelta(p Protocol, text string) map[string]any {
	switch p {
	case Chat:
		return map[string]any{"id": "reply", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}}}
	case Responses:
		return map[string]any{"type": "response.output_text.delta", "item_id": "message", "output_index": 0, "content_index": 0, "sequence_number": 1, "delta": text}
	default:
		return map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}
	}
}

func streamTestToolDelta(p Protocol, index int, text string) map[string]any {
	switch p {
	case Chat:
		return map[string]any{"id": "reply", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": text}}}}, "finish_reason": nil}}}
	case Responses:
		return map[string]any{"type": "response.function_call_arguments.delta", "item_id": "tool_" + strconv.Itoa(index), "output_index": index, "sequence_number": 1, "delta": text}
	default:
		return map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": text}}
	}
}

func streamTestEnd(p Protocol, tools bool) string {
	switch p {
	case Chat:
		return streamTestEvent(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}) + streamTestEvent(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 2}}) + "data: [DONE]\n\n"
	case Responses:
		return streamTestEvent(map[string]any{"type": "response.completed", "sequence_number": 1, "response": map[string]any{"status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 0, "output_tokens": 2}}})
	default:
		end := streamTestEvent(map[string]any{"type": "content_block_stop", "index": 0})
		if tools {
			end += streamTestEvent(map[string]any{"type": "content_block_stop", "index": 1})
		}
		return end + streamTestEvent(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}}) + streamTestEvent(map[string]any{"type": "message_stop"})
	}
}

type streamByteReader struct{ io.Reader }

func (r streamByteReader) Read(b []byte) (int, error) {
	if len(b) > 1 {
		b = b[:1]
	}
	return r.Reader.Read(b)
}

func streamTestCollect(t *testing.T, p Protocol, output string) (string, map[string]string, []map[string]any) {
	t.Helper()
	var text strings.Builder
	tools := map[string]string{}
	var events []map[string]any
	for _, event := range strings.Split(output, "\n\n") {
		for _, line := range strings.Split(event, "\n") {
			if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
				continue
			}
			m, err := streamDecode([]byte(strings.TrimPrefix(line, "data: ")))
			if err != nil {
				t.Fatalf("invalid output event: %v", err)
			}
			events = append(events, m)
			switch p {
			case Chat:
				for _, raw := range streamList(m["choices"]) {
					choice := streamMap(raw)
					delta := streamMap(choice["delta"])
					text.WriteString(streamString(delta["content"]))
					for _, rawTool := range streamList(delta["tool_calls"]) {
						tool := streamMap(rawTool)
						index := streamIndex(tool["index"])
						tools[index] += streamString(streamMap(tool["function"])["arguments"])
					}
				}
			case Responses:
				switch m["type"] {
				case "response.output_text.delta":
					text.WriteString(streamString(m["delta"]))
				case "response.function_call_arguments.delta":
					tools[strings.TrimPrefix(streamString(m["item_id"]), "tool_")] += streamString(m["delta"])
				}
			case Anthropic:
				delta := streamMap(m["delta"])
				switch delta["type"] {
				case "text_delta":
					text.WriteString(streamString(delta["text"]))
				case "input_json_delta":
					tools[streamIndex(m["index"])] += streamString(delta["partial_json"])
				}
			}
		}
	}
	return text.String(), tools, events
}

func TestSSEPlaceholderAcrossChunksAndUTF8Bytes(t *testing.T) {
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		for cut := 1; cut < len("<PERSON:1>"); cut++ {
			t.Run(string(p)+"/"+strconv.Itoa(cut), func(t *testing.T) {
				s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "林雪🙂"}}
				defer s.Close()
				input := ": keepalive\n\n" + streamTestEvent(streamTestTextDelta(p, "你好 <PERSON:1>"[:len("你好 ")+cut])) + streamTestEvent(streamTestTextDelta(p, "<PERSON:1>"[cut:]+" <UNKNOWN:1>")) + streamTestEnd(p, false)
				var output bytes.Buffer
				if err := s.RestoreSSE(context.Background(), p, streamByteReader{strings.NewReader(input)}, &output); err != nil {
					t.Fatal(err)
				}
				text, _, events := streamTestCollect(t, p, output.String())
				if text != "你好 林雪🙂 <UNKNOWN:1>" {
					t.Fatalf("text %q", text)
				}
				if !strings.Contains(output.String(), ": keepalive") {
					t.Fatal("SSE comment dropped")
				}
				if p == Responses {
					for i, m := range events {
						if streamIndex(m["sequence_number"]) != strconv.Itoa(i) {
							t.Fatal("response sequence order")
						}
					}
				}
			})
		}
	}
}

func TestSSEInterleavedToolJSONValuesAndEscapes(t *testing.T) {
	original := "林\"雪\\\n🙂"
	args := []string{`{"<PERSON:1>":"<PERSON:1>","nested":[{"v":"\u003cPERSON:1>"},42,true,null],"n":9007199254740993}`, `{"text":"<PERSON:1> / <UNKNOWN:1>","other":false}`}
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		t.Run(string(p), func(t *testing.T) {
			s := &Snapshot{mapping: map[string]string{"<PERSON:1>": original}}
			defer s.Close()
			var input strings.Builder
			// One-character JSON fragments deliberately split \u escapes and tokens.
			for i := 0; i < len(args[0]) || i < len(args[1]); i++ {
				for tool, arg := range args {
					if i < len(arg) {
						input.WriteString(streamTestEvent(streamTestToolDelta(p, tool, arg[i:i+1])))
					}
				}
			}
			input.WriteString(streamTestEnd(p, true))
			var output bytes.Buffer
			if err := s.RestoreSSE(context.Background(), p, streamByteReader{strings.NewReader(input.String())}, &output); err != nil {
				t.Fatal(err)
			}
			_, tools, _ := streamTestCollect(t, p, output.String())
			if len(tools) != 2 {
				t.Fatal("tool channels mixed")
			}
			var first, second map[string]any
			if json.Unmarshal([]byte(tools["0"]), &first) != nil || json.Unmarshal([]byte(tools["1"]), &second) != nil {
				t.Fatal("restored tools are invalid JSON")
			}
			if first["<PERSON:1>"] != original || second["text"] != original+" / <UNKNOWN:1>" {
				t.Fatalf("wrong restored parameters: %#v %#v", first, second)
			}
			nested := first["nested"].([]any)
			if nested[0].(map[string]any)["v"] != original || nested[1] != float64(42) || nested[2] != true || nested[3] != nil || second["other"] != false {
				t.Fatal("JSON structure/types changed")
			}
			if !strings.Contains(tools["0"], "9007199254740993") {
				t.Fatal("integer precision lost")
			}
		})
	}
}

func TestSSEUnknownIncompleteSuffixFlushBeforeTermination(t *testing.T) {
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "synthetic"}}
		var output bytes.Buffer
		input := streamTestEvent(streamTestTextDelta(p, "value <PERSON:")) + streamTestEnd(p, false)
		if err := s.RestoreSSE(context.Background(), p, strings.NewReader(input), &output); err != nil {
			t.Fatal(err)
		}
		text, _, events := streamTestCollect(t, p, output.String())
		if text != "value <PERSON:" {
			t.Fatalf("%s lost suffix %q", p, text)
		}
		if p == Responses {
			for i, m := range events {
				if streamIndex(m["sequence_number"]) != strconv.Itoa(i) {
					t.Fatal("synthetic response delta sequence")
				}
			}
		}
		s.Close()
	}
}

func TestSSEInvalidArgumentsWithholdSuccess(t *testing.T) {
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		for _, args := range []string{`{"value":"<PERSON:1>`, `{"a":1,"a":2}`, `{"a":"\uD800"}`, `{"a":01}`, `{"a":true,}`, `[1,2]`, `"text"`, `{"a":"\q"}`} {
			s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "private synthetic"}}
			input := streamTestEvent(streamTestToolDelta(p, 0, args)) + streamTestEnd(p, false)
			var output bytes.Buffer
			if err := s.RestoreSSE(context.Background(), p, strings.NewReader(input), &output); err == nil {
				t.Fatalf("%s accepted malformed arguments %q", p, args)
			} else if strings.Contains(err.Error(), "private synthetic") || strings.Contains(err.Error(), "PERSON") {
				t.Fatal("error contains private material")
			}
			if strings.Contains(output.String(), "[DONE]") || strings.Contains(output.String(), "response.completed") || strings.Contains(output.String(), "message_stop") {
				t.Fatal("failed stream emitted success terminator")
			}
			s.Close()
		}
	}
}

func TestSSETruncationAndErrorTail(t *testing.T) {
	for _, p := range []Protocol{Chat, Responses, Anthropic} {
		s := &Snapshot{mapping: map[string]string{}}
		for _, input := range []string{streamTestEvent(streamTestTextDelta(p, "partial")), strings.TrimSuffix(streamTestEvent(streamTestTextDelta(p, "partial")), "\n\n"), streamTestEvent(streamTestTextDelta(p, "partial")) + streamTestEnd(p, false) + streamTestEvent(map[string]any{"error": map[string]any{"message": "sensitive upstream body"}})} {
			var output bytes.Buffer
			if err := s.RestoreSSE(context.Background(), p, strings.NewReader(input), &output); err == nil {
				t.Fatal("truncated/contradictory stream succeeded")
			}
			if strings.Contains(output.String(), "[DONE]") || strings.Contains(output.String(), "response.completed") || strings.Contains(output.String(), "message_stop") || strings.Contains(output.String(), "sensitive upstream body") {
				t.Fatal("unsafe terminal or error body delivered")
			}
		}
		s.Close()
	}
}

type streamCancelReader struct {
	once    sync.Once
	entered chan struct{}
	closed  chan struct{}
}

func (r *streamCancelReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.closed
	return 0, io.ErrClosedPipe
}
func (r *streamCancelReader) Close() error { close(r.closed); return nil }

func TestSSECancelClosesInput(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{}}
	defer s.Close()
	reader := &streamCancelReader{entered: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.RestoreSSE(ctx, Chat, reader, io.Discard) }()
	select {
	case <-reader.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reader never entered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "stream_canceled" {
			t.Fatalf("cancel result %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation failed to unblock reader")
	}
}

func TestSSEBoundedLimitsAndStrictEvents(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{}}
	defer s.Close()
	for _, input := range []string{"data: " + strings.Repeat("x", streamEventLimit+1) + "\n\n", "data: {\"choices\":[],\"choices\":[]}\n\ndata: [DONE]\n\n", "data: {\"choices\":[],\"id\":\"\\ud800\"}\n\ndata: [DONE]\n\n"} {
		if err := s.RestoreSSE(context.Background(), Chat, strings.NewReader(input), io.Discard); err == nil {
			t.Fatal("unbounded or malformed event accepted")
		}
	}
	var input strings.Builder
	for i := 0; i <= streamChannelLimit; i++ {
		input.WriteString(streamTestEvent(streamTestToolDelta(Chat, i, `{`)))
	}
	if err := s.RestoreSSE(context.Background(), Chat, strings.NewReader(input.String()), io.Discard); err == nil || err.Error() != "stream_limit" {
		t.Fatalf("channel capacity %v", err)
	}
}

func TestSSEJSONLexerLargeValueUsesBoundedWindow(t *testing.T) {
	j := &streamJSON{text: streamText{mapping: map[string]string{"<PERSON:1>": "restored"}}}
	if _, err := j.feed(`{"value":"`, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		out, err := j.feed(strings.Repeat("x", 1024), false)
		if err != nil || len(out) != 1024 || j.text.pending != "" || j.escape != "" {
			t.Fatal("ordinary value retained in memory")
		}
	}
	if _, err := j.feed(`<PERSON:1>"}`, true); err != nil {
		t.Fatal(err)
	}
}

func TestSSEFullResponseSnapshotsRestoredWithoutTouchingMetadata(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "合成实体"}}
	defer s.Close()
	item := map[string]any{"type": "function_call", "id": "<PERSON:1>", "call_id": "call", "name": "<PERSON:1>", "arguments": `{"v":"<PERSON:1>","n":7}`}
	terminal := map[string]any{"type": "response.completed", "sequence_number": 20, "response": map[string]any{"status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 0, "output_tokens": 1}}}
	var output bytes.Buffer
	if err := s.RestoreSSE(context.Background(), Responses, strings.NewReader(streamTestEvent(terminal)), &output); err != nil {
		t.Fatal(err)
	}
	_, _, events := streamTestCollect(t, Responses, output.String())
	response := streamMap(events[0]["response"])
	restored := streamMap(streamList(response["output"])[0])
	if restored["id"] != "<PERSON:1>" || restored["name"] != "<PERSON:1>" || restored["call_id"] != "call" {
		t.Fatal("tool metadata changed")
	}
	var args map[string]any
	if json.Unmarshal([]byte(streamString(restored["arguments"])), &args) != nil || args["v"] != "合成实体" || args["n"] != float64(7) {
		t.Fatal("snapshot arguments not restored")
	}
	if !reflect.DeepEqual(response["usage"], map[string]any{"input_tokens": json.Number("0"), "output_tokens": json.Number("1")}) {
		t.Fatal("usage changed")
	}
}

func TestSSEResponsesDoneClosesExactContentChannel(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "合成"}}
	defer s.Close()
	one := streamTestTextDelta(Responses, "one")
	one["content_index"] = 1
	ten := streamTestTextDelta(Responses, "<PERSON:")
	ten["content_index"] = 10
	done := map[string]any{"type": "response.output_text.done", "item_id": "message", "output_index": 0, "content_index": 1, "sequence_number": 1, "text": "one"}
	last := streamTestTextDelta(Responses, "1>")
	last["content_index"] = 10
	input := streamTestEvent(one) + streamTestEvent(ten) + streamTestEvent(done) + streamTestEvent(last) + streamTestEnd(Responses, false)
	var output bytes.Buffer
	if err := s.RestoreSSE(context.Background(), Responses, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	text, _, _ := streamTestCollect(t, Responses, output.String())
	if text != "one合成" {
		t.Fatalf("mixed content channels %q", text)
	}
}

func TestSSESyntheticFlushKeepsNamedEvents(t *testing.T) {
	for _, p := range []Protocol{Responses, Anthropic} {
		s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "private"}}
		input := streamTestEvent(streamTestTextDelta(p, "<PERSON:")) + streamTestEnd(p, false)
		var output bytes.Buffer
		if err := s.RestoreSSE(context.Background(), p, strings.NewReader(input), &output); err != nil {
			t.Fatal(err)
		}
		for _, event := range strings.Split(output.String(), "\n\n") {
			if !strings.Contains(event, "data: ") {
				continue
			}
			lines := strings.Split(event, "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "data: ") {
					m, err := streamDecode([]byte(strings.TrimPrefix(line, "data: ")))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(event, "event: "+streamString(m["type"])+"\n") {
						t.Fatal("flush event lacks named SSE channel")
					}
				}
			}
		}
		s.Close()
	}
}

func TestSSEFullTextExpansionFailsClosed(t *testing.T) {
	text := strings.Repeat("<PERSON:1>", 2050)
	tests := []struct {
		p     Protocol
		event map[string]any
	}{
		{Responses, map[string]any{"type": "response.content_part.done", "part": map[string]any{"type": "output_text", "text": text}}},
		{Responses, map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}}},
		{Responses, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "custom_tool_call", "input": text}}},
		{Anthropic, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": text}}},
	}
	for _, tt := range tests {
		s := &Snapshot{mapping: map[string]string{"<PERSON:1>": strings.Repeat("x", 4096)}}
		var output bytes.Buffer
		if err := s.RestoreSSE(context.Background(), tt.p, strings.NewReader(streamTestEvent(tt.event)+streamTestEnd(tt.p, false)), &output); err == nil || err.Error() != "stream_limit" {
			t.Fatalf("expansion did not fail: %s %v", tt.p, err)
		}
		if output.Len() != 0 {
			t.Fatal("failed full event emitted partial text")
		}
		s.Close()
	}
}

func TestSSEConflictingToolAssociationsFail(t *testing.T) {
	chat := func(index int, id string) map[string]any {
		m := streamTestToolDelta(Chat, index, `{`)
		call := streamMap(streamList(streamMap(streamMap(streamList(m["choices"])[0])["delta"])["tool_calls"])[0])
		call["id"] = id
		return m
	}
	r1 := streamTestToolDelta(Responses, 0, `{`)
	r2 := streamTestToolDelta(Responses, 0, `}`)
	r2["output_index"] = 1
	a := func(id string) map[string]any {
		return map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "tool", "input": map[string]any{}}}
	}
	added := map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "tool_0", "call_id": "call", "name": "first", "arguments": ""}}
	done := map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "tool_0", "call_id": "call", "name": "changed", "arguments": "{}"}}
	for _, tt := range []struct {
		p    Protocol
		a, b map[string]any
	}{{Chat, chat(0, "first"), chat(0, "changed")}, {Chat, chat(0, "same"), chat(1, "same")}, {Responses, r1, r2}, {Responses, added, done}, {Anthropic, a("first"), a("changed")}} {
		s := &Snapshot{mapping: map[string]string{}}
		var output bytes.Buffer
		err := s.RestoreSSE(context.Background(), tt.p, strings.NewReader(streamTestEvent(tt.a)+streamTestEvent(tt.b)+streamTestEnd(tt.p, false)), &output)
		if err == nil || err.Error() != "stream_invalid_channel" {
			t.Fatalf("association conflict accepted %s: %v", tt.p, err)
		}
		s.Close()
	}
}

type streamShortWriter struct{}

func (streamShortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestSSEShortWriterFails(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{}}
	defer s.Close()
	if err := s.RestoreSSE(context.Background(), Chat, strings.NewReader(streamTestEnd(Chat, false)), streamShortWriter{}); err == nil || err.Error() != "stream_write_failed" {
		t.Fatalf("short write result: %v", err)
	}
}

func TestSSEJSONSurrogatePairSplit(t *testing.T) {
	j := &streamJSON{text: streamText{mapping: map[string]string{}}}
	var output strings.Builder
	for _, part := range []string{`{"v":"\uD8`, `3D\u`, `DE00"}`} {
		v, err := j.feed(part, false)
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(v)
	}
	v, err := j.feed("", true)
	if err != nil {
		t.Fatal(err)
	}
	output.WriteString(v)
	var got map[string]any
	if json.Unmarshal([]byte(output.String()), &got) != nil || got["v"] != "😀" {
		t.Fatal("surrogate pair changed")
	}
}

func TestSSEAnthropicPlaceholderSpansStartAndDelta(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "合成"}}
	defer s.Close()
	start := map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": "prefix <PERSON:"}}
	input := streamTestEvent(start) + streamTestEvent(streamTestTextDelta(Anthropic, "1> suffix")) + streamTestEnd(Anthropic, false)
	var output bytes.Buffer
	if err := s.RestoreSSE(context.Background(), Anthropic, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	text, _, events := streamTestCollect(t, Anthropic, output.String())
	text = streamString(streamMap(events[0]["content_block"])["text"]) + text
	if text != "prefix 合成 suffix" {
		t.Fatalf("start/delta split: %q", text)
	}
}

func TestSSEResponsesPlainReasoningSnapshotsRestored(t *testing.T) {
	s := &Snapshot{mapping: map[string]string{"<PERSON:1>": "合成"}}
	defer s.Close()
	part := map[string]any{"type": "response.reasoning_summary_part.done", "part": map[string]any{"type": "summary_text", "text": "<PERSON:1>"}}
	item := map[string]any{"type": "reasoning", "id": "reason", "encrypted_content": nil, "summary": []any{map[string]any{"type": "summary_text", "text": "<PERSON:1>"}}, "content": []any{map[string]any{"type": "reasoning_text", "text": "<PERSON:1>"}}}
	terminal := map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{item}, "output_text": "<PERSON:1>"}}
	var output bytes.Buffer
	if err := s.RestoreSSE(context.Background(), Responses, strings.NewReader(streamTestEvent(part)+streamTestEvent(terminal)), &output); err != nil {
		t.Fatal(err)
	}
	_, _, events := streamTestCollect(t, Responses, output.String())
	if streamMap(events[0]["part"])["text"] != "合成" {
		t.Fatal("reasoning part not restored")
	}
	response := streamMap(events[1]["response"])
	restored := streamMap(streamList(response["output"])[0])
	if response["output_text"] != "合成" || streamMap(streamList(restored["summary"])[0])["text"] != "合成" || streamMap(streamList(restored["content"])[0])["text"] != "合成" {
		t.Fatal("reasoning snapshot not restored")
	}
}
