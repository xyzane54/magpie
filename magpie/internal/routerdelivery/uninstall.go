package routerdelivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type UninstallPlan struct {
	InstallDir        string   `json:"installDir"`
	Remove            []string `json:"remove"`
	Retain            []string `json:"retain"`
	UserDataPreserved bool     `json:"userDataPreserved"`
	RecoveryTool      string   `json:"recoveryTool,omitempty"`
}
type UninstallResult struct {
	UninstallPlan
	Removed []string `json:"removed"`
}

// PreviewUninstall is read-only. Service/host/autostart ownership is coordinated
// by the CLI before Uninstall; this layer only removes receipt-managed files.
func PreviewUninstall(root string, keepRecovery bool) (UninstallPlan, error) {
	root, err := installRoot(root)
	if err != nil {
		return UninstallPlan{}, err
	}
	r, err := readReceipt(root)
	if err != nil {
		return UninstallPlan{}, fmt.Errorf("uninstall requires a verified installation receipt: %w", err)
	}
	p := UninstallPlan{InstallDir: root, UserDataPreserved: true}
	for _, f := range r.Manifest.Files {
		name := filepath.Join(root, f.Name)
		if err := removable(name); err != nil {
			return UninstallPlan{}, err
		}
		if keepRecovery && f.Name == "api-router.exe" {
			if err := verifyFile(context.Background(), name, f); err != nil {
				return UninstallPlan{}, fmt.Errorf("offline recovery executable is damaged; restore its verified package before uninstalling: %w", err)
			}
			p.Retain = append(p.Retain, name)
			p.RecoveryTool = name
		} else {
			p.Remove = append(p.Remove, name)
		}
	}
	if keepRecovery {
		p.Retain = append(p.Retain, filepath.Join(root, ReceiptName))
	} else {
		p.Remove = append(p.Remove, filepath.Join(root, ReceiptName))
	}
	return p, nil
}

func removable(name string) error {
	if err := checkPath(name); err != nil {
		return err
	}
	f, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() {
		return errors.New("managed uninstall target is not a regular file")
	}
	return checkFile(f, i)
}

// Uninstall re-derives the preview and never recursively removes a directory.
// Retaining the executable keeps offline host restore possible after shutdown.
func Uninstall(ctx context.Context, root string, keepRecovery bool) (UninstallResult, error) {
	p, err := PreviewUninstall(root, keepRecovery)
	if err != nil {
		return UninstallResult{}, err
	}
	r := UninstallResult{UninstallPlan: p}
	lock := p.InstallDir + ".install-lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		return r, fmt.Errorf("installation is busy: %w", err)
	}
	defer os.Remove(lock)
	for _, name := range p.Remove {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if err := removable(name); err != nil {
			return r, err
		}
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return r, err
		}
		r.Removed = append(r.Removed, name)
	}
	if !keepRecovery {
		// Nonempty directories (including user-created files) are retained.
		if entries, err := os.ReadDir(p.InstallDir); err == nil && len(entries) == 0 {
			return r, os.Remove(p.InstallDir)
		}
	}
	return r, nil
}
