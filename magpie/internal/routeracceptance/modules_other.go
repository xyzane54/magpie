//go:build !windows

package routeracceptance

import (
	"context"
	"github.com/yetone/magpie/internal/routerprocess"
)

func nativeLoaderState() (any, error) { return map[string]string{"state": "not_required"}, nil }
func loadedDependencyModules(context.Context, int, routerprocess.Spec) (any, error) {
	return map[string]string{"state": "not_required"}, nil
}
