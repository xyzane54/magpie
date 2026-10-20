package routerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
)

func connectedPortFixtures(t *testing.T) (Engine, []PortTarget, []Engine) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "recovery")
	var targets []PortTarget
	var engines []Engine
	for _, host := range []string{"workbuddy", "loomy"} {
		a, _ := portAdapter(host)
		e := Engine{StateDir: state, Adapter: a, Checker: checkerFunc(func(context.Context, Options) error { return nil })}
		root := filepath.Join(base, host)
		if host == "workbuddy" {
			put(t, filepath.Join(root, "models.json"), []byte(`[{"id":"other-model","url":"https://example.test/v1","apiKey":"unrelated-fixture"}]`))
			put(t, filepath.Join(root, "settings.json"), []byte("{\n // retain settings\n \"model\": \"previous-model\", \"theme\": \"dark\"\n}"))
		} else {
			put(t, filepath.Join(root, "opencode.json"), []byte("{\n // retain configuration\n \"provider\":{\"other\":{\"name\":\"Keep me\"}},\"model\":\"previous-model\"\n}"))
		}
		mustApply(t, e, Options{Host: host, ConfigRoot: root, RouterURL: "http://127.0.0.1:13145", Model: "fixture-model"})
		targets = append(targets, PortTarget{Host: host, ConfigRoot: root, Model: "fixture-model"})
		engines = append(engines, e)
	}
	return Engine{StateDir: state}, targets, engines
}

func portFixtureBytes(t *testing.T, e Engine, targets []PortTarget) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, target := range targets {
		a, _ := portAdapter(target.Host)
		for _, spec := range a.Contract().Files {
			path := filepath.Join(target.ConfigRoot, spec.Name)
			out[path] = get(t, path)
		}
		out[e.recordPath(target.Host, target.ConfigRoot)] = get(t, e.recordPath(target.Host, target.ConfigRoot))
	}
	return out
}

func assertPortFixtureBytes(t *testing.T, expected map[string][]byte) {
	t.Helper()
	for path, before := range expected {
		if !bytes.Equal(get(t, path), before) {
			t.Fatalf("port synchronization changed %s after a failure", filepath.Base(path))
		}
	}
}

func TestPortSynchronizationUpdatesBothHostsAndPreservesFirstBackup(t *testing.T) {
	e, targets, engines := connectedPortFixtures(t)
	originals := map[string][][]byte{}
	for _, target := range targets {
		j, _, err := e.readJournal(target.Host, target.ConfigRoot)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range j.Files {
			originals[target.Host] = append(originals[target.Host], bytes.Clone(f.Before))
		}
	}
	wbPath := filepath.Join(targets[0].ConfigRoot, "models.json")
	wb := get(t, wbPath)
	doc, _ := workBuddyModels(wb)
	_, index := workBuddyModel(doc, "fixture-model")
	entry, err := edit.PatchJSON(workBuddyModelRaw(wb, index), []edit.KV{{Path: "later", Value: "keep"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wb, err = workBuddyReplaceModel(wb, index, entry)
	if err != nil {
		t.Fatal(err)
	}
	put(t, wbPath, wb)
	loomyPath := filepath.Join(targets[1].ConfigRoot, "opencode.json")
	loomy, err := edit.PatchJSON(get(t, loomyPath), []edit.KV{{Path: "later", Value: "keep"}, {Path: "provider.api_router.options.timeout", Value: 1200000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, loomyPath, loomy)
	for _, origins := range [][2]string{{"http://127.0.0.1:13145", "http://127.0.0.1:13146"}, {"http://127.0.0.1:13146", "http://127.0.0.1:13147"}} {
		committed := false
		if err := e.synchronizePorts(context.Background(), origins[0], origins[1], targets, func() error { committed = true; return nil }); err != nil || !committed {
			t.Fatalf("port synchronization failed: %v", err)
		}
		for _, target := range targets {
			name := "models.json"
			if target.Host == "loomy" {
				name = "opencode.json"
			}
			if !bytes.Contains(get(t, filepath.Join(target.ConfigRoot, name)), []byte(origins[1]+"/app/"+target.Host+"/v1")) {
				t.Fatalf("%s did not follow the new port", target.Host)
			}
			j, _, err := e.readJournal(target.Host, target.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			for i, f := range j.Files {
				if !equal(f.Before, originals[target.Host][i]) {
					t.Fatal("port synchronization replaced the first pre-Router backup")
				}
			}
			roots, err := ConnectedRoots(e.StateDir, target.Host)
			if err != nil || len(roots) != 1 || roots[0] != target.ConfigRoot {
				t.Fatalf("custom connected directory lost: %v, %v", roots, err)
			}
		}
	}
	beforeRepeat := portFixtureBytes(t, e, targets)
	if err := e.synchronizePorts(context.Background(), "http://127.0.0.1:13147", "http://127.0.0.1:13147", targets, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertPortFixtureBytes(t, beforeRepeat)
	for i, target := range targets {
		mustRestore(t, engines[i], Options{Host: target.Host, ConfigRoot: target.ConfigRoot})
	}
	if !bytes.Contains(get(t, wbPath), []byte(`"later"`)) || !bytes.Contains(get(t, loomyPath), []byte(`"later"`)) ||
		!bytes.Contains(get(t, loomyPath), []byte(`1200000`)) || !bytes.Contains(get(t, filepath.Join(targets[0].ConfigRoot, "settings.json")), []byte("retain settings")) {
		t.Fatalf("offline restore after port changes discarded later host settings: workbuddy=%s loomy=%s settings=%s", get(t, wbPath), get(t, loomyPath), get(t, filepath.Join(targets[0].ConfigRoot, "settings.json")))
	}
}

func TestPortSynchronizationRollsBackBothHostsAndJournals(t *testing.T) {
	for _, failAt := range []string{"second-host", "persist-address"} {
		t.Run(failAt, func(t *testing.T) {
			e, targets, _ := connectedPortFixtures(t)
			before := portFixtureBytes(t, e, targets)
			injected := false
			e.write = func(path string, raw []byte, mode uint32) error {
				if failAt == "second-host" && path == filepath.Join(targets[1].ConfigRoot, "opencode.json") && !injected {
					injected = true
					return errors.New("fixture disk full")
				}
				return steady.WriteFile(path, raw, os.FileMode(mode))
			}
			persisted := false
			err := e.synchronizePorts(context.Background(), "http://127.0.0.1:13145", "http://127.0.0.1:13146", targets, func() error {
				persisted = true
				return errors.New("fixture config write failed")
			})
			if err == nil || failAt == "second-host" && persisted || failAt == "persist-address" && !persisted {
				t.Fatalf("failure was hidden or address committed early: %v", err)
			}
			assertPortFixtureBytes(t, before)
			if _, err := os.Stat(filepath.Join(e.StateDir, portTransactionFile)); !os.IsNotExist(err) {
				t.Fatalf("successful rollback leaked a pending transaction: %v", err)
			}
		})
	}
}

func TestPortSynchronizationPreflightsConflictsAndLeavesUnrelatedProviders(t *testing.T) {
	e, targets, _ := connectedPortFixtures(t)
	path := filepath.Join(targets[1].ConfigRoot, "opencode.json")
	bad, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: "provider.api_router.options.useSessionAuth", Value: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, bad)
	before := portFixtureBytes(t, e, targets)
	if err := e.synchronizePorts(context.Background(), "http://127.0.0.1:13145", "http://127.0.0.1:13146", targets, func() error { t.Fatal("configuration persisted despite a conflict"); return nil }); err == nil {
		t.Fatal("conflicting Loomy authentication was overwritten")
	}
	assertPortFixtureBytes(t, before)
	raw := []byte(`[{"id":"fixture-model","url":"https://example.test/v1","apiKey":"user-key"}]`)
	_, _, connected, err := rewritePort("workbuddy", "fixture-model", "http://127.0.0.1:13146", raw)
	if err != nil || connected {
		t.Fatal("an unrelated provider was adopted")
	}
}

func TestInterruptedPortSynchronizationCanRecoverOwnedWrites(t *testing.T) {
	for _, laterEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "preserve-later-edit"}[laterEdit], func(t *testing.T) {
			e, targets, _ := connectedPortFixtures(t)
			before := portFixtureBytes(t, e, targets)
			var groups []portGroup
			for _, target := range targets {
				group, err := e.preparePortGroup(target, "http://127.0.0.1:13145", "http://127.0.0.1:13146")
				if err != nil {
					t.Fatal(err)
				}
				groups = append(groups, *group)
			}
			pending := portTransaction{Version: 1, Groups: groups, LockToken: strings.Repeat("a", 32)}
			pending.Checksum = pending.checksum()
			raw, _ := json.Marshal(pending)
			put(t, filepath.Join(e.StateDir, portTransactionFile), raw)
			for _, path := range e.portLockPaths(groups) {
				put(t, filepath.Join(path, "port-sync-owner"), []byte(pending.LockToken))
			}
			if err := e.replace(groups[0].Root, groups[0].Files[0]); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(groups[0].Root, groups[0].Files[0].Name)
			var edited []byte
			if laterEdit {
				edited = append(get(t, path), '\n')
				put(t, path, edited)
			}
			err := RecoverPortSync(context.Background(), e.StateDir)
			if laterEdit {
				if err == nil || !strings.Contains(err.Error(), "host changed") || !equal(get(t, path), edited) {
					t.Fatalf("later user change was overwritten: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertPortFixtureBytes(t, before)
			}
		})
	}
}

func TestCommittedPortSynchronizationRecoveryPreservesNewEndpointAndLaterEdits(t *testing.T) {
	e, targets, _ := connectedPortFixtures(t)
	var groups []portGroup
	for _, target := range targets {
		group, err := e.preparePortGroup(target, "http://127.0.0.1:13145", "http://127.0.0.1:13146")
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, *group)
	}
	if err := e.visitPortFiles(groups, e.replace); err != nil {
		t.Fatal(err)
	}
	pending := portTransaction{Version: 1, Groups: groups, LockToken: strings.Repeat("b", 32), Committed: true}
	pending.Checksum = pending.checksum()
	raw, _ := json.Marshal(pending)
	put(t, filepath.Join(e.StateDir, portTransactionFile), raw)
	for _, path := range e.portLockPaths(groups) {
		put(t, filepath.Join(path, "port-sync-owner"), []byte(pending.LockToken))
	}
	path := filepath.Join(groups[0].Root, groups[0].Files[0].Name)
	edited := append(get(t, path), '\n')
	put(t, path, edited)
	if err := RecoverPortSync(context.Background(), e.StateDir); err != nil {
		t.Fatal(err)
	}
	if !equal(get(t, path), edited) || !bytes.Contains(edited, []byte("http://127.0.0.1:13146/")) {
		t.Fatal("committed recovery rolled back a working endpoint or a later edit")
	}
	for _, path := range e.portLockPaths(groups) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("committed recovery retained its operation lock", err)
		}
	}
}

func TestPortSynchronizationRecoveryPreservesUnknownOperationLocks(t *testing.T) {
	e, targets, _ := connectedPortFixtures(t)
	group, err := e.preparePortGroup(targets[0], "http://127.0.0.1:13145", "http://127.0.0.1:13146")
	if err != nil {
		t.Fatal(err)
	}
	pending := portTransaction{Version: 1, Groups: []portGroup{*group}, LockToken: strings.Repeat("c", 32)}
	pending.Checksum = pending.checksum()
	raw, _ := json.Marshal(pending)
	put(t, filepath.Join(e.StateDir, portTransactionFile), raw)
	unknown := filepath.Join(e.StateDir, ".operation.lock", "another-writer")
	put(t, unknown, []byte("keep"))
	before := portFixtureBytes(t, e, targets)
	if err := RecoverPortSync(context.Background(), e.StateDir); err == nil {
		t.Fatal("an unknown operation lock was reclaimed")
	}
	if !equal(get(t, unknown), []byte("keep")) {
		t.Fatal("unknown operation lock contents changed")
	}
	assertPortFixtureBytes(t, before)
}
