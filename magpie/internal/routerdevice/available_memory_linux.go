package routerdevice

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func availableMemory(context.Context) (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return byteSize(value, 1024)
		}
	}
	return 0, fmt.Errorf("available physical memory unknown")
}
