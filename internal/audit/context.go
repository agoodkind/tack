package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

type scopeKey struct{}

type entityKey struct{}

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

// WithEntityBuilder attaches a fresh, mutable Entity to ctx. The MCP tool
// wrapper calls this before the inner handler runs. The handler fills the
// entity in place because the wrapper cannot inspect the handler internals
// after it returns.
func WithEntityBuilder(ctx context.Context) context.Context {
	return context.WithValue(ctx, entityKey{}, &Entity{
		Type:       "",
		NodeType:   "",
		ID:         uuid.Nil,
		Identifier: "",
		Name:       "",
	})
}

// SetAuditEntity updates the Entity on ctx in place. Zero-valued fields are
// left untouched; pass the fields the handler resolved. No-op when ctx has no
// Entity attached.
func SetAuditEntity(ctx context.Context, entity Entity) {
	current, _ := ctx.Value(entityKey{}).(*Entity)
	if current == nil {
		return
	}
	if entity.Type != "" {
		current.Type = entity.Type
	}
	if entity.NodeType != "" {
		current.NodeType = entity.NodeType
	}
	if entity.ID != uuid.Nil {
		current.ID = entity.ID
	}
	if entity.Identifier != "" {
		current.Identifier = entity.Identifier
	}
	if entity.Name != "" {
		current.Name = entity.Name
	}
}

// EntityFromContext returns a copy of the Entity attached to ctx, or the zero
// value when none is attached.
func EntityFromContext(ctx context.Context) Entity {
	current, _ := ctx.Value(entityKey{}).(*Entity)
	if current == nil {
		return Entity{
			Type:       "",
			NodeType:   "",
			ID:         uuid.Nil,
			Identifier: "",
			Name:       "",
		}
	}
	return *current
}

// CanonicalRecorder wraps a Recorder and assigns the canonical EventID and
// OccurredAt when they are unset, because the producer owns
// both. The Kafka producer keys the partition by the shard derived from
// EventID, and the chain hashes over OccurredAt, so both must be fixed before
// any backend marshals or shards the event.
type CanonicalRecorder struct{ Inner Recorder }

// Record assigns canonical event fields before recording the event.
func (s CanonicalRecorder) Record(ctx context.Context, ev Event) error {
	if ev.EventID == uuid.Nil {
		ev.EventID = uuid.Must(uuid.NewV7())
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = clock.Now().UTC()
	}
	err := s.Inner.Record(ctx, ev)
	if err != nil {
		slog.ErrorContext(ctx, "audit.record_failed",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("record audit event %s: %w", ev.Verb, err)
	}
	return nil
}

// Close closes the wrapped Recorder when it supports closing.
func (s CanonicalRecorder) Close() error {
	switch recorder := s.Inner.(type) {
	case interface{ Close() error }:
		err := recorder.Close()
		if err != nil {
			slog.Error("audit.recorder_close_failed", slog.String("err", err.Error()))
			return fmt.Errorf("close audit recorder: %w", err)
		}
		return nil
	case interface{ Close() }:
		recorder.Close()
	}
	return nil
}

func canonicalizeCorrelation(ev *Event) {
	if ev.Context.RequestID == "" {
		ev.Context.RequestID = ev.Actor.RequestID
	}
}
