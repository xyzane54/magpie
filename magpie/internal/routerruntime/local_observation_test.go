package routerruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
)

func TestLocalCallObservationContractIsSafeAndNullable(t *testing.T) {
	zero := int64(0)
	usage := (&reportedUsageFixture{input: &zero}).value()
	observation := LocalCallObservation{
		RequestID: "req-1", CallID: "call-1", Check: false,
		Purpose: LocalCallExecution, Mode: LocalCallFormal,
		ModelID: "spark-x2.5-1.7B", ModelFingerprint: strings.Repeat("a", 64),
		RuntimeID: "windows-amd64-cpu-b10828", RuntimeFingerprint: strings.Repeat("b", 64),
		MaterialVersion: "execution-v1", PromptVersion: "prompt-v1", Suite: "execution-v1",
		StartedAt: time.Unix(1, 0).UTC(), EndedAt: time.Unix(2, 0).UTC(),
		Outcome: LocalCallSucceeded, Attempted: true,
		Usage: usage,
	}
	b, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, forbidden := range []string{"prompt text", "tool argument", "https://", "secret", "entity", "mapping"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe observation field %q in %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"InputTokens":0`) {
		t.Fatalf("explicit zero usage was not preserved: %s", text)
	}
	var roundTrip LocalCallObservation
	if err := json.Unmarshal(b, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Usage == nil || roundTrip.Usage.InputTokens == nil || *roundTrip.Usage.InputTokens != 0 {
		t.Fatalf("usage did not round-trip: %+v", roundTrip.Usage)
	}
	if _, err := json.Marshal(LocalCallObservation{CallID: "unknown", Purpose: LocalCallJudgment, Mode: LocalCallFormal}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerObserverEmitsOneSafeBusinessCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer server.Close()
	events := make(chan LocalCallObservation, 2)
	m, _, factory, _ := managerFixture(t, func(o *Options, _ *fixtureInstaller, f *fixtureFactory) {
		o.LocalObserver = LocalCallObserverFunc(func(value LocalCallObservation) { events <- value })
		f.endpoint = server.URL
	})
	if err := m.CheckCapability(context.Background(), Check{Purpose: Execution, Suite: "execution-observer-v1", Run: func(context.Context, string) (string, error) { return "evidence", nil }}); err != nil {
		t.Fatal(err)
	}
	if err := m.Invoke(context.Background(), Execution, []byte(`{"messages":[]}`), func(response *http.Response) error {
		_, err := io.ReadAll(response.Body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.CallID == "" || event.Purpose != LocalCallExecution || event.Outcome != LocalCallSucceeded || !event.Attempted || event.RequestID != "" || event.Check {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not receive completion")
	}
	if factory.count.Load() != 1 {
		t.Fatalf("unexpected process count: %d", factory.count.Load())
	}
}

// Keep the fixture local to the contract test while using the production
// ReportedUsage type. It makes the nil-versus-zero rule visible without adding
// a second usage schema to routerruntime.
type reportedUsageFixture struct{ input *int64 }

func (f reportedUsageFixture) value() *cloudcodec.ReportedUsage {
	return &cloudcodec.ReportedUsage{InputTokens: f.input}
}
