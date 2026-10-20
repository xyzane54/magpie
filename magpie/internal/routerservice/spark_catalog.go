package routerservice

import (
	"errors"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/routerconfig"
)

const sparkModelDocsURL = "https://www.xfyun.cn/doc/spark/HTTP调用文档.html"
const sparkMaaSDocsURL = "https://www.xfyun.cn/doc/spark/Spark-X2.5.html"

type providerModelListResponse struct {
	Models           []string `json:"models"`
	DocumentedModels []string `json:"documentedModels,omitempty"`
	Source           string   `json:"source,omitempty"`
	DocsURL          string   `json:"docsURL,omitempty"`
}

// completeProviderModelList preserves real model lists. Missing official Spark
// Chat lists permit documented candidates; an empty or unavailable official
// MaaS Chat list permits separately labeled documentation examples. Neither
// reference establishes account entitlement or successful generation.
func completeProviderModelList(p routerconfig.CloudProvider, models []string, err error) (providerModelListResponse, error) {
	var result providerModelListResponse
	if err == nil {
		result.Models = models
		if len(models) > 0 {
			return result, nil
		}
	} else {
		var failure *modelListError
		if !errors.As(err, &failure) || failure == nil || failure.Code != "model_list_unavailable" {
			return result, err
		}
	}
	if p.Protocol != "chat" {
		return result, err
	}
	address, parseErr := url.Parse(p.BaseURL)
	if parseErr != nil || address.Scheme != "https" || address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" {
		return result, err
	}
	path := address.EscapedPath()
	if strings.EqualFold(address.Host, "spark-api-open.xf-yun.com") && (path == "/v1" || path == "/v1/") && err != nil {
		return providerModelListResponse{
			Models:  []string{"4.0Ultra", "generalv3.5", "max-32k", "generalv3", "pro-128k", "lite"},
			Source:  "official_documentation",
			DocsURL: sparkModelDocsURL,
		}, nil
	}
	if strings.EqualFold(address.Host, "maas-api.cn-huabei-1.xf-yun.com") && (path == "/v2" || path == "/v2/") && (err != nil || models != nil) {
		return providerModelListResponse{
			Models:           []string{},
			DocumentedModels: []string{"spark-x2.5", "spark-x2.5-4b", "spark-x2.5-1.7b"},
			Source:           "official_examples",
			DocsURL:          sparkMaaSDocsURL,
		}, nil
	}
	return result, err
}
