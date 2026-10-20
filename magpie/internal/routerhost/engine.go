package routerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type journal struct {
	Version            int
	Host, Root, Status string
	Files              []snapshot
	Checksum           string
	Adapter            *AdapterContract `json:",omitempty"`
	RetiredModels      []string         `json:",omitempty"`
	recordHash         string
}

func (j journal) checksum() string { j.Checksum = ""; b, _ := json.Marshal(j); return sum(b) }
func (e Engine) readJournal(host, root string) (*journal, string, error) {
	path := e.recordPath(host, root)
	s, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(path)})
	if err != nil {
		return nil, "", fmt.Errorf("recovery record unavailable: %w", err)
	}
	if !s.Existed {
		return nil, "", nil
	}
	var j journal
	if json.Unmarshal(s.Before, &j) != nil || (j.Version != 1 && j.Version != 2) || j.Host != host || j.Root != root || j.Checksum != j.checksum() || len(j.Files) == 0 || (j.Status != "prepared" && j.Status != "applied" && j.Status != "restoring" && j.Status != "restored") {
		return nil, "", fmt.Errorf("recovery record is corrupt; preserve it and recover from an independently verified backup")
	}
	if len(j.RetiredModels) > 1024 || len(j.RetiredModels) > 0 && j.Version != 2 || j.Version == 2 && host != "workbuddy" && host != "loomy" {
		return nil, "", fmt.Errorf("invalid retired model recovery metadata")
	}
	retired := map[string]bool{}
	for _, id := range j.RetiredModels {
		if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "\r\n\x00") || retired[id] {
			return nil, "", fmt.Errorf("invalid retired model identity")
		}
		retired[id] = true
	}
	seen := map[string]bool{}
	for _, f := range j.Files {
		if !validFileName(f.Name) || seen[strings.ToLower(f.Name)] || f.After == nil || f.Mode&^0o777 != 0 {
			return nil, "", fmt.Errorf("recovery record has invalid file metadata")
		}
		seen[strings.ToLower(f.Name)] = true
	}
	j.recordHash = sum(s.Before)
	return &j, j.recordHash, nil
}
func (e Engine) saveJournal(j *journal) error {
	path := e.recordPath(j.Host, j.Root)
	current, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(path)})
	if err != nil {
		return fmt.Errorf("cannot verify recovery record: %w", err)
	}
	hash := ""
	if current.Existed {
		hash = sum(current.Before)
	}
	if hash != j.recordHash {
		return fmt.Errorf("recovery record changed during operation; preserve it and preview again")
	}
	j.Checksum = j.checksum()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := e.writePrivate(path, b, 0o600); err != nil {
		return err
	}
	verified, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(path)})
	if err != nil || !verified.Existed || !equal(verified.Before, b) {
		return fmt.Errorf("recovery record verification failed; configuration writes blocked")
	}
	j.recordHash = sum(b)
	return nil
}
func (e Engine) Preview(o Options) (*Plan, error) {
	o = NormalizeOptions(o)
	a, contract, err := e.adapter(o.Host)
	if err != nil {
		return nil, err
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	if err := e.validateState(o.ConfigRoot); err != nil {
		return nil, err
	}
	var originals []snapshot
	var input []FilePlan
	for _, spec := range contract.Files {
		s, err := e.readSnapshot(o.ConfigRoot, FilePlan{Name: spec.Name})
		if err != nil {
			return nil, err
		}
		originals, input = append(originals, s), append(input, cloneFile(s.FilePlan))
	}
	files, err := a.Build(o, input)
	if err != nil {
		if _, builtin := a.(builtinAdapter); builtin {
			return nil, err
		}
		return nil, fmt.Errorf("adapter could not safely build configuration; repair the source configuration")
	}
	if len(files) != len(originals) {
		return nil, fmt.Errorf("adapter changed the declared file set")
	}
	j, h, err := e.readJournal(o.Host, o.ConfigRoot)
	if err != nil {
		return nil, err
	}
	if err := checkJournalContract(j, contract); err != nil {
		return nil, err
	}
	p := &Plan{Action: "apply", Host: o.Host, options: o, recordHash: h, stateDir: e.StateDir, contract: contract}
	_, p.builtin = a.(builtinAdapter)
	for i, f := range files {
		original := originals[i]
		if f.Name != original.Name || f.Existed != original.Existed || !equal(f.Before, original.Before) || f.After == nil {
			return nil, fmt.Errorf("adapter changed source snapshots or returned an invalid replacement")
		}
		s, err := e.readSnapshot(o.ConfigRoot, f)
		if err != nil {
			return nil, err
		}
		if s.Existed != original.Existed || !equal(s.Before, original.Before) || s.Mode != original.Mode {
			return nil, fmt.Errorf("host configuration changed during preview; preview again")
		}
		p.files = append(p.files, s)
	}
	if j != nil && j.Status != "restored" {
		if j.Status != "applied" {
			return nil, fmt.Errorf("an incomplete operation needs recovery; use host restore-preview and restore")
		}
		for _, f := range j.Files {
			s, err := e.readSnapshot(o.ConfigRoot, f.FilePlan)
			if err != nil {
				return nil, err
			}
			if _, err = restoreContent(a, f.FilePlan, s.Before); err != nil {
				return nil, err
			}
		}
		for _, s := range p.files {
			if !equal(s.Before, s.After) {
				return nil, fmt.Errorf("host is already managed; restore before changing the Router target or token")
			}
		}
	}
	if len(p.files) == 0 {
		return nil, fmt.Errorf("host adapter produced no configuration files")
	}
	p.Digest = planDigest(p)
	return p, nil
}
func (e Engine) checkPlan(p *Plan, action string) error {
	if p == nil || p.Action != action || p.stateDir != e.StateDir || p.Digest != planDigest(p) {
		return fmt.Errorf("invalid or modified plan; preview again")
	}
	_, contract, err := e.adapter(p.Host)
	if err != nil {
		return err
	}
	if !sameContract(p.contract, contract) {
		return fmt.Errorf("host adapter version or contract changed; preview again")
	}
	if err := validateRoot(p.options.ConfigRoot); err != nil {
		return err
	}
	_, h, err := e.readJournal(p.Host, p.options.ConfigRoot)
	if err != nil {
		return err
	}
	if h != p.recordHash {
		return fmt.Errorf("recovery record changed since preview; preview again")
	}
	for _, f := range p.files {
		s, err := e.readSnapshot(p.options.ConfigRoot, f.FilePlan)
		if err != nil {
			return err
		}
		if s.Existed != f.Existed || s.Mode != f.Mode || !equal(s.Before, f.Before) {
			return fmt.Errorf("host configuration changed since preview: %s; preview again", f.Name)
		}
	}
	return nil
}
func (e Engine) Apply(ctx context.Context, p *Plan) error {
	if err := e.checkPlan(p, "apply"); err != nil {
		return err
	}
	if e.Checker == nil {
		return fmt.Errorf("connection checker is required; no host configuration was changed")
	}
	if err := e.Checker.Check(ctx, p.options); err != nil {
		var checked safeCheckError
		if errors.As(err, &checked) {
			return fmt.Errorf("connection checks failed; no host configuration was changed: %w", checked)
		}
		return fmt.Errorf("connection checks failed; no host configuration was changed; verify Router health, app/Profile binding and model entry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := e.lock(p.options.ConfigRoot)
	if err != nil {
		return err
	}
	defer unlock()
	if err := e.checkPlan(p, "apply"); err != nil {
		return err
	}
	changed := false
	for _, f := range p.files {
		changed = changed || !equal(f.Before, f.After) || !f.Existed
	}
	if !changed {
		return nil
	}
	j := &journal{Version: 1, Host: p.Host, Root: p.options.ConfigRoot, Status: "prepared", Files: p.files, Adapter: &p.contract, recordHash: p.recordHash}
	if err := e.saveJournal(j); err != nil {
		return fmt.Errorf("cannot save recovery record; configuration untouched: %w", err)
	}
	for _, f := range p.files {
		if equal(f.Before, f.After) && f.Existed {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.failed(j, err)
		} else if err := e.replace(p.options.ConfigRoot, f); err != nil {
			return e.failed(j, err)
		}
	}
	j.Status = "applied"
	if err := e.saveJournal(j); err != nil {
		return e.failed(j, err)
	}
	return nil
}
