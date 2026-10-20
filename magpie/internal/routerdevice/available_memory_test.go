package routerdevice

import (
	"context"
	"errors"
	"testing"
)

func TestAvailableMemoryPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value, err := AvailableMemory(ctx)
	if value != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation returned a usable fact: value=%d err=%v", value, err)
	}
}
