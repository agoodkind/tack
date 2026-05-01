package audit

import "context"

type suppressKey struct{}
type scopeKey struct{}

// Scope is the audit-scope tuple a request resolves to before the audit
// row is emitted. Stored on the context as a pointer so resolvers running
// inside the inner handler can mutate the same instance the MCP wrapper
// will read after the handler returns.
type Scope struct {
	OrgID       [16]byte
	WorkspaceID [16]byte
	ScopeID     [16]byte
	ParentID    [16]byte
}

// WithScopeBuilder attaches a fresh, mutable Scope to ctx. The MCP tool
// wrapper calls this on every request; resolvers populate the fields via
// SetScopeFields as they resolve them. The wrapper reads the result after
// the inner handler returns.
func WithScopeBuilder(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, &Scope{})
}

// SetScopeFields updates the Scope on ctx in place. Zero-valued fields are
// left untouched; pass non-zero values for the fields you actually
// resolved. No-op when ctx has no Scope attached.
func SetScopeFields(ctx context.Context, s Scope) {
	cur, _ := ctx.Value(scopeKey{}).(*Scope)
	if cur == nil {
		return
	}
	if s.OrgID != ([16]byte{}) {
		cur.OrgID = s.OrgID
	}
	if s.WorkspaceID != ([16]byte{}) {
		cur.WorkspaceID = s.WorkspaceID
	}
	if s.ScopeID != ([16]byte{}) {
		cur.ScopeID = s.ScopeID
	}
	if s.ParentID != ([16]byte{}) {
		cur.ParentID = s.ParentID
	}
}

// ScopeFromContext returns a copy of the Scope attached to ctx, or the
// zero value when none is attached.
func ScopeFromContext(ctx context.Context) Scope {
	cur, _ := ctx.Value(scopeKey{}).(*Scope)
	if cur == nil {
		return Scope{}
	}
	return *cur
}

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

func canonicalizeCorrelation(ev *Event) {
	if ev.Context.RequestID == "" {
		ev.Context.RequestID = ev.Actor.RequestID
	}
}
