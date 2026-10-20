package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func restoreContent(a Adapter, f FilePlan, current []byte) ([]byte, error) {
	if equal(current, f.Before) {
		return current, nil
	}
	if equal(current, f.After) {
		if !f.Existed {
			return nil, nil
		}
		return f.Before, nil
	}
	out, err := a.Restore(cloneFile(f), append([]byte(nil), current...))
	if err != nil {
		if _, builtin := a.(builtinAdapter); builtin {
			return nil, err
		}
		return nil, fmt.Errorf("managed field conflict or invalid configuration; reconcile using the adapter's declared fields")
	}
	if out == nil {
		return nil, fmt.Errorf("adapter cannot remove a file containing later user changes")
	}
	return out, nil
}

func (e Engine) PreviewRestore(host, root string) (*Plan, error) {
	a, contract, err := e.adapter(host)
	if err != nil {
		return nil, err
	}
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	if err := e.validateState(root); err != nil {
		return nil, err
	}
	j, h, err := e.readJournal(host, root)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("no recovery record for this host directory; locate its original --state-dir or use an independently verified backup")
	}
	if err := checkJournalContract(j, contract); err != nil {
		return nil, err
	}
	p := &Plan{Action: "restore", Host: host, options: Options{Host: host, ConfigRoot: root}, recordHash: h, stateDir: e.StateDir, contract: contract}
	_, p.builtin = a.(builtinAdapter)
	for _, f := range j.Files {
		s, err := e.readSnapshot(root, f.FilePlan)
		if err != nil {
			return nil, err
		}
		if j.Status == "restored" {
			s.After = s.Before
		} else {
			s.After, err = restoreConnectionContent(a, j, f.FilePlan, s.Before)
			if err != nil {
				return nil, fmt.Errorf("restore conflict in %s; keep the recovery record, reconcile the named fields, then preview again: %w", f.Name, err)
			}
		}
		p.files = append(p.files, s)
	}
	p.Digest = planDigest(p)
	return p, nil
}

func (e Engine) Restore(p *Plan) error {
	if err := e.checkPlan(p, "restore"); err != nil {
		return err
	}
	unlock, err := e.lock(p.options.ConfigRoot)
	if err != nil {
		return err
	}
	defer unlock()
	if err := e.checkPlan(p, "restore"); err != nil {
		return err
	}
	j, _, err := e.readJournal(p.Host, p.options.ConfigRoot)
	if err != nil {
		return err
	}
	if j.Status == "restored" {
		return nil
	}
	j.Status = "restoring"
	if err := e.saveJournal(j); err != nil {
		return err
	}
	for _, f := range p.files {
		if !(equal(f.Before, f.After) && (f.Existed || f.After == nil)) {
			if err := e.replace(p.options.ConfigRoot, f); err != nil {
				return fmt.Errorf("partial restore; record retained, preview and restore again: %w", err)
			}
		}
		for _, orig := range j.Files {
			if orig.Name == f.Name && orig.Existed && equal(f.After, orig.Before) {
				if err := os.Chmod(hostPath(j.Root, f.Name), fs.FileMode(orig.Mode)); err != nil {
					return err
				}
			}
		}
	}
	j.Status = "restored"
	return e.saveJournal(j)
}

func (e Engine) retireModels(ctx context.Context, o Options) error {
	o = NormalizeOptions(o)
	a, contract, err := e.adapter(o.Host)
	if err != nil {
		return err
	}
	j, recordHash, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil {
		return err
	}
	if err := checkJournalContract(j, contract); err != nil {
		return err
	}
	if j != nil && j.Status != "applied" {
		return errors.New("host recovery must be applied before retiring models")
	}
	var files []snapshot
	var input []FilePlan
	for _, spec := range contract.Files {
		s, err := e.readSnapshot(o.ConfigRoot, FilePlan{Name: spec.Name})
		if err != nil {
			return err
		}
		s.After = bytes.Clone(s.Before)
		files = append(files, s)
		input = append(input, cloneFile(s.FilePlan))
	}
	wanted, err := a.Build(o, input)
	if err != nil {
		return errors.New("host connection changed before retiring models")
	}
	for _, f := range wanted {
		if !equal(f.Before, f.After) {
			return errors.New("host selection or connection changed before retiring models")
		}
	}
	var after []byte
	if o.Host == "workbuddy" {
		after, err = retireWorkBuddyModels(o, input[0].Before, j)
	} else {
		after, err = retireLoomyModels(o, input[0].Before)
	}
	if err != nil || equal(after, files[0].Before) {
		return err
	}
	if j == nil {
		j = &journal{Version: 1, Host: o.Host, Root: o.ConfigRoot, Status: "applied", Files: append([]snapshot(nil), files...), Adapter: &contract}
	}
	ids, err := removedModelIDs(o.Host, files[0].Before, after)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, id := range append(j.RetiredModels, ids...) {
		known[id] = true
	}
	if len(known) > 1024 {
		return errors.New("too many retired model identities; preserve the current catalog")
	}
	j.RetiredModels = nil
	for id := range known {
		j.RetiredModels = append(j.RetiredModels, id)
	}
	sort.Strings(j.RetiredModels)
	j.Version = 2
	j.Checksum = j.checksum()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	record, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(e.recordPath(o.Host, o.ConfigRoot)), After: raw})
	if err != nil {
		return err
	}
	if record.Existed && sum(record.Before) != recordHash || !record.Existed && recordHash != "" {
		return errors.New("host recovery changed before retiring models")
	}
	files[0].After = after
	group := portGroup{Host: o.Host, Root: o.ConfigRoot, Files: files, Journal: record}
	return e.commitHostGroups(ctx, []portGroup{group}, func() error { return nil })
}

func retireWorkBuddyModels(o Options, raw []byte, j *journal) ([]byte, error) {
	doc, err := workBuddyModels(raw)
	if err != nil {
		return nil, err
	}
	keep := o.effectiveModelSet()
	entries := workBuddyModelEntries(doc)
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		id := entry["id"].(string)
		url, ok := entry["url"].(string)
		if keep[id] || strings.TrimSpace(id) == "" || strings.ContainsAny(id, "\r\n\x00") || len(entry) != 6 || entry["name"] != "API Router" || entry["apiKey"] != PlaceholderAPIKey || entry["supportsToolCall"] != true || entry["useCustomProtocol"] != false || !ok {
			continue
		}
		origin := strings.TrimSuffix(url, "/app/workbuddy/v1")
		if origin == url || !validPortOrigin(origin) {
			continue
		}
		owned := origin == strings.TrimRight(o.RouterURL, "/")
		if !owned && j != nil {
			for _, f := range j.Files {
				if f.Name != "models.json" {
					continue
				}
				for _, snapshot := range [][]byte{f.Before, f.After} {
					previous, err := workBuddyModels(snapshot)
					if err != nil {
						return nil, err
					}
					model, _ := workBuddyModel(previous, id)
					owned = owned || equalValue(entry, true, model, model != nil)
				}
			}
		}
		if !owned {
			continue
		}
		raw, err = workBuddyRemoveModel(raw, i)
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func retireLoomyModels(o Options, raw []byte) ([]byte, error) {
	doc, err := readJSON(raw)
	if err != nil {
		return nil, err
	}
	value, _ := lookup(doc, []string{"provider", loomyProvider, "models"})
	models, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Loomy Router model catalog unavailable")
	}
	keep := o.effectiveModelSet()
	var ids []string
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry, ok := models[id].(map[string]any)
		if keep[id] || !ok || !loomyManagedModelEntry(entry) || !loomyModelKey(id) {
			continue
		}
		raw, err = deleteJSONField(raw, loomyJSONPath([]string{"provider", loomyProvider, "models", id}))
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}
