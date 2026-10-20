package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
)

type HTTPChecker struct {
	ValidateApp     func(context.Context, Options) error
	managementToken string
	skipModelProbe  bool
}

func ProductionChecker(t ...string) Checker { return newHTTPChecker(false, t...) }
func FastChecker(t ...string) Checker       { return newHTTPChecker(true, t...) }
func newHTTPChecker(skip bool, t ...string) HTTPChecker {
	if len(t) == 1 {
		return HTTPChecker{managementToken: t[0], skipModelProbe: skip}
	}
	return HTTPChecker{skipModelProbe: skip}
}

type safeCheckError struct{ error }

func (c HTTPChecker) Check(ctx context.Context, o Options) (result error) {
	defer func() {
		if result != nil {
			result = safeCheckError{result}
		}
	}()
	if err := validateOptions(o); err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request := func(method, path string, body []byte, credential string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(o.RouterURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("cannot construct local check")
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("local endpoint unavailable; start Router and retry")
		}
		defer resp.Body.Close()
		if data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1)); err != nil || len(data) > 1<<20 {
			return nil, fmt.Errorf("invalid or oversized local check response")
		} else if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("local check returned HTTP %d; inspect Router app/provider status", resp.StatusCode)
		} else {
			return data, nil
		}
	}
	var h struct{ Service, Status string }
	if health, err := request(http.MethodGet, "/healthz", nil, ""); err != nil {
		return fmt.Errorf("Router health: %w", err)
	} else if json.Unmarshal(health, &h) != nil || h.Service != "api-router" || h.Status != "ok" {
		return fmt.Errorf("Router health: endpoint is not a healthy API-router service")
	}
	if c.skipModelProbe {
		return nil
	}
	if c.ValidateApp != nil {
		if err := c.ValidateApp(ctx, o); err != nil {
			return fmt.Errorf("application configuration invalid; verify app/Profile, provider credential and model")
		}
	} else if strings.TrimSpace(c.managementToken) == "" || strings.ContainsAny(c.managementToken, "\r\n") {
		return fmt.Errorf("application validation unavailable: set --management-token-env to a current management token variable; it is used only to read /api/apps")
	} else if apps, err := request(http.MethodGet, "/api/apps", nil, c.managementToken); err != nil {
		return fmt.Errorf("application validation: %w", err)
	} else if err := validateAppProjection(apps, o); err != nil {
		return err
	}
	path, body := "/app/codex/v1/responses", map[string]any{"model": o.Model, "input": "Reply with OK.", "stream": false, "max_output_tokens": 128}
	if o.Host == "claude-code" {
		path, body = "/app/claude-code/v1/messages", map[string]any{"model": o.Model, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}}, "max_tokens": 128, "stream": false}
	}
	raw, _ := json.Marshal(body)
	if response, err := request(http.MethodPost, path, raw, ""); err != nil {
		return fmt.Errorf("model business entry: %w", err)
	} else if !validProbe(o.Host, response) {
		return fmt.Errorf("model business entry did not return a completed text result")
	}
	return nil
}

func validProbe(host string, raw []byte) bool {
	type block struct{ Type, Text string }
	var r struct {
		Object, Type, Status string
		StopReason           string          `json:"stop_reason"`
		Error                json.RawMessage `json:"error"`
		Incomplete           json.RawMessage `json:"incomplete_details"`
		Output               []struct {
			Type, Role, Status string
			Content            []block
		}
		Content []block
	}
	if json.Unmarshal(raw, &r) != nil || (len(r.Error) > 0 && string(r.Error) != "null") {
		return false
	}
	if host == "claude-code" {
		for _, b := range r.Content {
			if r.Type == "message" && r.StopReason == "end_turn" && b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return true
			}
		}
		return false
	}
	for _, item := range r.Output {
		for _, b := range item.Content {
			if r.Object == "response" && r.Status == "completed" && (len(r.Incomplete) == 0 || string(r.Incomplete) == "null") && item.Type == "message" && item.Role == "assistant" && item.Status == "completed" && b.Type == "output_text" && strings.TrimSpace(b.Text) != "" {
				return true
			}
		}
	}
	return false
}

func validateAppProjection(raw []byte, o Options) error {
	var proj struct {
		SchemaVersion int
		Apps          []struct {
			SchemaVersion, Revision  int
			ID, Path, CredentialMode string
			Configured               bool
			Models                   []string
			Cloud                    struct{ ProviderID, Model string }
		}
	}
	if json.Unmarshal(raw, &proj) != nil || proj.SchemaVersion != 1 || proj.Apps == nil {
		return fmt.Errorf("application validation: unsupported /api/apps response; integrate the schema 1 application API before applying host configuration")
	}
	ids, idx := map[string]bool{}, -1
	for i, a := range proj.Apps {
		if a.ID == "" || ids[a.ID] {
			return fmt.Errorf("application validation: ambiguous application identities in /api/apps")
		}
		if ids[a.ID] = true; a.ID == o.Host {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("application validation: register the selected host in Router before applying")
	}
	a := proj.Apps[idx]
	cloudBound := strings.TrimSpace(a.Cloud.ProviderID) != "" && a.Cloud.Model == o.Model
	virtualBound := (strings.HasPrefix(o.Model, "group/") || strings.HasPrefix(o.Model, "local/")) && len(a.Models) > 0 && a.Models[0] == o.Model
	if a.SchemaVersion != 1 || a.Revision < 1 || !a.Configured || (!cloudBound && !virtualBound) || a.Path != "/app/"+o.Host+"/v1" || a.CredentialMode != "stored" {
		return fmt.Errorf("application validation: configure the selected host's stored credential and exact Profile/model binding in Router, then preview again")
	}
	return nil
}

func (e Engine) replace(root string, f snapshot) error {
	if s, err := e.readSnapshot(root, f.FilePlan); err != nil {
		return err
	} else if s.Existed != f.Existed || !equal(s.Before, f.Before) || s.Mode != f.Mode {
		return fmt.Errorf("configuration changed before replacement: %s", f.Name)
	} else if f.After == nil {
		if !s.Existed {
			return nil
		}
		return os.Remove(hostPath(root, f.Name))
	} else if s.Existed && s.Mode&0o222 == 0 {
		return fmt.Errorf("configuration is read-only: %s", f.Name)
	}
	return e.writePrivate(hostPath(root, f.Name), f.After, 0o600)
}

func loomyCleanEmptyContainers(out []byte, before map[string]any, models []string) ([]byte, error) {
	var cleanPaths [][]string
	for _, m := range models {
		cleanPaths = append(cleanPaths, []string{"provider", loomyProvider, "models", m})
	}
	for _, path := range append(cleanPaths, []string{"provider", loomyProvider, "models"}, []string{"provider", loomyProvider, "options"}, []string{"provider", loomyProvider}, []string{"provider"}) {
		if _, existed := lookup(before, path); !existed {
			doc, err := readJSON(out)
			if err != nil {
				return nil, errors.New("invalid restored Loomy configuration")
			}
			if v, exists := lookup(doc, path); exists {
				if m, ok := v.(map[string]any); ok && len(m) == 0 {
					if out, err = deleteJSONField(out, loomyJSONPath(path)); err != nil {
						return nil, errors.New("cannot clean restored Loomy configuration")
					}
				}
			}
		}
	}
	_, err := readJSON(out)
	return out, err
}

func workBuddyReplaceModel(raw []byte, index int, replacement []byte) ([]byte, error) {
	if v := workBuddyModelSpan(raw, index); v.IsObject() {
		out := spliceBytes(raw, v.Index, v.Index+len(v.Raw), replacement)
		_, err := workBuddyModels(out)
		return out, err
	}
	return nil, fmt.Errorf("cannot locate WorkBuddy model")
}

func workBuddyPatchModel(raw []byte, index int, values map[string]any, del []string) ([]byte, error) {
	var set []edit.KV
	for _, key := range workBuddyConnectionFields {
		if value, exists := values[key]; exists {
			set = append(set, edit.KV{Path: key, Value: value})
		}
	}
	if patched, err := edit.PatchJSON(workBuddyModelRaw(raw, index), set, del); err != nil {
		return nil, err
	} else {
		return workBuddyReplaceModel(raw, index, patched)
	}
}

func workBuddyAppendModel(raw []byte, entry map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	v := gjson.ParseBytes(raw)
	if !v.IsArray() {
		v = v.Get("models")
	}
	if !v.Exists() {
		return edit.PatchJSON(raw, []edit.KV{{Path: "models", Value: []any{entry}}}, nil)
	}
	at, last := v.Index+1, gjson.Result{}
	v.ForEach(func(_, item gjson.Result) bool { last = item; return true })
	if last.Exists() {
		at, encoded = last.Index+len(last.Raw), append([]byte(","), encoded...)
	}
	out := spliceBytes(raw, at, at, encoded)
	_, err = workBuddyModels(out)
	return out, err
}

func workBuddyRemoveModel(raw []byte, index int) ([]byte, error) {
	v := workBuddyModelSpan(raw, index)
	if !v.IsObject() {
		return nil, fmt.Errorf("cannot locate WorkBuddy model to remove")
	}
	a, b, clean := v.Index, v.Index+len(v.Raw), jsonc.ToJSONInPlace(bytes.Clone(raw))
	j, k, out := b, a, raw
	for j < len(clean) && clean[j] <= ' ' {
		j++
	}
	for k > 0 && clean[k-1] <= ' ' {
		k--
	}
	if j < len(clean) && clean[j] == ',' {
		out = spliceBytes(out, j, j+1, nil)
	} else if k > 0 && clean[k-1] == ',' {
		out, a, b = spliceBytes(out, k-1, k, nil), a-1, b-1
	}
	out = spliceBytes(out, a, b, nil)
	_, err := workBuddyModels(out)
	return out, err
}
