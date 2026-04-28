package audit

import "context"

type suppressKey struct{}

// WithSuppressed marks ctx so any Record call routed through SuppressIfNeeded
// becomes a no-op. Used for seed and other internal automation that should
// not emit audit events.
func WithSuppressed(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressKey{}, true)
}

// IsSuppressed reports whether ctx was produced by WithSuppressed.
func IsSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressKey{}).(bool)
	return v
}

// SuppressingRecorder wraps a Recorder so any Record call on a suppressed
// context returns nil without writing. Wrap the production Recorder with
// this once at startup; callers (seed, internal jobs) only need to set the
// suppression marker on their context.
type SuppressingRecorder struct{ Inner Recorder }

func (s SuppressingRecorder) Record(ctx context.Context, ev Event) error {
	if IsSuppressed(ctx) {
		return nil
	}
	return s.Inner.Record(ctx, ev)
}
