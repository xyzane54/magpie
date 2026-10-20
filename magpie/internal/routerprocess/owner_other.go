//go:build !windows

package routerprocess

import (
	"os/exec"

	"github.com/yetone/magpie/internal/proc"
)

type treeOwner struct{ tree *proc.Tree }

func startOwned(cmd *exec.Cmd) (ownedProcess, error) {
	configureParentExit(cmd)
	tree, err := proc.StartTree(cmd)
	if err != nil {
		return nil, err
	}
	return treeOwner{tree}, nil
}

func (o treeOwner) wait() error { return o.tree.Wait() }
func (o treeOwner) kill()       { o.tree.Kill() }
