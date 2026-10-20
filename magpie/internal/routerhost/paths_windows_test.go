package routerhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRecoveryDirectoryRejectsCaseAliases(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Host")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{strings.ToUpper(root), filepath.Join(strings.ToLower(root), "recovery"), strings.ToUpper(base)} {
		if err := (Engine{StateDir: state}).validateState(root); err == nil {
			t.Error("case alias allowed overlapping host and recovery directories")
		}
	}
}
