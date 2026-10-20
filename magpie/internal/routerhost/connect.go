package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func (e Engine) lock(root string) (func(), error) {
	if err := e.validateState(root); err != nil {
		return nil, err
	}
	for _, dir := range []string{e.StateDir, root} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("cannot create operation directory: %w", err)
		}
	}
	if err := validateRoot(e.StateDir); err != nil {
		return nil, err
	}
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	if err := os.Chmod(e.StateDir, 0o700); err != nil {
		return nil, err
	}
	var held []string
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = os.Remove(held[i])
		}
	}
	for _, path := range []string{filepath.Join(e.StateDir, ".operation.lock"), filepath.Join(root, ".api-router-host.lock")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			release()
			return nil, fmt.Errorf("operation locked or directory not writable: %s; check for a running writer before removing a stale lock", path)
		}
		held = append(held, path)
	}
	return release, nil
}

// Connect applies a saved Profile to WorkBuddy or Loomy, including a model
// change on this same Router, and retires obsolete generated model entries.
func (e Engine) Connect(ctx context.Context, o Options) (*Plan, error) {
	o = NormalizeOptions(o)
	p, err := e.connect(ctx, o)
	if err != nil {
		return nil, err
	}
	if err := e.retireModels(ctx, o); err != nil {
		return nil, err
	}
	return p, nil
}

func (e Engine) connect(ctx context.Context, o Options) (*Plan, error) {
	if o.Host != "workbuddy" && o.Host != "loomy" {
		return nil, errors.New("automatic connection supports WorkBuddy and Loomy only")
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	if err := e.validateState(o.ConfigRoot); err != nil {
		return nil, err
	}
	if pending, err := e.readSnapshot(e.StateDir, FilePlan{Name: portTransactionFile}); err != nil || pending.Existed {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("an interrupted host transaction needs recovery before another connection change")
	}
	if p, err := e.Preview(o); err == nil {
		return p, e.Apply(ctx, p)
	}
	group, contract, err := e.prepareConnection(o)
	if err != nil {
		return nil, err
	}
	if e.Checker == nil {
		return nil, errors.New("connection checker is required")
	}
	if err := e.Checker.Check(ctx, o); err != nil {
		return nil, errors.New("connection checks failed; host configuration unchanged")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.commitHostGroups(ctx, []portGroup{*group}, func() error { return nil }); err != nil {
		return nil, err
	}
	p := &Plan{Action: "apply", Host: o.Host, options: o, files: group.Files, stateDir: e.StateDir, contract: contract}
	p.Digest = planDigest(p)
	return p, nil
}

func (e Engine) prepareConnection(o Options) (*portGroup, AdapterContract, error) {
	o = NormalizeOptions(o)
	a, contract, err := e.adapter(o.Host)
	if err != nil {
		return nil, contract, err
	}
	if err := validateOptions(o); err != nil {
		return nil, contract, err
	}
	if err := e.validateState(o.ConfigRoot); err != nil {
		return nil, contract, err
	}
	var files []snapshot
	var input []FilePlan
	for _, spec := range contract.Files {
		s, err := e.readSnapshot(o.ConfigRoot, FilePlan{Name: spec.Name})
		if err != nil {
			return nil, contract, err
		}
		files = append(files, s)
		input = append(input, cloneFile(s.FilePlan))
	}
	if err := checkConnectedFiles(a, o, input); err != nil {
		return nil, contract, err
	}
	j, recordHash, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil {
		return nil, contract, err
	}
	if err := checkJournalContract(j, contract); err != nil {
		return nil, contract, err
	}
	if j != nil && j.Status != "applied" && j.Status != "restored" {
		return nil, contract, errors.New("an incomplete host operation needs recovery")
	}
	var baseline []FilePlan
	if j != nil && j.Status == "applied" {
		for i, f := range j.Files {
			if i >= len(input) || f.Name != input[i].Name {
				return nil, contract, errors.New("host recovery file set changed")
			}
			before, err := restoreConnectionContent(a, j, f.FilePlan, input[i].Before)
			if err != nil {
				return nil, contract, err
			}
			input[i].Before, input[i].Existed = before, before != nil
			baseline = append(baseline, cloneFile(f.FilePlan))
		}
		if len(baseline) != len(input) {
			return nil, contract, errors.New("host recovery file set changed")
		}
	} else {
		for _, f := range input {
			baseline = append(baseline, cloneFile(f))
		}
		j = &journal{Version: 1, Host: o.Host, Root: o.ConfigRoot, Adapter: &contract}
	}
	replacements, err := buildConnection(a, o, input)
	if err != nil {
		return nil, contract, err
	}
	managed, err := buildConnection(a, o, baseline)
	if err != nil {
		return nil, contract, err
	}
	for i := range files {
		files[i].After = replacements[i].After
	}
	if len(j.Files) == 0 || j.Status == "restored" {
		j.Files = make([]snapshot, len(files))
		copy(j.Files, files)
	}
	for i := range j.Files {
		j.Files[i].After = managed[i].After
	}
	j.Status = "applied"
	keep := o.effectiveModelSet()
	var retired []string
	for _, id := range j.RetiredModels {
		if !keep[id] {
			retired = append(retired, id)
		}
	}
	j.RetiredModels = retired
	j.Checksum = j.checksum()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return nil, contract, err
	}
	record, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(e.recordPath(o.Host, o.ConfigRoot)), After: raw})
	if err != nil {
		return nil, contract, err
	}
	if (record.Existed && sum(record.Before) != recordHash) || (!record.Existed && recordHash != "") {
		return nil, contract, errors.New("host recovery record changed during connection planning")
	}
	return &portGroup{Host: o.Host, Root: o.ConfigRoot, Files: files, Journal: record}, contract, nil
}

func checkConnectedFiles(a Adapter, o Options, input []FilePlan) error {
	i, prefix := 0, loomyProvider+"/"
	if o.Host == "workbuddy" {
		i, prefix = 1, "custom-local:"
	}
	if i >= len(input) {
		return errors.New("host connection files unavailable")
	}
	doc, err := readJSON(input[i].Before)
	if err != nil {
		return err
	}
	selection, ok := doc["model"].(string)
	if !ok || !strings.HasPrefix(selection, prefix) {
		return errors.New("host is not selecting its Router connection")
	}
	o.Model = strings.TrimPrefix(selection, prefix)
	o.Models = nil
	checkInput := input
	if o.Host == "loomy" {
		if small, ok := doc["small_model"].(string); ok && loomyRouterSelection(small) && small != selection {
			if patched, err := edit.PatchJSON(input[i].Before, []edit.KV{{Path: "small_model", Value: selection}}, nil); err == nil {
				checkInput = append([]FilePlan(nil), input...)
				checkInput[i] = cloneFile(input[i])
				checkInput[i].Before = patched
			}
		}
	}
	got, err := a.Build(o, checkInput)
	if err != nil {
		return errors.New("host Router connection fields conflict")
	}
	for _, f := range got {
		if !bytes.Equal(f.Before, f.After) {
			return errors.New("host Router connection belongs to another target")
		}
	}
	return nil
}

func buildConnection(a Adapter, o Options, input []FilePlan) ([]FilePlan, error) {
	got, err := a.Build(o, input)
	if err == nil || o.Host != "loomy" || len(input) != 1 {
		return got, err
	}
	if err := checkConnectedFiles(a, o, input); err != nil {
		return nil, err
	}
	doc, err := readJSON(input[0].Before)
	if err != nil {
		return nil, err
	}
	var patches []edit.KV
	for _, m := range o.EffectiveModels() {
		if !loomyModelKey(m) {
			return nil, errors.New("invalid Loomy model key")
		}
		want := map[string]any{"name": m, "tool_call": true}
		if existing, exists := lookup(doc, []string{"provider", loomyProvider, "models", m}); exists {
			if !loomyManagedModelEntry(existing) {
				return nil, errors.New("Loomy target model conflicts")
			}
			continue
		}
		patches = append(patches, edit.KV{Path: loomyJSONPath([]string{"provider", loomyProvider, "models", m}), Value: want})
	}
	raw, err := edit.PatchJSON(input[0].Before, patches, nil)
	if err != nil {
		return nil, err
	}
	f := cloneFile(input[0])
	f.Before = raw
	got, err = a.Build(o, []FilePlan{f})
	if err == nil {
		got[0].Before = bytes.Clone(input[0].Before)
	}
	return got, err
}
