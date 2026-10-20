package routerservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

func TestSparkModelListHTTPPreservesCustomTargetAndUpstreamSource(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-cloud-api-key" {
			t.Error("model discovery changed the explicit request target or credential")
		}
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"data":[{"id":"custom-account-model"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	layout, _ := fixture(t)
	h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
	in := providerInputFor(upstream.URL + "/v1")
	in.Preset = "spark"
	w := providerRequest(t, h, http.MethodPost, "/api/cloud-providers/models", "management-token", in)
	var result providerModelListResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || !reflect.DeepEqual(result.Models, []string{"custom-account-model"}) || result.Source != "" || result.DocsURL != "" {
		t.Fatalf("real upstream list provenance: %d %s", w.Code, w.Body.String())
	}
	w = providerRequest(t, h, http.MethodPost, "/api/cloud-providers/models", "management-token", in)
	var failure struct{ Code string }
	if w.Code != http.StatusBadGateway || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Code != "model_list_unavailable" || calls.Load() != 2 {
		t.Fatalf("custom Spark address received fallback or extra requests: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
}

func TestSparkDocumentedModelsOnlyFollowUnavailableOfficialChatList(t *testing.T) {
	want := []string{"4.0Ultra", "generalv3.5", "max-32k", "generalv3", "pro-128k", "lite"}
	for _, base := range []string{"https://spark-api-open.xf-yun.com/v1", "https://spark-api-open.xf-yun.com/v1/"} {
		t.Run(base, func(t *testing.T) {
			p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: base}
			result, err := completeProviderModelList(p, nil, modelListFailure("model_list_unavailable", "endpoint unavailable"))
			if err != nil || !reflect.DeepEqual(result.Models, want) || result.Source != "official_documentation" || result.DocsURL != sparkModelDocsURL {
				t.Fatalf("documented candidates: %#v, %v", result, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if json.Unmarshal(encoded, &response) != nil || response["source"] != "official_documentation" || response["docsURL"] != sparkModelDocsURL {
				t.Fatalf("candidate provenance missing: %s", encoded)
			}
			result.Models[0] = "mutated"
			next, _ := completeProviderModelList(p, nil, modelListFailure("model_list_unavailable", "endpoint unavailable"))
			if !reflect.DeepEqual(next.Models, want) {
				t.Fatal("documented candidates retained caller mutation")
			}
		})
	}
}

func TestSparkDocumentedModelsNeverReplaceSuccessfulUpstreamLists(t *testing.T) {
	p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: "https://spark-api-open.xf-yun.com/v1"}
	for _, models := range [][]string{{"account-model"}, {}} {
		result, err := completeProviderModelList(p, models, nil)
		if err != nil || !reflect.DeepEqual(result.Models, models) || result.Source != "" || result.DocsURL != "" {
			t.Fatalf("upstream list changed: %#v, %v", result, err)
		}
	}
}

func TestSparkDocumentedModelsPreserveOtherFailures(t *testing.T) {
	p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: "https://spark-api-open.xf-yun.com/v1"}
	failures := []error{
		modelListFailure("model_list_auth_failed", "HTTP 401"),
		modelListFailure("model_list_forbidden", "HTTP 403"),
		modelListFailure("model_list_timeout", "request timed out"),
		modelListFailure("model_list_connection_failed", "connection failed"),
		modelListFailure("model_list_invalid_response", "unsupported format"),
		modelListFailure("model_list_rate_limited", "HTTP 429"),
		modelListFailure("model_list_upstream_failed", "upstream failed"),
		context.DeadlineExceeded,
		context.Canceled,
		errors.New("model_list_unavailable"),
	}
	for _, failure := range failures {
		t.Run(failure.Error(), func(t *testing.T) {
			result, err := completeProviderModelList(p, nil, failure)
			if err != failure || result.Models != nil || result.Source != "" || result.DocsURL != "" {
				t.Fatalf("failure hidden by candidates: %#v, %v", result, err)
			}
		})
	}
}

func TestSparkDocumentedModelsDoNotApplyToOtherTargets(t *testing.T) {
	for _, base := range []string{
		"http://spark-api-open.xf-yun.com/v1",
		"https://spark-api-open.xf-yun.com:443/v1",
		"https://spark-api-open.xf-yun.com.evil.invalid/v1",
		"https://relay.invalid/v1",
		"https://spark-api-open.xf-yun.com/other",
		"https://spark-api-open.xf-yun.com/v2",
		"https://spark-api-open.xf-yun.com/v1/other",
		"https://spark-api-open.xf-yun.com/v%31",
		"https://spark-api-open.xf-yun.com/v1?target=other",
		"https://spark-api-open.xf-yun.com/v1?",
		"https://spark-api-open.xf-yun.com/v1#other",
		"https://user@spark-api-open.xf-yun.com/v1",
		":invalid",
	} {
		t.Run(base, func(t *testing.T) {
			p := routerconfig.CloudProvider{Preset: "spark", Protocol: "chat", BaseURL: base}
			failure := modelListFailure("model_list_unavailable", "endpoint unavailable")
			result, err := completeProviderModelList(p, nil, failure)
			if err != failure || result.Models != nil || result.Source != "" {
				t.Fatalf("nonofficial target received candidates: %#v, %v", result, err)
			}
		})
	}
	for _, protocol := range []string{"responses", "anthropic", ""} {
		t.Run(protocol, func(t *testing.T) {
			p := routerconfig.CloudProvider{Protocol: protocol, BaseURL: "https://spark-api-open.xf-yun.com/v1"}
			failure := modelListFailure("model_list_unavailable", "endpoint unavailable")
			result, err := completeProviderModelList(p, nil, failure)
			if err != failure || result.Models != nil || result.Source != "" {
				t.Fatalf("non-Chat protocol received candidates: %#v, %v", result, err)
			}
		})
	}
}

func TestSparkMaaSEmptyOrUnavailableListKeepsExamplesSeparate(t *testing.T) {
	for _, base := range []string{"https://maas-api.cn-huabei-1.xf-yun.com/v2", "https://maas-api.cn-huabei-1.xf-yun.com/v2/"} {
		for _, failure := range []error{nil, modelListFailure("model_list_unavailable", "endpoint unavailable")} {
			p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: base}
			models := []string{}
			if failure != nil {
				models = nil
			}
			result, err := completeProviderModelList(p, models, failure)
			if err != nil || result.Models == nil || len(result.Models) != 0 || !reflect.DeepEqual(result.DocumentedModels, []string{"spark-x2.5", "spark-x2.5-4b", "spark-x2.5-1.7b"}) || result.Source != "official_examples" || result.DocsURL != sparkMaaSDocsURL {
				t.Fatalf("MaaS example replaced real list or lost its provenance: %#v, %v", result, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]json.RawMessage
			if json.Unmarshal(encoded, &response) != nil || string(response["models"]) != "[]" || string(response["documentedModels"]) != `["spark-x2.5","spark-x2.5-4b","spark-x2.5-1.7b"]` || string(response["source"]) != `"official_examples"` {
				t.Fatalf("MaaS example response misrepresented the upstream list: %s", encoded)
			}
		}
	}
}

func TestSparkMaaSExamplesPreserveNonemptyAndUnknownLists(t *testing.T) {
	p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: "https://maas-api.cn-huabei-1.xf-yun.com/v2"}
	for _, models := range [][]string{{"account-model"}, nil} {
		result, err := completeProviderModelList(p, models, nil)
		if err != nil || !reflect.DeepEqual(result.Models, models) || result.DocumentedModels != nil || result.Source != "" || result.DocsURL != "" {
			t.Fatalf("MaaS real or unknown list changed: %#v, %v", result, err)
		}
	}
}

func TestSparkMaaSExamplesDoNotHideFailures(t *testing.T) {
	p := routerconfig.CloudProvider{Protocol: "chat", BaseURL: "https://maas-api.cn-huabei-1.xf-yun.com/v2"}
	for _, failure := range []error{
		modelListFailure("model_list_auth_failed", "HTTP 401"),
		modelListFailure("model_list_forbidden", "HTTP 403"),
		modelListFailure("model_list_timeout", "request timed out"),
		modelListFailure("model_list_connection_failed", "connection failed"),
		modelListFailure("model_list_invalid_response", "unsupported format"),
		modelListFailure("model_list_rate_limited", "HTTP 429"),
		modelListFailure("model_list_upstream_failed", "upstream failed"),
		context.DeadlineExceeded,
		context.Canceled,
		errors.New("model_list_unavailable"),
	} {
		result, err := completeProviderModelList(p, []string{}, failure)
		if err != failure || result.Models != nil || result.DocumentedModels != nil || result.Source != "" || result.DocsURL != "" {
			t.Fatalf("MaaS failure hidden by examples: %#v, %v", result, err)
		}
	}
}

func TestSparkMaaSExamplesRequireExactOfficialChatTarget(t *testing.T) {
	for _, base := range []string{
		"http://maas-api.cn-huabei-1.xf-yun.com/v2",
		"https://maas-api.cn-huabei-1.xf-yun.com:443/v2",
		"https://maas-api.cn-huabei-1.xf-yun.com.evil.invalid/v2",
		"https://relay.invalid/v2",
		"https://maas-api.cn-huabei-1.xf-yun.com/v1",
		"https://maas-api.cn-huabei-1.xf-yun.com/v2/other",
		"https://maas-api.cn-huabei-1.xf-yun.com/v%32",
		"https://maas-api.cn-huabei-1.xf-yun.com/v2?target=other",
		"https://maas-api.cn-huabei-1.xf-yun.com/v2?",
		"https://maas-api.cn-huabei-1.xf-yun.com/v2#other",
		"https://user@maas-api.cn-huabei-1.xf-yun.com/v2",
		":invalid",
	} {
		for _, failure := range []error{nil, modelListFailure("model_list_unavailable", "endpoint unavailable")} {
			p := routerconfig.CloudProvider{Preset: "spark", Protocol: "chat", BaseURL: base}
			result, err := completeProviderModelList(p, []string{}, failure)
			if err != failure || result.DocumentedModels != nil || result.Source != "" || result.DocsURL != "" || failure == nil && (result.Models == nil || len(result.Models) != 0) {
				t.Fatalf("nonofficial MaaS target received examples: %s %#v, %v", base, result, err)
			}
		}
	}
	for _, protocol := range []string{"responses", "anthropic", ""} {
		p := routerconfig.CloudProvider{Protocol: protocol, BaseURL: "https://maas-api.cn-huabei-1.xf-yun.com/v2"}
		for _, failure := range []error{nil, modelListFailure("model_list_unavailable", "endpoint unavailable")} {
			result, err := completeProviderModelList(p, []string{}, failure)
			if err != failure || result.DocumentedModels != nil || result.Source != "" || result.DocsURL != "" {
				t.Fatalf("non-Chat MaaS protocol received examples: %s %#v, %v", protocol, result, err)
			}
		}
	}
}
