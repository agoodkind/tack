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

// Scope records resolved request scope. Resolvers update the context's shared
// pointer before the MCP wrapper reads it after the handler returns.
type Scope struct {
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	ScopeID     uuid.UUID
	ParentID    uuid.UUID
}

// WithScopeBuilder stores a fresh mutable Scope in ctx for resolvers to fill
// with SetScopeFields and the wrapper to read after the handler.
func WithScopeBuilder(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		scopeKey{},
		&Scope{
			OrgID:       uuid.Nil,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
		},
	)
}

// SetScopeFields applies nonzero fields to the shared Scope. It preserves
// existing values for zero inputs and does not change ctx without a Scope.
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

// ScopeFromContext copies the stored Scope or returns its zero value when absent.
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

// WithEntityBuilder stores a fresh mutable Entity for the handler to update
// before the wrapper reads the resolved target.
func WithEntityBuilder(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		entityKey{},
		&Entity{
			Type:       "",
			NodeType:   "",
			ID:         uuid.Nil,
			Identifier: "",
			Name:       "",
		},
	)
}

// SetAuditEntity applies nonzero fields to the shared Entity. It preserves
// existing values for zero inputs and does not change ctx without an Entity.
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

// EntityFromContext copies the stored Entity or returns its zero value when absent.
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

// CanonicalRecorder assigns missing EventID and OccurredAt before a backend
// serializes or partitions the event. Partitioning depends on EventID and the
// chain hash includes OccurredAt; both values must remain stable on redelivery.
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
		slog.ErrorContext(
			ctx,
			"audit.record_failed",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", err.Error()),
		)

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
