package routerruntime

import "context"

type callObserverKey struct{}

// WithLocalCallObserver carries a trusted per-call consumer through the existing
// context. It grants no model capability and creates no model invocation.
func WithLocalCallObserver(ctx context.Context, observer LocalCallObserver) context.Context {
	return context.WithValue(ctx, callObserverKey{}, observer)
}

// LocalCallObserverFromContext is the integration seam for the Manager producer.
// It lives with the shared contract to avoid an import cycle with its consumer.
func LocalCallObserverFromContext(ctx context.Context) LocalCallObserver {
	observer, _ := ctx.Value(callObserverKey{}).(LocalCallObserver)
	return observer
}
