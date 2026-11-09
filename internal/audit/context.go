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

// Scope records the audit scope a request resolves before it emits an audit
// row.
//
// The context stores a pointer to Scope. Inner resolvers update that instance,
// and the MCP wrapper reads it after the handler returns.
type Scope struct {
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	ScopeID     uuid.UUID
	ParentID    uuid.UUID
}

// WithScopeBuilder attaches a new, mutable [Scope] to ctx.
//
// The MCP tool wrapper runs WithScopeBuilder for every request. Resolvers
// update resolved fields through [SetScopeFields], and the wrapper reads the
// scope after the inner handler returns.
//
// 	ctx = WithScopeBuilder(ctx)
func WithScopeBuilder(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, &Scope{
		OrgID:       uuid.Nil,
		WorkspaceID: uuid.Nil,
		ScopeID:     uuid.Nil,
		ParentID:    uuid.Nil,
	})
}

// SetScopeFields updates the [Scope] in ctx with nonzero fields from s.
//
// Zero fields preserve existing values. The function returns without an update
// when ctx does not contain a Scope.
//
// 	SetScopeFields(ctx, Scope{OrgID: orgID})
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

// ScopeFromContext returns a copy of the [Scope] in ctx.
//
// The function returns a zero Scope when ctx does not contain one. Updates to
// the returned copy do not change the context value.
//
// 	scope := ScopeFromContext(ctx)
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

// WithEntityBuilder attaches a new, mutable [Entity] to ctx.
//
// The MCP tool wrapper runs WithEntityBuilder before the inner handler runs.
// The handler updates the entity in place because the wrapper cannot inspect
// the handler's internal state after it returns.
//
// 	ctx = WithEntityBuilder(ctx)
func WithEntityBuilder(ctx context.Context) context.Context {
	return context.WithValue(ctx, entityKey{}, &Entity{
		Type:       "",
		NodeType:   "",
		ID:         uuid.Nil,
		Identifier: "",
		Name:       "",
	})
}

// SetAuditEntity updates the [Entity] in ctx with nonzero fields from entity.
//
// Handlers supply the fields they resolved. Zero fields preserve existing
// values, and the function returns without an update when ctx lacks an Entity.
//
// 	SetAuditEntity(ctx, Entity{ID: entityID})
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

// EntityFromContext returns a copy of the [Entity] in ctx.
//
// The function returns a zero Entity when ctx does not contain one. Updates to
// the returned copy do not change the context value.
//
// 	entity := EntityFromContext(ctx)
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

// CanonicalRecorder wraps a [Recorder] and assigns EventID and OccurredAt when
// the producer has not set them.
//
// Kafka partition selection uses a shard derived from the actor ID and EventID.
// The chain hash includes OccurredAt. The producer must fix both fields before
// a backend marshals the event or selects its shard.
type CanonicalRecorder struct{ Inner Recorder }

// Record assigns missing canonical fields before it records the event.
//
// The method supplies a UUIDv7 EventID and the current UTC OccurredAt only when
// those fields are unset. It reports an error from the wrapped recorder.
//
// 	err := recorder.Record(ctx, event)
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

// Close closes the wrapped [Recorder] when it supports closing.
//
// The method accepts both Close() error and Close() implementations. It returns
// an error from the former and succeeds when the recorder cannot close.
//
// 	err := recorder.Close()
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
