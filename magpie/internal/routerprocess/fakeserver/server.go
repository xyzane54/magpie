// Package fakeserver supplies deterministic llama-server wire and failure
// fixtures. Its outputs establish no real model quality or capability.
package fakeserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Ready     func() bool
	Status    string
	OnRequest func()
	OnCancel  func()
	Crash     func()
}

// Control is accepted only by this fake through the fake field in the normal
// chat completion body. Modes: error, hang, delay, crash, or ordinary success.
type Control struct {
	Mode        string   `json:"mode"`
	Status      int      `json:"status"`
	DelayMS     int      `json:"delay_ms"`
	Text        string   `json:"text"`
	Chunks      []string `json:"chunks"`
	ChunkWaitMS int      `json:"chunk_wait_ms"`
}

func New(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if config.Ready != nil && !config.Ready() {
			failure(w, http.StatusServiceUnavailable, "Loading model")
			return
		}
		status := config.Status
		if status == "" {
			status = "ok"
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
			Stream   bool              `json:"stream"`
			Fake     Control           `json:"fake"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil {
			failure(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if decoder.Decode(new(any)) != io.EOF || len(request.Messages) == 0 {
			failure(w, http.StatusBadRequest, "Expected one JSON body with messages")
			return
		}
		if config.OnRequest != nil {
			config.OnRequest()
		}
		control := request.Fake
		if mode := r.Header.Get("X-Fake-Mode"); mode != "" {
			control.Mode = mode
		}
		switch control.Mode {
		case "crash":
			if config.Crash != nil {
				config.Crash()
			}
			failure(w, http.StatusInternalServerError, "Fake crash")
			return
		case "error":
			code := control.Status
			if code < 400 || code > 599 {
				code = http.StatusInternalServerError
			}
			failure(w, code, "Controlled fake failure")
			return
		case "hang":
			// Flush a response so tests can cancel after observing acceptance,
			// without guessing whether the fake has received the request yet.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			http.NewResponseController(w).Flush()
			<-r.Context().Done()
			if config.OnCancel != nil {
				config.OnCancel()
			}
			return
		}
		if control.DelayMS > 0 && !wait(r, time.Duration(control.DelayMS)*time.Millisecond, config.OnCancel) {
			return
		}
		text := control.Text
		if text == "" {
			text = "fake completion"
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			chunks := control.Chunks
			if len(chunks) == 0 {
				chunks = strings.SplitAfter(text, " ")
			}
			for _, chunk := range chunks {
				if !wait(r, time.Duration(control.ChunkWaitMS)*time.Millisecond, config.OnCancel) {
					return
				}
				data, _ := json.Marshal(map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": request.Model,
					"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": chunk}, "finish_reason": nil}}})
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
			}
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl-fake\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "chatcmpl-fake", "object": "chat.completion", "model": request.Model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": text}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}})
	})
	return mux
}

func wait(r *http.Request, delay time.Duration, onCancel func()) bool {
	if delay <= 0 {
		return r.Context().Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		if onCancel != nil {
			onCancel()
		}
		return false
	}
}

func failure(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": message, "type": "unavailable_error"}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
