//go:build !windows

package routeracceptance

import "time"

func sampleMemory(pid int) memorySample {
	return memorySample{At: time.Now().UTC(), PID: pid, Error: "native memory sampling is implemented only for this Windows acceptance run"}
}
