package routerredact

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPrivacyBuildSendsStrictSchemaForAllCompleteFields(t *testing.T) {
	spec := privacyParseSpec(t)
	in := Extraction{Versions: spec.Versions(), Texts: []string{"", "合成数据", "😀"}}
	body, err := spec.Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		ResponseFormat struct {
			Type   string         `json:"type"`
			Schema map[string]any `json:"schema"`
		} `json:"response_format"`
	}
	if json.Unmarshal(body, &request) != nil || request.ResponseFormat.Type != "json_object" || request.ResponseFormat.Schema == nil {
		t.Fatal("model request does not constrain output to the strict privacy schema")
	}
	var want map[string]any
	if json.Unmarshal([]byte(PrivacySchema()), &want) != nil {
		t.Fatal("invalid embedded schema")
	}
	fields := want["properties"].(map[string]any)["fields"].(map[string]any)
	fields["minItems"], fields["maxItems"] = float64(3), float64(3)
	if !reflect.DeepEqual(request.ResponseFormat.Schema, want) {
		t.Fatal("generation schema omitted or weakened validation/complete field coverage")
	}
	if spec.CapabilitySuite()[:14] != "privacy-go-v5:" {
		t.Fatal("changed generation policy inherited an old capability suite")
	}
}

func TestPrivacyLongPositionGuideRetainsAllCharactersAndFinalPartialBlock(t *testing.T) {
	spec := privacyParseSpec(t)
	text := strings.Repeat("前", 128) + "尾"
	body, err := spec.Build(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{text}})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	var input struct {
		Fields []struct{ Text, Positions string } `json:"fields"`
	}
	if json.Unmarshal(body, &request) != nil || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		t.Fatal("invalid block guide request")
	}
	f := input.Fields[0]
	if f.Text != text || strings.Count(f.Positions, "前") != 128 || !strings.HasPrefix(f.Positions, "0:\"前前前前前前前前\" 8:") || !strings.HasSuffix(f.Positions, "120:\"前前前前前前前前\" 128:\"尾\" end=129") {
		t.Fatal("block guide shortened or changed complete source text")
	}
}

type rejectResultSpecification struct{ syntheticSpecification }

func (rejectResultSpecification) Parse(context.Context, Extraction, []byte) (Result, error) {
	return Result{}, privacyInvalid("span_text")
}

func TestRuntimePreservesSafeInvalidResultInsteadOfExtractionFailure(t *testing.T) {
	runtime := &fakeRuntime{available: true}
	_, err := NewRuntimeExtractor(runtime, rejectResultSpecification{}).Extract(context.Background(), Extraction{Versions: testInput().Versions, Texts: []string{"synthetic"}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "redaction_invalid_result" {
		t.Fatal("strict parser rejection was folded into a transport/extraction failure", err)
	}
}

func TestPrivacyPositionGuideCoversEveryOriginalCodepoint(t *testing.T) {
	spec := privacyParseSpec(t)
	text := "😀e\u0301\n\"末尾"
	body, err := spec.Build(context.Background(), Extraction{Versions: spec.Versions(), Texts: []string{text, ""}})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	var input struct {
		Fields []struct{ Text, Positions string } `json:"fields"`
	}
	if json.Unmarshal(body, &request) != nil || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		t.Fatal("invalid guide request")
	}
	if len(input.Fields) != 2 || input.Fields[0].Text != text || input.Fields[0].Positions != "0:\"😀\" 1:\"e\" 2:\"́\" 3:\"\\n\" 4:\"\\\"\" 5:\"末\" 6:\"尾\" end=7" || input.Fields[1].Positions != "end=0" {
		t.Fatal("position guide omitted, changed or misnumbered complete source codepoints")
	}
}
