//go:build darwin && !cgo

package routerdevice

func metalBackend() Backend {
	return Backend{Kind: Metal, State: UnknownAvailability, Reason: "native Metal query requires cgo; no runtime was started"}
}
