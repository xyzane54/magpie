package main

import (
	"bytes"
	"context"
	"github.com/yetone/magpie/internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func TestDeliveryAndServiceHelpAndInvalidArgumentsNeverWrite(t *testing.T) {
	base := t.TempDir()
	testenv.SetHome(t, base)
	for _, name := range []string{"APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, filepath.Join(base, name))
	}
	for _, args := range [][]string{{"install", "--help"}, {"uninstall", "execute", "--help"}, {"service", "start", "--help"}, {"service", "autostart", "--help"}} {
		if err := runContext(context.Background(), args, &bytes.Buffer{}); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{{"install", "--manifest", "relative", "--package", "relative"}, {"uninstall", "force"}, {"service", "start", "--unknown"}, {"service", "autostart", "--enabled"}, {"service", "stop", "extra"}} {
		if err := runContext(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("unsafe options accepted", args)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatal("help/invalid command wrote product data", entries, err)
	}
}
