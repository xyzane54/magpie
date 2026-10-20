package routeracceptance

import (
	"bytes"
	"testing"
)

func TestPrivacyDiagnosticCLIIsFixedAndExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"-privacy-diagnostic", "arbitrary"},
		{"-privacy-diagnostic", "full", "-model", "spark-x2.5-4B"},
		{"-privacy-diagnostic", "full", "-business-preacceptance"},
		{"-privacy-diagnostic", "representative", "-prepare-only"},
	} {
		if _, err := ParseOptions(args, &bytes.Buffer{}); err == nil {
			t.Fatal("unsupported privacy diagnostic accepted", args)
		}
	}
	if _, err := ParseOptions([]string{"-privacy-diagnostic", "representative"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivacyMetadataUsesActualCountersWithNestedUsageDetails(t *testing.T) {
	var entry privacyCallEvidence
	privacyResponseMetadata([]byte(`{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":5004,"completion_tokens":54,"total_tokens":5058,"prompt_tokens_details":{"cached_tokens":4991}},"timings":{"cache_n":4991,"prompt_n":13,"prompt_ms":255.149,"predicted_n":54,"predicted_ms":3339.081}}`), &entry)
	if entry.Usage["prompt_tokens"] != 5004 || entry.ServerTimings["predicted_n"] != 54 || entry.FinishReason != "stop" {
		t.Fatal("measured token counters were discarded because of nested server metadata")
	}
}
