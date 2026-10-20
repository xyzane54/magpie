package routerservice

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/routerconfig"
)

// Exercise the management HTTP entry with an entirely local proxy fixture:
// official hostnames identify the platform, but no DNS or vendor request occurs.
func TestModelCatalogFiltersSpecialAccessOnlyOnOfficialPlatforms(t *testing.T) {
	all := []string{"cmaas-qwen3.8-flash", "qwen3.8-flash", "cmaas-deepseek-v4-pro-0813", "kimi-k3", "my-cmaas-model"}
	ordinary := []string{"qwen3.8-flash", "kimi-k3", "my-cmaas-model"}
	for _, tc := range []struct {
		name, base, preset string
		want               []string
	}{
		{"qianwen", "http://maas.qianwenaiapi.com/compatible-mode/v1", "", ordinary},
		{"bailian-beijing", "http://dashscope.aliyuncs.com/compatible-mode/v1", "", ordinary},
		{"model-studio-singapore", "http://dashscope-intl.aliyuncs.com/compatible-mode/v1", "", ordinary},
		{"canonical-host", "http://MAAS.QIANWENAIAPI.COM./compatible-mode/v1", "", ordinary},
		{"custom-relay", "http://relay.invalid/v1", "qianwen-ai", all},
		{"unrelated-vendor", "http://api.openai.com/v1", "", all},
		{"hostname-suffix", "http://maas.qianwenaiapi.com.relay.invalid/v1", "", all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-cloud-api-key" {
					t.Error("model fetch changed method or precise provider credential")
				}
				_, _ = io.WriteString(w, `{"data":[{"id":"cmaas-qwen3.8-flash"},{"id":"qwen3.8-flash"},{"id":"cmaas-deepseek-v4-pro-0813"},{"id":"kimi-k3"},{"id":"my-cmaas-model"},{"id":"qwen3.8-flash"}]}`)
			}))
			defer proxy.Close()
			layout, _ := fixture(t)
			h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
			before, err := os.ReadFile(layout.ConfigFile())
			if err != nil {
				t.Fatal(err)
			}
			in := providerInputFor(tc.base)
			in.Preset, in.Model = tc.preset, ""
			in.ProxyMode, in.ProxyURL = "custom", ptr(proxy.URL)
			response := providerRequest(t, h, "POST", "/api/cloud-providers/models", "management-token", in)
			var result struct {
				Models []string `json:"models"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || !reflect.DeepEqual(result.Models, tc.want) {
				t.Fatalf("catalog: status=%d body=%s want=%v", response.Code, response.Body.String(), tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("filtering started extra requests: %d", calls.Load())
			}
			after, err := os.ReadFile(layout.ConfigFile())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("filtering changed provider or Profile configuration")
			}
			if _, err := os.Stat(layout.CredentialsFile()); !os.IsNotExist(err) {
				t.Fatal("draft filtering wrote credentials")
			}
		})
	}
}

func TestFilteredModelCatalogKeepsValidationAndBounds(t *testing.T) {
	oversized := make([]string, 1001)
	for i := range oversized {
		oversized[i] = "cmaas-model-" + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name   string
		ids    []string
		status int
	}{
		{"all-special", []string{"cmaas-qwen3.8-flash"}, 200},
		{"invalid-special-id", []string{"cmaas-model\ninvalid"}, 502},
		{"oversized-special-catalog", oversized, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entries := make([]map[string]string, 0, len(tc.ids))
				for _, id := range tc.ids {
					entries = append(entries, map[string]string{"id": id})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": entries})
			}))
			defer proxy.Close()
			layout, _ := fixture(t)
			h := handler(Info{Address: "127.0.0.1:13145"}, "management-token", routerconfig.New(layout))
			in := providerInputFor("http://maas.qianwenaiapi.com/compatible-mode/v1")
			in.ProxyMode, in.ProxyURL = "custom", ptr(proxy.URL)
			response := providerRequest(t, h, "POST", "/api/cloud-providers/models", "management-token", in)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", response.Code, response.Body.String(), tc.status)
			}
			if tc.status == 200 && response.Body.String() != "{\"models\":[]}\n" {
				t.Fatalf("all-special catalog should return an empty array: %s", response.Body.String())
			}
		})
	}
}
