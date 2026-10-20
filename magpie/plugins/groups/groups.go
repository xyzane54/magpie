// Package groups is routing groups' part of the window: several models
// agents pick as one, each request sent to the right one and on to the
// next when one fails. While the user has a group, it stays on.
package groups

import (
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/provider"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "groups", API: "^1", Requires: []string{"window", "providers"},
		Name: "Routing groups", Sub: "Several models agents pick as one: each request goes to the right one, and to the next when one fails",
		One: "1 group in use", Many: "{n} groups in use",
		InUse: func() (int, error) {
			n := 0
			for _, g := range provider.Groups() {
				if !g.Auto && !g.Hidden {
					n++
				}
			}
			return n, nil
		},
		Parts: []pluginrt.Part{{Slot: "part", Key: "groups"}},
	})
}
