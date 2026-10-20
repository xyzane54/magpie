package cloudcodec

import (
	"reflect"
	"testing"
)

func TestResponseCapabilitiesInspectTypedResultAnnotations(t *testing.T) {
	typed := []byte(`{"input":[{"type":"function_call_output","call_id":"fixture","output":[{"type":"output_text","text":"source","annotations":[{"type":"url_citation","url":"https://example.com","title":"source","start_index":0,"end_index":6}]}]}]}`)
	if got := ResponseCapabilities(typed); !reflect.DeepEqual(got, []string{"web_search"}) {
		t.Fatal("typed output annotation escaped capability check", got)
	}
	opaque := []byte(`{"input":[{"type":"function_call_output","call_id":"fixture","output":"{\"annotations\":[{\"type\":\"url_citation\"}]}"}],"tools":[{"type":"function","name":"run","parameters":{"type":"object","properties":{"annotations":{"type":"array"},"phase":{"type":"string"}}}}]}`)
	if got := ResponseCapabilities(opaque); len(got) != 0 {
		t.Fatal("business data treated as wire capability", got)
	}
}
