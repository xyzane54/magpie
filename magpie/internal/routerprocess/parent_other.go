//go:build !windows && !linux

package routerprocess

import "os/exec"

// Native parent-exit behavior on these platforms still needs acceptance.
func configureParentExit(*exec.Cmd) {}
