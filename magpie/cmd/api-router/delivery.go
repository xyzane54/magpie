package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routercontrol"
	"github.com/yetone/magpie/internal/routerdelivery"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const deliveryHelp = `Usage:
 api-router install --manifest ABSOLUTE_JSON --package ABSOLUTE_ZIP [--install-dir ABSOLUTE_DIR]
 api-router uninstall <preview|execute> [--install-dir ABSOLUTE_DIR]
Install accepts a locally supplied fixed Windows amd64 package. No latest lookup, download,
config initialization, service startup, autostart or model installation is performed.
Default installation: %LOCALAPPDATA%/Programs/API-router. User data is stored separately.
After installation: api-router.exe service start; service autostart enable|disable|status.
Uninstall preview writes nothing. execute stops only this product instance, removes its
login autostart and receipt-managed support files. Config, credentials, models, statistics,
runtime caches and host recovery backups are preserved. The EXE and receipt are retained
as an offline recovery tool. Stop does not restore hosts. Before uninstall, use
host restore-preview/restore --host HOST --config-root ORIGINAL_ABSOLUTE_HOST_DIR
--state-dir ORIGINAL_STATE_DIR and a fresh --plan-digest for restore. Managed-field conflicts
block host restoration; resolve them explicitly. Do not delete the last recovery tool/log.
No automatic upgrade or rollback. See README-windows.md for manual backup/replacement.
`

func runDelivery(ctx context.Context, command string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return printHelp(out, deliveryHelp)
	}
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "help" {
			return printHelp(out, deliveryHelp)
		}
	}
	action := ""
	if command == "uninstall" {
		action = args[0]
		args = args[1:]
		if action != "preview" && action != "execute" {
			return fmt.Errorf("use uninstall preview|execute")
		}
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var manifest, archive, root string
	f.StringVar(&root, "install-dir", "", "dedicated product directory")
	if command == "install" {
		f.StringVar(&manifest, "manifest", "", "fixed local manifest")
		f.StringVar(&archive, "package", "", "fixed local package")
	}
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return fmt.Errorf("invalid %s options; use %s --help", command, command)
	}
	if command == "install" && (!filepath.IsAbs(manifest) || !filepath.IsAbs(archive)) {
		return fmt.Errorf("--manifest and --package require explicit absolute local paths")
	}
	l, err := dirs.Resolve()
	if err != nil {
		return err
	}
	if root == "" {
		root = filepath.Join(filepath.Dir(l.Data), "Programs", "API-router")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("--install-dir must be absolute")
	}
	if command == "install" {
		result, err := routerdelivery.Install(ctx, routerdelivery.InstallOptions{ManifestPath: manifest, PackagePath: archive, InstallDir: root, ProtectedDirs: []string{l.Config, l.Data, l.State, l.Models, l.Runtime}})
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			routerdelivery.InstallResult
			AutostartNotice string `json:"autostartNotice"`
		}{result, "Installation does not change login autostart. Choose service autostart enable|disable|status explicitly. No models are enabled, downloaded or loaded."})
	}
	p, err := routerdelivery.PreviewUninstall(root, true)
	if err != nil {
		return err
	}
	recovery := map[string]string{"tool": p.RecoveryTool, "stateDir": filepath.Join(l.State, "host-config"), "restorePreview": "host restore-preview --host HOST --config-root ORIGINAL_ABSOLUTE_DIR --state-dir ORIGINAL_STATE_DIR", "restore": "host restore --host HOST --config-root ORIGINAL_ABSOLUTE_DIR --state-dir ORIGINAL_STATE_DIR --plan-digest FRESH_PREVIEW_DIGEST", "notice": "Restore before removing the last recovery tool. Conflicts block restore. Uninstall preserves hosts and all user data; connected hosts need explicit restoration."}
	if action == "preview" {
		return json.NewEncoder(out).Encode(map[string]any{"plan": p, "hostRecovery": recovery, "stopsService": true, "removesAutostart": true})
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(actual), filepath.Join(filepath.Clean(root), "api-router.exe")) {
		return fmt.Errorf("execute uninstall using the installed EXE in %s; no service or files changed", root)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err = routercontrol.Stop(ctx, l); err != nil {
		return err
	}
	if _, err = routercontrol.SetAutostart(l, actual, false); err != nil {
		return err
	}
	result, err := routerdelivery.Uninstall(ctx, root, true)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"result": result, "hostRecovery": recovery, "retainedRecoveryTool": actual, "userDataPreserved": true})
}
