package routercloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportedUsagePreservesActualFields(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, tc := range []struct {
			name, usage          string
			input, output, total *int64
		}{
			{"absent", "", nil, nil, nil},
			{"empty", `{}`, nil, nil, nil},
			{"null", `null`, nil, nil, nil},
			{"zero", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, count(0), count(0), count(0)},
			{"partial", `{"input_tokens":7}`, count(7), nil, nil},
			{"total-only", `{"total_tokens":9}`, nil, nil, count(9)},
			{"nonzero", `{"input_tokens":7,"output_tokens":2,"total_tokens":9}`, count(7), count(2), count(9)},
			{"missing-null", `{"input_tokens":7,"output_tokens":null}`, count(7), nil, nil},
			{"negative", `{"input_tokens":-1,"output_tokens":2}`, nil, count(2), nil},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				body := whole(protocol, false)
				if tc.usage != "" {
					usage := tc.usage
					if protocol == "chat" {
						usage = strings.ReplaceAll(strings.ReplaceAll(usage, "input_tokens", "prompt_tokens"), "output_tokens", "completion_tokens")
					}
					body = strings.TrimSuffix(body, "}") + `,"usage":` + usage + "}"
				}
				e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })}
				out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{protocol, input(protocol, false), configured("http://upstream.test/v1", protocol)})
				if err != nil {
					t.Fatal(err)
				}
				u := out.ReportedUsage
				if tc.input == nil && tc.output == nil && tc.total == nil {
					if u != nil {
						t.Fatalf("absent counts became known: %+v", u)
					}
					return
				}
				if u == nil {
					t.Fatal("actual usage was discarded")
				}
				for _, pair := range []struct {
					name      string
					got, want *int64
				}{{"input", u.InputTokens, tc.input}, {"output", u.OutputTokens, tc.output}, {"total", u.TotalTokens, tc.total}} {
					if pair.got == nil && pair.want == nil {
						continue
					}
					if pair.got == nil || pair.want == nil || *pair.got != *pair.want {
						t.Fatalf("%s presence/value changed: got %v want %v", pair.name, pair.got, pair.want)
					}
				}
			})
		}
	}
}
func count(n int64) *int64 { return &n }

func TestReportedUsageIsNotIRNormalizedOrInferred(t *testing.T) {
	body := strings.TrimSuffix(whole("chat", false), "}") + `,"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`
	e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })}
	out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", false), configured("http://upstream.test/v1", "chat")})
	if err != nil || out.ReportedUsage == nil || *out.ReportedUsage.InputTokens != 10 || out.ReportedUsage.TotalTokens != nil {
		t.Fatalf("normalized or inferred actual usage: %+v %v", out, err)
	}
}

func TestReportedStreamingUsageExplicitZeroOverridesEarlierCount(t *testing.T) {
	wire := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":8}}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0}}\n\n" +
		"data: [DONE]\n\n"
	e := Executor{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		r := response(200, wire)
		r.Header.Set("Content-Type", "text/event-stream")
		return r, nil
	})}
	out, err := e.Execute(context.Background(), httptest.NewRecorder(), Request{"chat", input("chat", true), configured("http://upstream.test/v1", "chat")})
	if err != nil || out.ReportedUsage == nil || *out.ReportedUsage.InputTokens != 0 || *out.ReportedUsage.OutputTokens != 0 || out.ReportedUsage.TotalTokens != nil {
		t.Fatalf("explicit zero was replaced by IR accumulation: %+v %v", out, err)
	}
}
