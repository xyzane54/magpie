package routerredact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/routerruntime"
)

type fakeRuntime struct {
	available bool
	calls     int
	body      []byte
}

func (f *fakeRuntime) Require(p routerruntime.Purpose) error {
	if p != routerruntime.Redaction || !f.available {
		return routerruntime.ErrCapability
	}
	return nil
}
func (f *fakeRuntime) Invoke(_ context.Context, p routerruntime.Purpose, b []byte, consume func(*http.Response) error) error {
	if err := f.Require(p); err != nil {
		return err
	}
	f.calls++
	f.body = append([]byte(nil), b...)
	return consume(&http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"synthetic":true}`))})
}

type syntheticSpecification struct{}

func (syntheticSpecification) Versions() Versions { return testInput().Versions }
func (syntheticSpecification) Build(_ context.Context, in Extraction) ([]byte, error) {
	return json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": in.Texts}}, "chat_template_kwargs": map[string]any{"enable_thinking": true}, "stream": true})
}
func (syntheticSpecification) Parse(_ context.Context, _ Extraction, raw []byte) (Result, error) {
	var response map[string]bool
	if json.Unmarshal(raw, &response) != nil || !response["synthetic"] {
		return Result{}, errors.New("synthetic invalid")
	}
	return Result{Complete: true}, nil
}

func TestRuntimeSpecificationAndCapabilityGate(t *testing.T) {
	runtime := &fakeRuntime{available: true}
	p := New(Options{Extractor: NewRuntimeExtractor(runtime, nil)})
	defer p.Close()
	if _, err := p.Prepare(context.Background(), testInput("Alice")); err == nil || runtime.calls != 0 {
		t.Fatal("missing production spec invoked model")
	}
	p2 := New(Options{Extractor: NewRuntimeExtractor(runtime, syntheticSpecification{})})
	defer p2.Close()
	prepared := mustPrepare(t, p2, testInput("Alice"))
	_ = prepared
	var request map[string]any
	if json.Unmarshal(runtime.body, &request) != nil {
		t.Fatal("invalid runtime request")
	}
	if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false || request["stream"] != false || runtime.calls != 1 {
		t.Fatal("thinking or runtime purpose not enforced")
	}
	runtime.available = false
	if _, err := p2.Prepare(context.Background(), testInput("Alice")); err == nil || runtime.calls != 1 {
		t.Fatal("cached hit bypassed current capability gate")
	}
	runtime.available = true
	in := testInput("Alice")
	in.Versions.Model = "unconfirmed-model"
	if _, err := p2.Prepare(context.Background(), in); err == nil || runtime.calls != 1 {
		t.Fatal("version mismatch invoked runtime")
	}
}
