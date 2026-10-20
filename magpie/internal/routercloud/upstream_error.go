package routercloud

import (
	"encoding/json"
	"io"
	"strings"
)

// Only known machine codes may refine a safe error. Upstream messages, model
// names, request IDs and arbitrary fields never enter public errors or events.
// A model directory is not proof that the supplied key can generate with an ID.
func upstreamFailure(status int, body io.Reader) *Error {
	fallback := statusFailure(status)
	switch status {
	case 400, 401, 403, 404, 422:
	default:
		return fallback
	}
	const limit = 32 << 10
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || len(data) > limit {
		return fallback
	}
	var fields struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &fields) != nil {
		return fallback
	}
	for _, code := range []string{fields.Code, fields.Type, fields.Error.Code, fields.Error.Type} {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(code))
		switch normalized {
		case "modelnotfound", "modelnotexist", "modeldoesnotexist", "invalidmodel", "modelnotsupported":
			if status == 400 || status == 404 || status == 422 {
				return failure("cloud_model_not_found", "the selected model is unavailable at this API address; choose a supported model ID", status)
			}
		case "invalidapikey", "invalidtoken", "authenticationerror":
			if status == 401 || status == 403 {
				return failure("cloud_auth_error", "cloud authentication failed; check the key and its service address", status)
			}
		}
	}
	return fallback
}
