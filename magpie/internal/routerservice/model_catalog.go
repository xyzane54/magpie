package routerservice

import (
	"net/url"
	"strings"
)

// ordinaryModelCandidates removes platform-specific services that this product's
// standard HTTP transport cannot provide. CMaaS needs its own SDK for attestation
// and encrypted inference; discovery alone must not advertise that integration.
// Match the actual endpoint, not a preset that a custom relay may have retained.
// https://help.aliyun.com/zh/model-studio/confidential-maas-best-practice
func ordinaryModelCandidates(base string, models []string) []string {
	address, err := url.Parse(base)
	if err != nil {
		return models
	}
	switch strings.TrimSuffix(strings.ToLower(address.Hostname()), ".") {
	case "maas.qianwenaiapi.com", "dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com":
	default:
		return models
	}
	ordinary := make([]string, 0, len(models))
	for _, model := range models {
		if !strings.HasPrefix(model, "cmaas-") {
			ordinary = append(ordinary, model)
		}
	}
	return ordinary
}
