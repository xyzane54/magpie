package routerhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/edit"
)

type WorkBuddyAdapter struct{}

var workBuddyConnectionFields = []string{"url", "apiKey", "supportsToolCall", "useCustomProtocol"}

func (WorkBuddyAdapter) Contract() AdapterContract {
	return AdapterContract{ID: "workbuddy", Version: 1, Files: []FileSpec{
		{Name: "models.json", Fields: []string{"models[id=selected-model].url", "models[id=selected-model].apiKey", "models[id=selected-model].supportsToolCall", "models[id=selected-model].useCustomProtocol"}},
		{Name: "settings.json", Fields: []string{"model"}},
	}}
}

func (WorkBuddyAdapter) Build(o Options, files []FilePlan) ([]FilePlan, error) {
	if o.Host != "workbuddy" || len(files) != 2 || files[0].Name != "models.json" || files[1].Name != "settings.json" {
		return nil, fmt.Errorf("invalid WorkBuddy adapter input")
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	models := o.EffectiveModels()
	for _, m := range models {
		if strings.HasPrefix(m, "custom-local:") {
			return nil, fmt.Errorf("use the wire model ID without custom-local prefix")
		}
	}
	out := make([]FilePlan, len(files))
	for i, f := range files {
		out[i] = cloneFile(f)
		if f.Existed && len(bytes.TrimSpace(f.Before)) == 0 {
			return nil, fmt.Errorf("empty WorkBuddy configuration")
		}
	}
	values := map[string]any{"url": strings.TrimRight(o.RouterURL, "/") + "/app/workbuddy/v1", "apiKey": PlaceholderAPIKey, "supportsToolCall": true, "useCustomProtocol": false}
	currentRaw := bytes.Clone(files[0].Before)
	for _, m := range models {
		doc, err := workBuddyModels(currentRaw)
		if err != nil {
			return nil, err
		}
		entry, index := workBuddyModel(doc, m)
		if entry == nil {
			entry = map[string]any{"id": m, "name": "API Router"}
			for k, v := range values {
				entry[k] = v
			}
			currentRaw, err = workBuddyAppendModel(currentRaw, entry)
		} else {
			for _, key := range workBuddyConnectionFields {
				if old, exists := entry[key]; exists {
					_, wantStr := values[key].(string)
					_, isStr := old.(string)
					_, isBool := old.(bool)
					if (wantStr && !isStr) || (!wantStr && !isBool) {
						return nil, fmt.Errorf("invalid WorkBuddy managed connection field")
					}
				}
			}
			currentRaw, err = workBuddyPatchModel(currentRaw, index, values, nil)
		}
		if err != nil {
			return nil, err
		}
	}
	out[0].After = currentRaw
	settings, err := readJSON(files[1].Before)
	if err != nil {
		return nil, err
	}
	if value, exists := settings["model"]; exists {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("WorkBuddy default model must be a string")
		}
	}
	out[1].After, err = edit.PatchJSON(files[1].Before, []edit.KV{{Path: "model", Value: "custom-local:" + models[0]}}, nil)
	return out, err
}

func (WorkBuddyAdapter) Restore(f FilePlan, current []byte) ([]byte, error) {
	if bytes.Equal(current, f.Before) {
		return current, nil
	} else if bytes.Equal(current, f.After) {
		if !f.Existed {
			return nil, nil
		}
		return bytes.Clone(f.Before), nil
	}
	if f.Name == "settings.json" {
		before, err1 := readJSON(f.Before)
		after, err2 := readJSON(f.After)
		now, err3 := readJSON(current)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("invalid WorkBuddy settings")
		}
		return workBuddyRestoreFields(current, before, after, now, []string{"model"})
	}
	if f.Name != "models.json" {
		return nil, fmt.Errorf("unknown WorkBuddy file")
	}
	before, err1 := workBuddyModels(f.Before)
	after, err2 := workBuddyModels(f.After)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("invalid WorkBuddy models")
	}
	if _, err := workBuddyModels(current); err != nil {
		return nil, err
	}
	for _, a := range workBuddyModelEntries(after) {
		now, err := workBuddyModels(current)
		if err != nil {
			return nil, err
		}
		id := a["id"].(string)
		b, _ := workBuddyModel(before, id)
		changed := b == nil
		for _, key := range workBuddyConnectionFields {
			bv, bok := b[key]
			av, aok := a[key]
			changed = changed || !equalValue(bv, bok, av, aok)
		}
		if !changed {
			continue
		}
		c, index := workBuddyModel(now, id)
		if c == nil {
			if b != nil {
				return nil, fmt.Errorf("WorkBuddy managed model was removed")
			}
			for _, candidate := range workBuddyModelEntries(now) {
				cid := candidate["id"].(string)
				old, _ := workBuddyModel(before, cid)
				afterModel, _ := workBuddyModel(after, cid)
				matches := old == nil && afterModel == nil
				for _, key := range workBuddyConnectionFields {
					cv, cok := candidate[key]
					av, aok := a[key]
					matches = matches && equalValue(cv, cok, av, aok)
				}
				if matches {
					return nil, fmt.Errorf("WorkBuddy managed model identity changed")
				}
			}
			continue
		}
		patched, err := workBuddyRestoreFields(workBuddyModelRaw(current, index), b, a, c, workBuddyConnectionFields)
		if err != nil {
			return nil, err
		}
		if b == nil {
			remaining, err := readJSON(patched)
			if err != nil {
				return nil, err
			}
			if equalValue(remaining["name"], remaining["name"] != nil, a["name"], a["name"] != nil) {
				if patched, err = deleteJSONField(patched, "name"); err != nil {
					return nil, err
				}
				delete(remaining, "name")
			}
			if len(remaining) == 1 {
				current, err = workBuddyRemoveModel(current, index)
			} else {
				current, err = workBuddyReplaceModel(current, index, patched)
			}
		} else {
			current, err = workBuddyReplaceModel(current, index, patched)
		}
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func workBuddyRestoreFields(raw []byte, before, after, now map[string]any, fields []string) ([]byte, error) {
	var err error
	for _, key := range fields {
		b, bok := before[key]
		a, aok := after[key]
		c, cok := now[key]
		if equalValue(b, bok, a, aok) || equalValue(b, bok, c, cok) {
			continue
		}
		if !equalValue(a, aok, c, cok) {
			if cs, ok := c.(string); key != "model" || !ok || !strings.HasPrefix(cs, "custom-local:") || strings.TrimSpace(strings.TrimPrefix(cs, "custom-local:")) == "" {
				return nil, fmt.Errorf("WorkBuddy managed field conflict: %s", key)
			}
		}
		if bok {
			raw, err = edit.PatchJSON(raw, []edit.KV{{Path: key, Value: b}}, nil)
		} else {
			raw, err = deleteJSONField(raw, key)
		}
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}
func workBuddyModels(raw []byte) (map[string]any, error) {
	if len(raw) > 0 && !json.Valid(raw) {
		return nil, fmt.Errorf("WorkBuddy models.json requires strict JSON")
	}
	doc := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		value, err := uniqueJSONValue(dec)
		if err != nil {
			return nil, fmt.Errorf("ambiguous WorkBuddy JSON")
		}
		if _, err = dec.Token(); err != io.EOF {
			return nil, fmt.Errorf("invalid WorkBuddy JSON")
		}
		if v, ok := value.(map[string]any); ok {
			doc = v
		} else if arr, ok := value.([]any); ok {
			doc["models"] = arr
		} else {
			return nil, fmt.Errorf("WorkBuddy models must be an object or array")
		}
	}
	if value, exists := doc["models"]; exists {
		models, ok := value.([]any)
		if !ok || value == nil {
			return nil, fmt.Errorf("WorkBuddy models must be an array")
		}
		ids := map[string]bool{}
		for _, item := range models {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("WorkBuddy model must be an object")
			}
			id, ok := m["id"].(string)
			if !ok || strings.TrimSpace(id) == "" || ids[id] {
				return nil, fmt.Errorf("WorkBuddy model IDs must be unique nonempty strings")
			}
			ids[id] = true
		}
	}
	return doc, nil
}

func workBuddyModelRaw(raw []byte, index int) []byte {
	v := workBuddyModelSpan(raw, index)
	return bytes.Clone(raw[v.Index : v.Index+len(v.Raw)])
}
func workBuddyModelSpan(raw []byte, index int) gjson.Result {
	path := strconv.Itoa(index)
	if !gjson.ParseBytes(raw).IsArray() {
		path = "models." + path
	}
	return gjson.GetBytes(raw, path)
}
