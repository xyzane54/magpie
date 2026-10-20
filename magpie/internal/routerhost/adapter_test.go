package routerhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/routerhost"
)

// An invented format, deliberately unrelated to any shipping host. Its auth
// object is opaque: neither this adapter nor Engine replaces it with a token.
type fixtureAdapter struct {
	version int
	bad     string
}

func (a fixtureAdapter) Contract() routerhost.AdapterContract {
	name := "provider.jsonc"
	if a.bad == "path" {
		name = "../outside.json"
	}
	c := routerhost.AdapterContract{ID: "fixture-provider", Version: a.version, Files: []routerhost.FileSpec{{Name: name, Fields: []string{"endpoint"}}}}
	if a.bad == "duplicate" {
		c.Files = append(c.Files, c.Files[0])
	}
	return c
}
func (a fixtureAdapter) Build(o routerhost.Options, input []routerhost.FilePlan) ([]routerhost.FilePlan, error) {
	f := input[0]
	if a.bad == "source" {
		f.Before[0] = '!'
	}
	if a.bad == "file" {
		f.Name = "undeclared.json"
	}
	if a.bad == "secret-error" {
		return nil, fmt.Errorf("private-fixture")
	}
	if a.bad == "nil" {
		return []routerhost.FilePlan{f}, nil
	}
	var err error
	f.After, err = edit.PatchJSON(f.Before, []edit.KV{{Path: "endpoint", Value: o.RouterURL + "/custom-app"}}, nil)
	return []routerhost.FilePlan{f}, err
}
func endpoint(b []byte) (any, bool, error) {
	if len(b) == 0 {
		return nil, false, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(jsonc.ToJSON(b), &doc); err != nil {
		return nil, false, fmt.Errorf("invalid fixture")
	}
	v, ok := doc["endpoint"]
	return v, ok, nil
}
func (a fixtureAdapter) Restore(f routerhost.FilePlan, current []byte) ([]byte, error) {
	b, bok, err := endpoint(f.Before)
	if err != nil {
		return nil, err
	}
	after, _, err := endpoint(f.After)
	if err != nil {
		return nil, err
	}
	now, nok, err := endpoint(current)
	if err != nil {
		return nil, err
	}
	if bok == nok && b == now {
		return current, nil
	}
	if now != after {
		return nil, fmt.Errorf("endpoint conflict")
	}
	if bok {
		return edit.PatchJSON(current, []edit.KV{{Path: "endpoint", Value: b}}, nil)
	}
	return edit.PatchJSON(current, nil, []string{"endpoint"})
}

type fixtureChecker struct{ calls *int }

func (c fixtureChecker) Check(_ context.Context, o routerhost.Options) error {
	*c.calls++
	if o.Host != "fixture-provider" {
		return fmt.Errorf("wrong adapter identity")
	}
	// A real adapter's checker can target a distinct app and use Header forwarding.
	return nil
}

type namedFixtureAdapter struct{ fixtureAdapter }

func (a namedFixtureAdapter) Contract() routerhost.AdapterContract {
	c := a.fixtureAdapter.Contract()
	c.ID = "codex"
	return c
}

func TestPublicAdapterIdentityDoesNotSelectBuiltInAuthentication(t *testing.T) {
	base := t.TempDir()
	e := routerhost.Engine{StateDir: filepath.Join(base, "state"), Adapter: namedFixtureAdapter{fixtureAdapter{version: 1}}}
	p, err := e.Preview(routerhost.Options{Host: "codex", ConfigRoot: filepath.Join(base, "host"), RouterURL: "http://127.0.0.1:9", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p.Summary())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("placeholder")) || bytes.Contains(b, []byte("/app/codex")) {
		t.Fatal("adapter ID silently selected the built-in authentication or app path")
	}
}

func TestPublicAdapterTransactionPreservesOpaqueAuthentication(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			base := t.TempDir()
			o := routerhost.Options{Host: "fixture-provider", ConfigRoot: filepath.Join(base, "host"), RouterURL: "http://127.0.0.1:9", Model: "fixture"}
			path := filepath.Join(o.ConfigRoot, "provider.jsonc")
			original := []byte("{\n // keep comment\n \"endpoint\":\"previous\",\"auth\":{\"kind\":\"dynamic-header\",\"refresh\":[1,true,null],\"value\":\"private-fixture\"},\"unknown\":42\n}\n")
			if exists {
				if err := os.MkdirAll(o.ConfigRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			e := routerhost.Engine{StateDir: filepath.Join(base, "recovery"), Adapter: fixtureAdapter{version: 1}, Checker: fixtureChecker{&calls}}
			p, err := e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(e.StateDir); !os.IsNotExist(err) || calls != 0 {
				t.Fatal("preview wrote state or checked network")
			}
			if !exists {
				if _, err = os.Stat(o.ConfigRoot); !os.IsNotExist(err) {
					t.Fatal("preview created host directory")
				}
			}
			summary, err := json.Marshal(p.Summary())
			if err != nil {
				t.Fatal(err)
			}
			for _, hidden := range []string{"private-fixture", "placeholder", "/responses", "codex"} {
				if bytes.Contains(summary, []byte(hidden)) {
					t.Fatal("generic summary assumed a host or exposed authentication")
				}
			}
			if err = e.Apply(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			applied, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p, err = e.Preview(o)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Apply(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("checker was bypassed")
			}
			if exists {
				for _, text := range []string{"// keep comment", `"auth":{"kind":"dynamic-header","refresh":[1,true,null],"value":"private-fixture"}`, `"unknown":42`} {
					if !bytes.Contains(applied, []byte(text)) {
						t.Fatal("opaque authentication or unrelated bytes changed")
					}
				}
			}
			later, err := edit.PatchJSON(applied, []edit.KV{{Path: "later", Value: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, later, 0o600); err != nil {
				t.Fatal(err)
			}
			// A new offline Engine needs neither Router nor Checker nor auth policy.
			e = routerhost.Engine{StateDir: e.StateDir, Adapter: fixtureAdapter{version: 1}}
			for i := 0; i < 2; i++ {
				r, err := e.PreviewRestore(o.Host, o.ConfigRoot)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Restore(r); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(restored, []byte(`"later"`)) || bytes.Contains(restored, []byte("custom-app")) {
				t.Fatal("offline restore lost later fields or retained Router endpoint")
			}
			if exists && !bytes.Contains(restored, []byte("dynamic-header")) {
				t.Fatal("restore lost original credential type")
			}
		})
	}
}

func TestPublicAdapterRejectsInvalidOutputAndVersionChanges(t *testing.T) {
	for _, bad := range []string{"path", "duplicate", "source", "file", "nil", "secret-error"} {
		t.Run(bad, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "host")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "provider.jsonc")
			original := []byte("{}\n")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			e := routerhost.Engine{StateDir: filepath.Join(base, "state"), Adapter: fixtureAdapter{version: 1, bad: bad}}
			_, err := e.Preview(routerhost.Options{Host: "fixture-provider", ConfigRoot: root, RouterURL: "http://127.0.0.1:9", Model: "fixture"})
			if err == nil || strings.Contains(err.Error(), "private-fixture") {
				t.Fatal("invalid output accepted or adapter error leaked")
			}
			current, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(current, original) {
				t.Fatal("invalid preview wrote source")
			}
		})
	}
	base := t.TempDir()
	calls := 0
	e := routerhost.Engine{StateDir: filepath.Join(base, "state"), Adapter: fixtureAdapter{version: 1}, Checker: fixtureChecker{&calls}}
	o := routerhost.Options{Host: "fixture-provider", ConfigRoot: filepath.Join(base, "host"), RouterURL: "http://127.0.0.1:9", Model: "fixture"}
	p, err := e.Preview(o)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = fixtureAdapter{version: 2}
	if err = e.Apply(context.Background(), p); err == nil || calls != 0 {
		t.Fatal("adapter version changed after preview")
	}
	e.Adapter = fixtureAdapter{version: 1}
	if err = e.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	e.Adapter = fixtureAdapter{version: 2}
	if _, err = e.PreviewRestore(o.Host, o.ConfigRoot); err == nil {
		t.Fatal("wrong adapter version used an old recovery record")
	}
}
