package service

import "context"

// probeAccountConcurrencyContextKey marks a request whose upstream account
// slot belongs to a strict health probe. The marker is deliberately carried
// in context so every account-slot acquisition path (scheduler or handler)
// can share the same accounting hook.
type probeAccountConcurrencyContextKey struct{}

// WithProbeAccountConcurrency marks ctx as a real probe request. Followers
// never acquire an account slot and therefore never need this marker.
func WithProbeAccountConcurrency(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, probeAccountConcurrencyContextKey{}, enabled)
}

// ProbeAccountConcurrencyEnabled reports whether an account slot acquired
// under ctx should also be reflected in the probe-only runtime counter.
func ProbeAccountConcurrencyEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(probeAccountConcurrencyContextKey{}).(bool)
	return enabled
}
