package routerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

func nextAppRevision(d Document, previous int) (int, error) {
	var revision int
	if b := d["appRevision"]; b != nil {
		if json.Unmarshal(b, &revision) != nil || revision < 0 || revision >= 1<<52 {
			return 0, errors.New("invalid application revision counter")
		}
	}
	if revision < previous {
		revision = previous
	}
	revision++
	d["appRevision"], _ = json.Marshal(revision)
	return revision, nil
}

func readApps(d Document) (map[string]AppProfile, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(d["apps"], &raw) != nil || raw == nil {
		return nil, errors.New("invalid application configuration")
	}
	var providers Document
	if json.Unmarshal(d["providers"], &providers) != nil || providers == nil {
		return nil, errors.New("invalid provider configuration")
	}
	out := map[string]AppProfile{}
	for id, name := range builtinApps {
		out[id] = defaultApp(id, name)
	}
	for id, b := range raw {
		var p AppProfile
		var shape Document
		if json.Unmarshal(b, &shape) != nil || shape == nil {
			return nil, errors.New("invalid application Profile")
		}
		for _, key := range []string{"schemaVersion", "id", "revision", "name", "cloud", "credentialMode", "localRuntime", "redactionEnabled"} {
			if v := shape[key]; v == nil || string(v) == "null" {
				return nil, errors.New("missing Profile field")
			}
		}
		var cloud Document
		if json.Unmarshal(shape["cloud"], &cloud) != nil || cloud == nil || cloud["providerId"] == nil || cloud["model"] == nil || string(cloud["model"]) == "null" || string(cloud["providerId"]) == "null" {
			return nil, errors.New("invalid application target")
		}
		if price := shape["referencePrice"]; price != nil && string(price) != "null" {
			var fields Document
			if json.Unmarshal(price, &fields) != nil {
				return nil, errors.New("invalid reference price")
			}
			for _, key := range []string{"currency", "unit", "input", "output", "version", "source"} {
				if fields[key] == nil || string(fields[key]) == "null" {
					return nil, errors.New("missing reference price field")
				}
			}
		}
		if json.Unmarshal(b, &p) != nil || p.ID != id {
			return nil, errors.New("invalid application Profile")
		}
		normModels, err := normalizeAppModels(p.Models)
		if err != nil {
			return nil, errors.New("invalid application Profile")
		}
		p.Models = normModels
		if len(p.Models) == 0 && p.Cloud.ProviderID != "" && p.Cloud.Model != "" {
			p.Models = []string{p.Cloud.ProviderID + "/" + p.Cloud.Model}
		} else if len(p.Models) > 0 && p.Cloud.ProviderID == "" && p.Cloud.Model == "" {
			if provID, modelID, kind, ok := parseAppModelTarget(p.Models[0]); ok && kind == "cloud" {
				p.Cloud.ProviderID = provID
				p.Cloud.Model = modelID
			}
		}
		if validateApp(p) != nil {
			return nil, errors.New("invalid application Profile")
		}
		p.Builtin = builtinApps[id] != ""
		p.Path = "/app/" + id + "/v1"
		p.Configured = p.Cloud.ProviderID != "" || len(p.Models) > 0
		if p.Cloud.ProviderID != "" && providers[p.Cloud.ProviderID] == nil {
			return nil, errors.New("application references a missing provider")
		}
		for _, m := range p.Models {
			if provID, _, kind, ok := parseAppModelTarget(m); ok && kind == "cloud" {
				if providers[provID] == nil {
					return nil, errors.New("application references a missing provider")
				}
			}
		}
		out[id] = p
	}
	return out, nil
}

func (s *Store) Apps() ([]AppProfile, error) {
	return s.AppsContext(context.Background())
}

func (s *Store) AppsContext(ctx context.Context) ([]AppProfile, error) {
	d, err := s.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := readApps(d)
	if err != nil {
		return nil, err
	}
	list := make([]AppProfile, 0, len(apps))
	for _, p := range apps {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, ctx.Err()
}

// mergeObject retains unknown members at each edited object level.
func mergeObject(old json.RawMessage, value any) json.RawMessage {
	fields := Document{}
	_ = json.Unmarshal(old, &fields)
	if fields == nil {
		fields = Document{}
	}
	b, _ := json.Marshal(value)
	var updates Document
	_ = json.Unmarshal(b, &updates)
	for k, v := range updates {
		fields[k] = v
	}
	b, _ = json.Marshal(fields)
	return b
}
