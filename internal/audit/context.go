package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

type (
	suppressKey struct{}
	scopeKey    struct{}
)

// Scope is the audit-scope tuple a request resolves to before the audit
// row is emitted. Stored on the context as a pointer so resolvers running
// inside the inner handler can mutate the same instance the MCP wrapper
// will read after the handler returns.
type Scope struct {
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	ScopeID     uuid.UUID
	ParentID    uuid.UUID
}

// WithScopeBuilder attaches a fresh, mutable Scope to ctx. The MCP tool
// wrapper calls this on every request; resolvers populate the fields via
// SetScopeFields as they resolve them. The wrapper reads the result after
// the inner handler returns.
func WithScopeBuilder(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, &Scope{
		OrgID:       uuid.Nil,
		WorkspaceID: uuid.Nil,
		ScopeID:     uuid.Nil,
		ParentID:    uuid.Nil,
	})
}

// SetScopeFields updates the Scope on ctx in place. Zero-valued fields are
// left untouched; pass non-zero values for the fields you actually
// resolved. No-op when ctx has no Scope attached.
func SetScopeFields(ctx context.Context, s Scope) {
	cur, _ := ctx.Value(scopeKey{}).(*Scope)
	if cur == nil {
		return
	}
	if s.OrgID != uuid.Nil {
		cur.OrgID = s.OrgID
	}
	if s.WorkspaceID != uuid.Nil {
		cur.WorkspaceID = s.WorkspaceID
	}
	if s.ScopeID != uuid.Nil {
		cur.ScopeID = s.ScopeID
	}
	if s.ParentID != uuid.Nil {
		cur.ParentID = s.ParentID
	}
}

// ScopeFromContext returns a copy of the Scope attached to ctx, or the
// zero value when none is attached.
func ScopeFromContext(ctx context.Context) Scope {
	cur, _ := ctx.Value(scopeKey{}).(*Scope)
	if cur == nil {
		return Scope{
			OrgID:       uuid.Nil,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
		}
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

// Record skips writes when ctx has the suppression marker. It also assigns
// the canonical EventID and OccurredAt when unset, because the producer owns
// both. The Kafka producer keys the partition by the shard derived from
// EventID, and the chain hashes over OccurredAt, so both must be fixed before
// any backend marshals or shards the event.
func (s SuppressingRecorder) Record(ctx context.Context, ev Event) error {
	if IsSuppressed(ctx) {
		return nil
	}
	if ev.EventID == uuid.Nil {
		ev.EventID = uuid.Must(uuid.NewV7())
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = clock.Now().UTC()
	}
	err := s.Inner.Record(ctx, ev)
	if err != nil {
		slog.ErrorContext(ctx, "audit.record_failed", slog.String("err", err.Error()))
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

func canonicalizeCorrelation(ev *Event) {
	if ev.Context.RequestID == "" {
		ev.Context.RequestID = ev.Actor.RequestID
	}
}
