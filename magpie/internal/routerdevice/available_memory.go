package routerdevice

import "context"

// AvailableMemory reports current physical memory available to applications.
// It is an observation, never proof that a particular context can be loaded.
func AvailableMemory(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	value, err := availableMemory(ctx)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return value, nil
}
