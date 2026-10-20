package routerdelivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsHardLinkedManagedFileIsRejected(t *testing.T) {
	opts := fixture(t, nil)
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(opts.InstallDir, "api-router.exe")
	other := filepath.Join(filepath.Dir(opts.InstallDir), "linked-executable")
	if err := os.Link(exe, other); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), opts); err == nil {
		t.Fatal("repeat trusted a hard-linked executable")
	}
	if _, err := PreviewUninstall(opts.InstallDir, true); err == nil {
		t.Fatal("uninstall accepted a hard-linked managed file")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("rejected operation changed external hard link")
	}
}

func TestWindowsAliasesAndNamespacesAreRejected(t *testing.T) {
	for _, bad := range []string{`C:\programs\API-router.`, `C:\programs\API-router `, `C:\programs\.. \API-router`, `C:\programs\CON\API-router`, `C:\programs\file:stream\API-router`, `\\?\C:\programs\API-router`, `\\server\share\API-router`} {
		if _, err := installRoot(bad); err == nil {
			t.Fatalf("accepted unsafe Windows installation path: %s", bad)
		}
	}
}
