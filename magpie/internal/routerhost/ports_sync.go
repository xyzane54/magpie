package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (e Engine) preparePortGroup(target PortTarget, oldOrigin, origin string) (*portGroup, error) {
	a, err := portAdapter(target.Host)
	if err != nil {
		return nil, err
	}
	if err := e.validateState(target.ConfigRoot); err != nil {
		return nil, err
	}
	contract := a.Contract()
	var files []snapshot
	for _, spec := range contract.Files {
		s, err := e.readSnapshot(target.ConfigRoot, FilePlan{Name: spec.Name})
		if err != nil {
			return nil, err
		}
		s.After = bytes.Clone(s.Before)
		if s.After == nil {
			s.After = []byte{}
		}
		files = append(files, s)
	}
	if !files[0].Existed {
		return nil, nil
	}
	after, currentOrigin, connected, err := rewritePort(target.Host, target.Model, origin, files[0].Before)
	if err != nil || !connected {
		return nil, err
	}
	if equal(files[0].Before, after) {
		return nil, nil
	}
	j, _, err := e.readJournal(target.Host, target.ConfigRoot)
	if err != nil {
		return nil, err
	}
	if err := checkJournalContract(j, contract); err != nil {
		return nil, err
	}
	if j != nil && j.Status != "applied" && j.Status != "restored" {
		return nil, errors.New("incomplete host operation blocks port synchronization")
	}
	// The former saved origin or the exact previous managed target establishes
	// ownership; a different local Router instance is not adopted by its name.
	owned := currentOrigin == oldOrigin
	if j != nil && j.Status == "applied" {
		for _, f := range j.Files {
			if f.Name == files[0].Name {
				_, previous, connected, err := rewritePort(target.Host, target.Model, origin, f.After)
				owned = owned || err == nil && connected && previous == currentOrigin
			}
		}
	}
	if !owned {
		return nil, errors.New("host endpoint belongs to another Router layout")
	}
	files[0].After = after
	if j == nil || j.Status == "restored" {
		j = &journal{Version: 1, Host: target.Host, Root: target.ConfigRoot, Status: "applied", Files: files, Adapter: &contract}
	} else {
		// Preserve the first pre-Router backup and all original restore metadata.
		for i := range j.Files {
			if j.Files[i].Name == files[0].Name {
				managedAfter, _, _, err := rewritePort(target.Host, target.Model, origin, j.Files[i].After)
				if err != nil {
					return nil, err
				}
				j.Files[i].After = managedAfter
			}
		}
	}
	j.Checksum = j.checksum()
	encoded, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return nil, err
	}
	record, err := e.readSnapshot(e.StateDir, FilePlan{Name: filepath.Base(e.recordPath(target.Host, target.ConfigRoot)), After: encoded})
	if err != nil {
		return nil, err
	}
	return &portGroup{Host: target.Host, Root: target.ConfigRoot, Files: files, Journal: record}, nil
}

func (e Engine) portLocks(groups []portGroup) (func(), error) {
	unlock, err := e.lock(groups[0].Root)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{groups[0].Root: true}
	var locks []string
	release := func() {
		for _, path := range locks {
			_ = os.Remove(path)
		}
		unlock()
	}
	for _, group := range groups[1:] {
		if held[group.Root] {
			continue
		}
		path := filepath.Join(group.Root, ".api-router-host.lock")
		if err := os.Mkdir(path, 0o700); err != nil {
			release()
			return nil, errors.New("host configuration is being changed; port synchronization blocked")
		}
		held[group.Root] = true
		locks = append(locks, path)
	}
	return release, nil
}

func (e Engine) visitPortFiles(groups []portGroup, visit func(string, snapshot) error) error {
	for _, group := range groups {
		for _, f := range group.Files {
			if err := visit(group.Root, f); err != nil {
				return err
			}
		}
		if err := visit(e.StateDir, group.Journal); err != nil {
			return err
		}
	}
	return nil
}

func (e Engine) rollbackPortFiles(groups []portGroup) error {
	var failure error
	_ = e.visitPortFiles(groups, func(root string, f snapshot) error {
		now, err := e.readSnapshot(root, f.FilePlan)
		if err == nil && equal(now.Before, f.Before) && now.Existed == f.Existed {
			return nil
		}
		if err == nil && equal(now.Before, f.After) {
			now.After = f.Before
			if !f.Existed {
				now.After = nil
			}
			err = e.replace(root, now)
			if err == nil && f.Existed {
				err = os.Chmod(hostPath(root, f.Name), os.FileMode(f.Mode))
			}
		} else if err == nil {
			err = errors.New("host changed during port rollback; pending recovery retained")
		}
		failure = errors.Join(failure, err)
		return nil
	})
	return failure
}

// SynchronizePorts runs under the caller's exclusive service lock, before HTTP
// readiness. All connected hosts are prepared first. Failures restore exact
// owned writes; the pending private transaction also permits crash recovery.
// persist saves the bound address only after both host configurations succeed.
func SynchronizePorts(ctx context.Context, stateDir, oldOrigin, origin string, targets []PortTarget, persist func() error) error {
	return (Engine{StateDir: stateDir}).synchronizePorts(ctx, oldOrigin, origin, targets, persist)
}

func (e Engine) synchronizePorts(ctx context.Context, oldOrigin, origin string, targets []PortTarget, persist func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validPortOrigin(oldOrigin) || !validPortOrigin(origin) {
		return errors.New("port synchronization requires literal loopback origins")
	}
	if len(targets) > 64 {
		return errors.New("too many port synchronization targets")
	}
	var groups []portGroup
	seen := map[string]bool{}
	for _, target := range targets {
		key := target.Host + "\x00" + filepath.Clean(target.ConfigRoot)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := validateOptions(Options{ConfigRoot: target.ConfigRoot, RouterURL: origin, Model: target.Model}); err != nil {
			return err
		}
		group, err := e.preparePortGroup(target, oldOrigin, origin)
		if err != nil {
			return fmt.Errorf("%s port synchronization: %w", target.Host, err)
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	if len(groups) == 0 {
		return persist()
	}
	return e.commitHostGroups(ctx, groups, persist)
}
