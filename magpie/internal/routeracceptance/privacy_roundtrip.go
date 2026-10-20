package routeracceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/routerprocess"
	"github.com/yetone/magpie/internal/routerredact"
	"github.com/yetone/magpie/internal/routerruntime"
)

type privacyRoundtripEvidence struct {
	Case                         string `json:"case"`
	Fields                       int    `json:"fields"`
	Batches                      int    `json:"batches"`
	Entities                     int    `json:"entities"`
	UpstreamRequests             int    `json:"upstreamRequests"`
	CaptureBlocked               bool   `json:"captureBlocked"`
	OutboundSensitiveWordsAbsent bool   `json:"outboundSensitiveWordsAbsent"`
	JSONRestored                 bool   `json:"jsonRestored"`
	SSERestored                  bool   `json:"sseRestored"`
	ToolArgumentsRestored        bool   `json:"toolArgumentsRestored"`
	ToolCheckApplicable          bool   `json:"toolCheckApplicable"`
	ExactNumbersPreserved        bool   `json:"exactNumbersPreserved"`
	NumberCheckApplicable        bool   `json:"numberCheckApplicable"`
	RedactionCapabilityUnknown   bool   `json:"redactionCapabilityUnknown"`
	ExtractionMS                 int64  `json:"extractionMs"`
	DurationMS                   int64  `json:"durationMs"`
	ErrorCode                    string `json:"errorCode,omitempty"`
}

type privacyRoundtripGenerate func(context.Context, routerruntime.Purpose, []byte, func(*http.Response) error) error

// runPrivacyRoundtrip is an offline synthetic composition check, never a
// production capability or routing adapter. Only the service-owned Manager may
// provide the model result; no capability is accepted or changed here.
func runPrivacyRoundtrip(ctx context.Context, m *routerruntime.Manager, spec *routerredact.PrivacySpecification, outputDir string) (privacyRoundtripEvidence, error) {
	if m == nil {
		return privacyRoundtripEvidence{Case: "person-email-tools-numbers", ToolCheckApplicable: true, NumberCheckApplicable: true, ErrorCode: "runtime_unavailable"}, errors.New("runtime_unavailable")
	}
	return runPrivacyRoundtripWith(ctx, m.Generate, m.Status, spec, outputDir)
}

func runPrivacyRoundtripWith(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, outputDir string) (report privacyRoundtripEvidence, result error) {
	// Keep the original complete history and numeric fixture unchanged.
	const sourceText = "姓名：李芳；邮箱：roundtrip@example.test"
	const sourceArguments = `{"person":"李芳","email":"roundtrip@example.test","amount":932000.00,"large":9007199254740993,"ok":true,"nil":null}`
	return runPrivacyRoundtripCaseWith(parent, generate, status, spec, outputDir, privacyRoundtripCase{
		name: "person-email-tools-numbers", rawStem: "roundtrip", identity: "synthetic-roundtrip",
		text: sourceText, arguments: sourceArguments, sensitive: []string{"李芳", "roundtrip@example.test"}, checkNumbers: true,
	})
}

// runPrivacySmallRoundtrip is separate scoped evidence. It never replaces the
// original larger history, its result or the full recognition evaluation.
func runPrivacySmallRoundtrip(ctx context.Context, m *routerruntime.Manager, spec *routerredact.PrivacySpecification, outputDir string) (privacyRoundtripEvidence, error) {
	if m == nil {
		return privacyRoundtripEvidence{Case: "email-tools-small", ToolCheckApplicable: true, ErrorCode: "runtime_unavailable"}, errors.New("runtime_unavailable")
	}
	return runPrivacySmallRoundtripWith(ctx, m.Generate, m.Status, spec, outputDir)
}

func runPrivacySmallRoundtripWith(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, outputDir string) (privacyRoundtripEvidence, error) {
	return runPrivacyRoundtripCaseWith(parent, generate, status, spec, outputDir, privacyRoundtripCase{
		name: "email-tools-small", rawStem: "small-roundtrip", identity: "synthetic-small-roundtrip",
		text: "邮箱：roundtrip@example.test", arguments: `{"email":"roundtrip@example.test"}`, sensitive: []string{"roundtrip@example.test"},
	})
}

// runPrivacyTextRoundtrip reuses the exact fixed56 case 4 annotation to check
// only a single text round trip. It makes no tool or numeric acceptance claim.
func runPrivacyTextRoundtrip(ctx context.Context, m *routerruntime.Manager, spec *routerredact.PrivacySpecification, outputDir string) (privacyRoundtripEvidence, error) {
	if m == nil {
		return privacyRoundtripEvidence{Case: "email-text-only", ErrorCode: "runtime_unavailable"}, errors.New("runtime_unavailable")
	}
	return runPrivacyTextRoundtripWith(ctx, m.Generate, m.Status, spec, outputDir)
}

func runPrivacyTextRoundtripWith(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, outputDir string) (privacyRoundtripEvidence, error) {
	const text = "邮箱：synthetic@example.test"
	return runPrivacyRoundtripCaseWith(parent, generate, status, spec, outputDir, privacyRoundtripCase{
		name: "email-text-only", rawStem: "text-roundtrip", identity: "synthetic-text-roundtrip",
		text: text, sensitive: []string{"synthetic@example.test"},
		expected: []routerredact.Entity{{Field: 0, Start: len("邮箱："), End: len(text), Text: "synthetic@example.test", Category: "EMAIL"}},
	})
}

type privacyRoundtripCase struct {
	name, rawStem, identity, text, arguments string
	sensitive                                []string
	checkNumbers                             bool
	expected                                 []routerredact.Entity
}

func runPrivacyRoundtripCaseWith(parent context.Context, generate privacyRoundtripGenerate, status func() routerruntime.Status, spec *routerredact.PrivacySpecification, outputDir string, scenario privacyRoundtripCase) (report privacyRoundtripEvidence, result error) {
	report.Case, report.NumberCheckApplicable = scenario.name, scenario.checkNumbers
	report.ToolCheckApplicable = scenario.arguments != ""
	started := time.Now()
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		if status != nil {
			capability := status().Capabilities[routerruntime.Redaction]
			report.RedactionCapabilityUnknown = capability.State == "unknown" && !capability.Ready
		}
		if result != nil && report.ErrorCode == "" {
			report.ErrorCode = safeDiagnosticError(result)
		}
	}()
	fail := func(code string) error { report.ErrorCode = code; return errors.New(code) }
	if spec == nil || generate == nil || status == nil {
		return report, fail("roundtrip_unavailable")
	}
	capabilityMatches := func() bool {
		capability := status().Capabilities[routerruntime.Redaction]
		return capability.State == "unknown" && !capability.Ready && capability.Fingerprint == spec.RuntimeFingerprint()
	}
	if !capabilityMatches() {
		return report, fail("roundtrip_capability_state")
	}
	// One deadline covers capture, all model batches and replay preparation.
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	messages := []any{map[string]any{"role": "user", "content": scenario.text}}
	if report.ToolCheckApplicable {
		messages = append(messages,
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-roundtrip", "type": "function", "function": map[string]any{"name": "notify", "arguments": scenario.arguments}}}},
			map[string]any{"role": "tool", "tool_call_id": "call-roundtrip", "content": "ok"},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "synthetic-cloud", "messages": messages})
	in := routerredact.Input{RequestID: scenario.identity, AppID: scenario.identity, Protocol: routerredact.Chat, Operation: "generation", Versions: spec.Versions(), Body: body}
	var captured routerredact.Extraction
	capture := routerredact.New(routerredact.Options{Extractor: routerredact.ExtractorFunc(func(_ context.Context, input routerredact.Extraction) (routerredact.Result, error) {
		captured = routerredact.Extraction{Versions: input.Versions, Texts: append([]string(nil), input.Texts...)}
		return routerredact.Result{}, errors.New("synthetic_capture_only")
	})})
	prepared, captureErr := capture.Prepare(ctx, in)
	capture.Close()
	if prepared != nil {
		prepared.Close()
	}
	report.CaptureBlocked = captureErr != nil && prepared == nil && len(captured.Texts) > 0
	if !report.CaptureBlocked {
		return report, fail("roundtrip_capture_failed")
	}
	report.Fields = len(captured.Texts)
	if !report.ToolCheckApplicable && (len(captured.Texts) != 1 || captured.Texts[0] != scenario.text) {
		return report, fail("roundtrip_capture_failed")
	}
	batches, err := spec.Batches(ctx, captured)
	if err != nil {
		return report, err
	}
	covered := make([]bool, len(captured.Texts))
	combined := routerredact.Result{Complete: true}
	extractionStarted := time.Now()
	for index, fields := range batches {
		if !capabilityMatches() {
			return report, fail("roundtrip_capability_state")
		}
		batch := routerredact.Extraction{Versions: captured.Versions, Texts: make([]string, len(fields))}
		for local, field := range fields {
			if field < 0 || field >= len(covered) || covered[field] {
				return report, fail("roundtrip_batch_coverage")
			}
			covered[field], batch.Texts[local] = true, captured.Texts[field]
		}
		request, err := spec.Build(ctx, batch)
		if err != nil {
			return report, err
		}
		var parsed routerredact.Result
		consumed := 0
		err = generate(ctx, routerruntime.Redaction, request, func(response *http.Response) error {
			consumed++
			if consumed != 1 || response == nil || response.StatusCode != http.StatusOK || response.Body == nil {
				return fail("roundtrip_response_invalid")
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			if err != nil || len(raw) > 1<<20 {
				return fail("roundtrip_response_invalid")
			}
			if outputDir != "" {
				directory := filepath.Join(outputDir, "synthetic-only")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					return fail("roundtrip_evidence_write_failed")
				}
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%s-batch-%02d.response.json", scenario.rawStem, index)), raw, 0o600); err != nil {
					return fail("roundtrip_evidence_write_failed")
				}
			}
			parsed, err = spec.Parse(ctx, batch, raw)
			if err != nil {
				report.ErrorCode = "roundtrip_parse_" + routerredact.PrivacyParseDiagnosticCode(err)
			}
			return err
		})
		report.Batches++
		report.ExtractionMS = time.Since(extractionStarted).Milliseconds()
		if err != nil {
			return report, err
		}
		if !capabilityMatches() {
			return report, fail("roundtrip_capability_state")
		}
		if consumed != 1 || !parsed.Complete {
			return report, fail("roundtrip_incomplete")
		}
		for _, entity := range parsed.Entities {
			if entity.Field < 0 || entity.Field >= len(fields) {
				return report, fail("roundtrip_batch_coverage")
			}
			entity.Field = fields[entity.Field]
			combined.Entities = append(combined.Entities, entity)
		}
	}
	for _, seen := range covered {
		if !seen {
			return report, fail("roundtrip_batch_coverage")
		}
	}
	report.Entities = len(combined.Entities)
	if scenario.expected != nil && !reflect.DeepEqual(combined.Entities, scenario.expected) {
		// Expected spans only validate the actual model findings; they never
		// supply, repair or filter a finding used by the W5 preparation.
		return report, fail("roundtrip_expected_entity_mismatch")
	}
	// Immutable replay is tied to every exact captured text and version. It has
	// no recognition logic, and cannot answer any other request or be installed
	// into the service's production pipeline.
	pipeline := routerredact.New(routerredact.Options{Extractor: routerredact.ExtractorFunc(func(call context.Context, input routerredact.Extraction) (routerredact.Result, error) {
		if call.Err() != nil {
			return routerredact.Result{}, call.Err()
		}
		if input.Versions != captured.Versions || !reflect.DeepEqual(input.Texts, captured.Texts) {
			return routerredact.Result{}, errors.New("synthetic_replay_mismatch")
		}
		return routerredact.Result{Complete: true, Entities: append([]routerredact.Entity(nil), combined.Entities...)}, nil
	})})
	defer pipeline.Close()
	prepared, err = pipeline.Prepare(ctx, in)
	if err != nil {
		return report, err
	}
	defer prepared.Close()
	masked, err := prepared.Body()
	if err != nil {
		return report, err
	}
	if !capabilityMatches() {
		return report, fail("roundtrip_capability_state")
	}
	containsSensitive := func(body []byte) bool {
		for _, value := range scenario.sensitive {
			if value == "" || bytes.Contains(body, []byte(value)) {
				return true
			}
		}
		return false
	}
	if containsSensitive(masked) {
		return report, fail("roundtrip_sensitive_word_remaining")
	}
	report.OutboundSensitiveWordsAbsent = true
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamRequests.Add(1)
		outbound, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
		if err != nil || !bytes.Equal(outbound, masked) || containsSensitive(outbound) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		text, arguments, err := privacyRoundtripRequestValues(outbound)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.URL.Path == "/json" {
			w.Header().Set("Content-Type", "application/json")
			message := map[string]any{"role": "assistant", "content": text}
			finish := "stop"
			if report.ToolCheckApplicable {
				message["tool_calls"] = []any{map[string]any{"id": "call-roundtrip", "type": "function", "function": map[string]any{"name": "notify", "arguments": arguments}}}
				finish = "tool_calls"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent := func(delta map[string]any, finish any) {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		for _, part := range privacyRoundtripParts(text) {
			writeEvent(map[string]any{"content": part}, nil)
		}
		for index, part := range privacyRoundtripParts(arguments) {
			call := map[string]any{"index": 0, "function": map[string]any{"arguments": part}}
			if index == 0 {
				call["id"], call["type"] = "call-roundtrip", "function"
				call["function"].(map[string]any)["name"] = "notify"
			}
			writeEvent(map[string]any{"tool_calls": []any{call}}, nil)
		}
		finish := "stop"
		if report.ToolCheckApplicable {
			finish = "tool_calls"
		}
		writeEvent(map[string]any{}, finish)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	defer func() { report.UpstreamRequests = int(upstreamRequests.Load()) }()
	client := routerprocess.NewHTTPClient()
	defer client.CloseIdleConnections()
	for _, path := range []string{"/json", "/sse"} {
		request, err := http.NewRequestWithContext(parent, http.MethodPost, upstream.URL+path, bytes.NewReader(masked))
		if err != nil {
			return report, fail("roundtrip_upstream_failed")
		}
		response, err := client.Do(request)
		if err != nil {
			return report, fail("roundtrip_upstream_failed")
		}
		var restored []byte
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return report, fail("roundtrip_upstream_failed")
		}
		if path == "/json" {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			response.Body.Close()
			if readErr != nil || len(raw) > 1<<20 {
				return report, fail("roundtrip_upstream_failed")
			}
			restored, err = prepared.RestoreResponse(routerredact.Chat, raw)
		} else {
			var out bytes.Buffer
			err = prepared.RestoreSSE(parent, routerredact.Chat, response.Body, &out)
			response.Body.Close()
			restored = out.Bytes()
		}
		if err != nil {
			return report, fail("roundtrip_restore_failed")
		}
		text, arguments, err := privacyRoundtripRestoredValues(restored, path == "/sse")
		argumentsMatch := arguments == ""
		if report.ToolCheckApplicable {
			argumentsMatch = privacyRoundtripEqualJSON(arguments, scenario.arguments)
		}
		if err != nil || text != scenario.text || !argumentsMatch {
			return report, fail("roundtrip_restore_mismatch")
		}
		if scenario.checkNumbers && (!strings.Contains(arguments, "932000.00") || !strings.Contains(arguments, "9007199254740993")) {
			return report, fail("roundtrip_number_mismatch")
		}
		if path == "/json" {
			report.JSONRestored = true
		} else {
			report.SSERestored = true
		}
	}
	report.ToolArgumentsRestored, report.ExactNumbersPreserved = report.ToolCheckApplicable, scenario.checkNumbers
	return report, nil
}

func privacyRoundtripParts(value string) []string {
	// Small chunks deliberately split placeholders and JSON tokens.
	runes := []rune(value)
	var parts []string
	for len(runes) > 0 {
		n := min(5, len(runes))
		parts, runes = append(parts, string(runes[:n])), runes[n:]
	}
	return parts
}

func privacyRoundtripRequestValues(raw []byte) (string, string, error) {
	var request struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return "", "", errors.New("roundtrip_wire_invalid")
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == "user" && len(request.Messages[0].ToolCalls) == 0 {
		return request.Messages[0].Content, "", nil
	}
	if len(request.Messages) != 3 || len(request.Messages[1].ToolCalls) != 1 {
		return "", "", errors.New("roundtrip_wire_invalid")
	}
	return request.Messages[0].Content, request.Messages[1].ToolCalls[0].Function.Arguments, nil
}

func privacyRoundtripRestoredValues(raw []byte, stream bool) (string, string, error) {
	var text, arguments strings.Builder
	parse := func(data []byte) error {
		var response struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &response) != nil || len(response.Choices) != 1 {
			return errors.New("roundtrip_wire_invalid")
		}
		choice := response.Choices[0]
		if stream {
			text.WriteString(choice.Delta.Content)
			for _, call := range choice.Delta.ToolCalls {
				arguments.WriteString(call.Function.Arguments)
			}
		} else {
			text.WriteString(choice.Message.Content)
			for _, call := range choice.Message.ToolCalls {
				arguments.WriteString(call.Function.Arguments)
			}
		}
		return nil
	}
	if !stream {
		if err := parse(raw); err != nil {
			return "", "", err
		}
	} else {
		terminal := false
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				terminal = true
				continue
			}
			if err := parse([]byte(data)); err != nil {
				return "", "", err
			}
		}
		if !terminal {
			return "", "", errors.New("roundtrip_stream_incomplete")
		}
	}
	return text.String(), arguments.String(), nil
}

func privacyRoundtripEqualJSON(left, right string) bool {
	decode := func(value string) (any, error) {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var result any
		err := decoder.Decode(&result)
		if err != nil {
			return nil, err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("roundtrip_json_invalid")
		}
		return result, nil
	}
	l, le := decode(left)
	r, re := decode(right)
	return le == nil && re == nil && reflect.DeepEqual(l, r)
}
