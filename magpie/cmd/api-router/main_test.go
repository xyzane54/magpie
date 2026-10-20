package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }

func TestSetupNeverImportsMagpie(t *testing.T) {
	// Seed legacy settings/subscriptions and old dial migration sources.
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	legacy := map[string]string{}
	for _, base := range []string{filepath.Join(home, ".config"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("APPDATA")} {
		for _, app := range []string{"magpie", "dial"} {
			for _, file := range []string{"settings.json", "providers.json", "logins.json", "plugin-auth.json", "plugins.yml"} {
				p := filepath.Join(base, app, file)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				legacy[p] = "legacy-subscription-marker-" + file
				if err := os.WriteFile(p, []byte(legacy[p]), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	l, err := dirs.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"version"}, {"dirs"}, {"accounts", "discovery"}} {
		_ = run(args, &out)
	}
	if _, err := os.Stat(l.Config); !os.IsNotExist(err) {
		t.Fatalf("read-only command created config: %v", err)
	}
	if err := run([]string{"config", "check"}, &out); err == nil {
		t.Fatal("missing config reported valid")
	}
	if err := run([]string{"config", "init"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"config", "check"}, &out); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(l.Config)
	if err != nil || len(files) != 1 || files[0].Name() != "config.json" {
		t.Fatalf("imported legacy files: %v %v", files, err)
	}
	for p, want := range legacy {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Fatalf("changed Magpie source %s: %q %v", p, b, err)
		}
	}
	if err := os.WriteFile(l.ConfigFile(), []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"check", "init"} {
		if err := run([]string{"config", command}, &out); err == nil {
			t.Fatalf("config %s accepted damage", command)
		}
	}
	b, _ := os.ReadFile(l.ConfigFile())
	if string(b) != "{damaged" {
		t.Fatalf("damaged config overwritten: %q", b)
	}
}

func TestLegacyCommandsCannotBeEnabled(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	// Neither old product variables nor old plugin configuration dispatch commands.
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_DEBUG", "1")
	for _, args := range [][]string{
		{"sync"}, {"providers"}, {"presets"}, {"provider"}, {"import"}, {"models"}, {"search"},
		{"groups"}, {"group"}, {"accounts", "discovery"}, {"quota"}, {"statusline"},
		{"profiles"}, {"save"}, {"use"}, {"rm"}, {"backup"}, {"restore"},
		{"webdav"}, {"s3"}, {"github"}, {"plugin"}, {"library"}, {"mcp"},
		{"claude-code"}, {"update"}, {"daemon"}, {"autostart"}, {"gui"}, {"tray"},
		{"panel"}, {"web"}, {"tui"}, {"app"}, {"serve", "--addr", "0.0.0.0:3425"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), "disabled in api-router") {
			t.Errorf("%v: %v", args, err)
		}
	}
	l, err := dirs.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.Config); !os.IsNotExist(err) {
		t.Fatalf("disabled CLI created config: %v", err)
	}
	if err := run([]string{"healthcheck"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing config reported healthy")
	}
	if _, err := os.Stat(l.Config); !os.IsNotExist(err) {
		t.Fatalf("healthcheck initialized config: %v", err)
	}
}
