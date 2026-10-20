package routerhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
)

func TestConnectSwitchesModelsAndPreservesOriginalRecovery(t *testing.T) {
	_, targets, engines := connectedPortFixtures(t)
	for i, target := range targets {
		t.Run(target.Host, func(t *testing.T) {
			e := engines[i]
			original, _, err := e.readJournal(target.Host, target.ConfigRoot)
			if err != nil {
				t.Fatal(err)
			}
			path := hostPath(target.ConfigRoot, "settings.json")
			if target.Host == "loomy" {
				path = hostPath(target.ConfigRoot, "opencode.json")
			}
			later, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: "later", Value: "retain user edit"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			put(t, path, later)
			for _, model := range []string{"spark-x2.5", "model.v2", "fixture-model"} {
				o := Options{Host: target.Host, ConfigRoot: target.ConfigRoot, RouterURL: "http://127.0.0.1:13145", Model: model}
				if _, err := e.Connect(context.Background(), o); err != nil {
					t.Fatalf("switch to %s: %v", model, err)
				}
				p, err := e.Preview(o)
				if err != nil {
					t.Fatal("new model is not an idempotent connection:", err)
				}
				for _, f := range p.files {
					if !bytes.Equal(f.Before, f.After) {
						t.Fatal("host did not select the new model")
					}
				}
				current, _, err := e.readJournal(target.Host, target.ConfigRoot)
				if err != nil {
					t.Fatal(err)
				}
				for i, f := range current.Files {
					if !bytes.Equal(f.Before, original.Files[i].Before) || f.Existed != original.Files[i].Existed || f.Mode != original.Files[i].Mode {
						t.Fatal("model switch replaced the first backup")
					}
					if bytes.Contains(f.After, []byte("retain user edit")) {
						t.Fatal("model switch absorbed later user settings into the managed snapshot")
					}
				}
			}
			mustRestore(t, e, Options{Host: target.Host, ConfigRoot: target.ConfigRoot})
			if !bytes.Contains(get(t, path), []byte("retain user edit")) {
				t.Fatal("offline restore lost later user settings")
			}
		})
	}
}

func TestConnectSwitchesLegacyLoomyAndRestoresItsActualBaseline(t *testing.T) {
	e, o := loomySetup(t)
	mustApply(t, e, o)
	if err := os.Remove(e.recordPath(o.Host, o.ConfigRoot)); err != nil {
		t.Fatal(err)
	}
	path := hostPath(o.ConfigRoot, "opencode.json")
	before := get(t, path)
	o.Model = "spark-x2.5"
	if _, err := e.Connect(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(get(t, path), []byte("api_router/spark-x2.5")) {
		t.Fatal("legacy connection still selects the old model")
	}
	mustRestore(t, e, o)
	if !bytes.Equal(get(t, path), before) {
		t.Fatal("legacy recovery fabricated a pre-Router configuration")
	}
}

func TestConnectRefusesConflictsAndFailedChecksWithoutWrites(t *testing.T) {
	for _, failure := range []string{"checker", "other-origin", "managed-field", "journal", "pending-transaction"} {
		t.Run(failure, func(t *testing.T) {
			e, o := loomySetup(t)
			mustApply(t, e, o)
			path := hostPath(o.ConfigRoot, "opencode.json")
			switch failure {
			case "checker":
				e.Checker = checkerFunc(func(context.Context, Options) error { return errors.New("fixture denied") })
			case "other-origin":
				o.RouterURL = "http://127.0.0.1:14388"
			case "managed-field":
				raw, err := edit.PatchJSON(get(t, path), []edit.KV{{Path: "provider.api_router.options.useSessionAuth", Value: true}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				put(t, path, raw)
			case "journal":
				put(t, e.recordPath(o.Host, o.ConfigRoot), []byte("invalid journal"))
			case "pending-transaction":
				put(t, hostPath(e.StateDir, portTransactionFile), []byte("pending fixture"))
			}
			before := get(t, path)
			record := get(t, e.recordPath(o.Host, o.ConfigRoot))
			o.Model = "spark-x2.5"
			if _, err := e.Connect(context.Background(), o); err == nil {
				t.Fatal("connection ignored", failure)
			}
			if !bytes.Equal(get(t, path), before) || !bytes.Equal(get(t, e.recordPath(o.Host, o.ConfigRoot)), record) {
				t.Fatal("failed connection changed host or recovery bytes")
			}
		})
	}
}

func TestConnectRollsBackHostAndJournalOnWriteFailure(t *testing.T) {
	for _, failFile := range []string{"models.json", "settings.json", "journal"} {
		t.Run(failFile, func(t *testing.T) {
			e, o := workBuddySetup(t)
			mustApply(t, e, o)
			paths := []string{hostPath(o.ConfigRoot, "models.json"), hostPath(o.ConfigRoot, "settings.json"), e.recordPath(o.Host, o.ConfigRoot)}
			before := make([][]byte, len(paths))
			for i, path := range paths {
				before[i] = get(t, path)
			}
			failed := false
			e.write = func(path string, raw []byte, mode uint32) error {
				match := filepath.Base(path) == failFile || failFile == "journal" && path == paths[2]
				if match && !failed {
					failed = true
					return errors.New("fixture write failed")
				}
				return steady.WriteFile(path, raw, os.FileMode(mode))
			}
			o.Model = "spark-x2.5"
			if _, err := e.Connect(context.Background(), o); err == nil || !failed {
				t.Fatal("did not exercise write rollback", err)
			}
			for i, path := range paths {
				if !bytes.Equal(get(t, path), before[i]) {
					t.Fatal("rollback changed", filepath.Base(path))
				}
			}
			if _, err := os.Stat(hostPath(e.StateDir, portTransactionFile)); !os.IsNotExist(err) {
				t.Fatal("successful rollback retained a pending transaction")
			}
		})
	}
}

func TestConnectRejectsHostEditDuringConnectionCheck(t *testing.T) {
	e, o := loomySetup(t)
	mustApply(t, e, o)
	path := hostPath(o.ConfigRoot, "opencode.json")
	var later []byte
	e.Checker = checkerFunc(func(context.Context, Options) error {
		later = append(get(t, path), []byte("\n// changed during check\n")...)
		put(t, path, later)
		return nil
	})
	o.Model = "spark-x2.5"
	if _, err := e.Connect(context.Background(), o); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("stale connection was not rejected", err)
	}
	if !bytes.Equal(get(t, path), later) {
		t.Fatal("stale connection overwrote an external edit")
	}
}
