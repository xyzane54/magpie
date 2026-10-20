//go:build !windows && !darwin && !linux

package routerdevice

func nativeProbes() Probes { return Probes{} }
