//go:build darwin && cgo

package routerdevice

/*
#cgo LDFLAGS: -framework Metal -framework Foundation
int router_device_metal(void);
*/
import "C"

func metalBackend() Backend {
	state := Unavailable
	if C.router_device_metal() != 0 {
		state = Available
	}
	return Backend{Kind: Metal, State: state}
}
