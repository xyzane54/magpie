package routerredact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }
func testInput(texts ...string) Input {
	messages := []any{}
	for _, text := range texts {
		messages = append(messages, map[string]any{"role": "user", "content": text})
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": messages})
	return Input{RequestID: "req", AppID: "app", Protocol: Chat, Operation: "generation", SessionID: "verified", SessionVerified: true, Versions: Versions{"model-v1", "prompt-v1", "config-v1"}, Body: body}
}

// synthetic is deliberately test-only literal matching, not a recognition model.
func synthetic(values ...string) ExtractorFunc {
	return func(_ context.Context, in Extraction) (Result, error) {
		out := Result{Complete: true}
		for field, text := range in.Texts {
			for _, value := range values {
				for offset := 0; offset < len(text); {
					at := strings.Index(text[offset:], value)
					if at < 0 {
						break
					}
					at += offset
					out.Entities = append(out.Entities, Entity{field, at, at + len(value), value, "PERSON"})
					offset = at + len(value)
				}
			}
		}
		return out, nil
	}
}
func bodyString(t *testing.T, p *Prepared) string {
	t.Helper()
	b, e := p.Body()
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func mustPrepare(t *testing.T, p *Pipeline, in Input) *Prepared {
	t.Helper()
	out, e := p.Prepare(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(out.Close)
	return out
}

func TestDeterministicCollisionUnicodeAndOwnership(t *testing.T) {
	p := New(Options{Extractor: synthetic("张三", "Zoë")})
	defer p.Close()
	in := testInput("<PERSON:1> 张三 张三 Zoë 👩🏽‍💻")
	original := string(in.Body)
	prepared := mustPrepare(t, p, in)
	body := bodyString(t, prepared)
	if strings.Contains(body, "张三") || strings.Contains(body, "Zoë") || !strings.Contains(body, "<PERSON:2> <PERSON:2> <PERSON:3>") {
		t.Fatal("collision or duplicate replacement failed", body)
	}
	if string(in.Body) != original {
		t.Fatal("mutated input")
	}
	copyBody, _ := prepared.Body()
	copyBody[0] = '!'
	if bodyString(t, prepared) != body {
		t.Fatal("body is not private")
	}
	restored, err := prepared.RestoreText("<PERSON:1>|<PERSON:2>|<PERSON:3>|<UNKNOWN:1>")
	if err != nil || restored != "<PERSON:1>|张三|Zoë|<UNKNOWN:1>" {
		t.Fatalf("restore failed %q %v", restored, err)
	}
	p2 := New(Options{Extractor: synthetic("张三", "Zoë")})
	defer p2.Close()
	second := mustPrepare(t, p2, in)
	if bodyString(t, second) != body {
		t.Fatal("nondeterministic")
	}
	prepared.Close()
	if _, err := prepared.Body(); err == nil {
		t.Fatal("body accessible after release")
	}
	if _, err := prepared.RestoreText("<PERSON:2>"); err == nil {
		t.Fatal("mapping accessible after release")
	}
}

func TestEntityValidation(t *testing.T) {
	tests := map[string]Result{
		"incomplete": {}, "missing": {true, []Entity{{0, 0, 3, "bad", "PERSON"}}},
		"type": {true, []Entity{{0, 0, 3, "abc", "person"}}}, "field": {true, []Entity{{8, 0, 3, "abc", "PERSON"}}},
		"overlap":           {true, []Entity{{0, 0, 3, "abc", "PERSON"}, {0, 1, 4, "bcd", "PERSON"}}},
		"category conflict": {true, []Entity{{0, 0, 3, "abc", "PERSON"}, {0, 0, 3, "abc", "ORG"}}},
		"byte boundary":     {true, []Entity{{0, 5, 6, string([]byte{0xa0}), "PERSON"}}},
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			p := New(Options{Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return result, nil })})
			defer p.Close()
			out, err := p.Prepare(context.Background(), testInput("abcd你"))
			if err == nil || out != nil {
				t.Fatal("accepted invalid entities")
			}
		})
	}
	p := New(Options{Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) {
		e := Entity{0, 0, 3, "abc", "PERSON"}
		return Result{true, []Entity{e, e}}, nil
	})})
	defer p.Close()
	prepared := mustPrepare(t, p, testInput("abc"))
	if strings.Count(bodyString(t, prepared), "<PERSON:1>") != 1 || p.Stats().Entities != 1 {
		t.Fatal("exact duplicate not deduplicated")
	}
	many := make([]Entity, maxEntities+1)
	if _, e := validateEntities([]string{"a"}, Result{true, many}); e == nil {
		t.Fatal("entity limit")
	}
}

func TestHistoryIsolationInvalidationAndSnapshots(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	var observed [][]string
	p := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
		observed = append(observed, append([]string(nil), in.Texts...))
		return synthetic("Alice", "Bob", "Carol")(c, in)
	}), CacheEntries: 1, CacheTTL: time.Minute, Now: func() time.Time { return now }})
	defer p.Close()
	first := mustPrepare(t, p, testInput("Alice"))
	second := mustPrepare(t, p, testInput("Alice", "Bob"))
	if len(observed) != 2 || len(observed[1]) != 1 || observed[1][0] != "Bob" {
		t.Fatal("history not reused", observed)
	}
	full := mustPrepare(t, p, testInput("Alice", "Bob"))
	_ = full
	if len(observed) != 2 {
		t.Fatal("full hit rescanned")
	}
	edit := mustPrepare(t, p, testInput("Carol", "Bob"))
	if !strings.Contains(bodyString(t, edit), "<PERSON:1>") {
		t.Fatal("edit scan")
	}
	if len(observed[2]) != 2 {
		t.Fatal("edited history reused")
	}
	version := testInput("Carol", "Bob")
	version.Versions.Prompt = "prompt-v2"
	mustPrepare(t, p, version)
	if len(observed[3]) != 2 {
		t.Fatal("version reused")
	}
	app := testInput("Carol", "Bob")
	app.AppID = "other"
	mustPrepare(t, p, app)
	if len(observed[4]) != 2 {
		t.Fatal("cross app reused")
	}
	if restored, _ := first.RestoreText("<PERSON:1>"); restored != "Alice" {
		t.Fatal("eviction invalidated in-flight snapshot")
	}
	if restored, _ := second.RestoreText("<PERSON:2>"); restored != "Bob" {
		t.Fatal("snapshot mapping changed")
	}
	before := len(observed)
	in := testInput("Carol")
	in.SessionVerified = false
	mustPrepare(t, p, in)
	mustPrepare(t, p, in)
	if len(observed) != before+2 {
		t.Fatal("unverified session cached")
	}
	mustPrepare(t, p, testInput("Alice"))
	before = len(observed)
	now = now.Add(time.Minute)
	mustPrepare(t, p, testInput("Alice"))
	if len(observed) != before+1 {
		t.Fatal("TTL failed")
	}
	p.Clear()
	before = len(observed)
	mustPrepare(t, p, testInput("Alice"))
	if len(observed) != before+1 {
		t.Fatal("clear failed")
	}
	collision := mustPrepare(t, p, testInput("Alice", "literal <PERSON:1> Bob"))
	if !strings.Contains(bodyString(t, collision), "<PERSON:2>") {
		t.Fatal("historical literal collision not rescanned")
	}
	if restored, _ := collision.RestoreText("<PERSON:1>"); restored != "<PERSON:1>" {
		t.Fatal("literal confused with mapping")
	}
}

func TestConcurrentSnapshotsAndClearDuringExtraction(t *testing.T) {
	var calls atomic.Int32
	p := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
		calls.Add(1)
		return synthetic("Alice", "Bob", "Carol")(c, in)
	})})
	defer p.Close()
	mustPrepare(t, p, testInput("Alice"))
	var wg sync.WaitGroup
	for _, name := range []string{"Bob", "Carol"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := p.Prepare(context.Background(), testInput("Alice", name))
			if e != nil {
				t.Error(e)
				return
			}
			defer out.Close()
			text, e := out.RestoreText("<PERSON:2>")
			if e != nil || text != name {
				t.Errorf("snapshot got %q", text)
			}
		}()
	}
	wg.Wait()
	started, release := make(chan struct{}), make(chan struct{})
	p2 := New(Options{Extractor: ExtractorFunc(func(c context.Context, in Extraction) (Result, error) {
		close(started)
		<-release
		return synthetic("Alice")(c, in)
	})})
	defer p2.Close()
	done := make(chan *Prepared, 1)
	go func() { v, _ := p2.Prepare(context.Background(), testInput("Alice")); done <- v }()
	<-started
	p2.Clear()
	close(release)
	out := <-done
	if out == nil {
		t.Fatal("preparation failed")
	}
	out.Close()
	p2.mu.Lock()
	size := len(p2.cache)
	p2.mu.Unlock()
	if size != 0 {
		t.Fatal("clear resurrected cache")
	}
}

func TestFailuresAreSafeAndCancelStopsExtraction(t *testing.T) {
	p := New(Options{})
	defer p.Close()
	_, err := p.Prepare(context.Background(), testInput("secret"))
	if err == nil {
		t.Fatal("missing extractor allowed")
	}
	p = New(Options{Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { return Result{}, errors.New("secret <PERSON:1>") })})
	defer p.Close()
	_, err = p.Prepare(context.Background(), testInput("secret"))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe extraction error")
	}
	raw, _ := json.Marshal(p.Stats())
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "PERSON") {
		t.Fatal("statistics leaked")
	}
	calls := 0
	p = New(Options{Extractor: ExtractorFunc(func(context.Context, Extraction) (Result, error) { calls++; return Result{Complete: true}, nil })})
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Prepare(ctx, testInput("secret"))
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("cancellation admitted")
	}
	p = New(Options{Budget: time.Millisecond, Extractor: ExtractorFunc(func(c context.Context, _ Extraction) (Result, error) { <-c.Done(); return Result{Complete: true}, nil })})
	defer p.Close()
	_, err = p.Prepare(context.Background(), testInput("secret"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout admitted", err)
	}
	p.Close()
	_, err = p.Prepare(context.Background(), testInput("secret"))
	if err == nil {
		t.Fatal("closed pipeline admitted")
	}
}

func TestPrivateFormattingAndIdleExpiry(t *testing.T) {
	var clock atomic.Int64
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	p := New(Options{Extractor: synthetic("Alice"), CacheTTL: time.Minute, Now: func() time.Time { return now.Add(time.Duration(clock.Load())) }})
	defer p.Close()
	in := testInput("Alice")
	prepared := mustPrepare(t, p, in)
	for _, value := range []any{p, in, Extraction{Texts: []string{"Alice"}}, Entity{Text: "Alice"}, Result{Entities: []Entity{{Text: "Alice"}}}, prepared, prepared.Snapshot} {
		if text := fmt.Sprintf("%v %+v %#v", value, value, value); strings.Contains(text, "Alice") || strings.Contains(text, "PERSON:1") {
			t.Fatal("private diagnostic leak")
		}
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "Alice") {
			t.Fatal("private JSON leak")
		}
	}
	clock.Store(int64(time.Minute))
	p.Sweep()
	p.mu.Lock()
	size := len(p.cache)
	p.mu.Unlock()
	if size != 0 {
		t.Fatal("idle expiry retained mapping")
	}
	if text, _ := prepared.RestoreText("<PERSON:1>"); text != "Alice" {
		t.Fatal("idle eviction invalidated in-flight mapping")
	}
	p.Close()
	select {
	case <-p.done:
	default:
		t.Fatal("expiry worker outlived Close")
	}
}
