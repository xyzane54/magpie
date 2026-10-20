// fake-llama-server is a test fixture, never a real inference engine.
package main

import "github.com/yetone/magpie/internal/routerprocess/fakeserver"

func main() { fakeserver.Main() }
