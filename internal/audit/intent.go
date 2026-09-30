package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// StageStateChange builds the ledger event for a state change the caller is
// about to commit and stages it on ctx, so the storage layer writes it inside
// the same FoundationDB transaction as the change (TACK-173). The relay in the
// audit-consumer then delivers it to the ledger topic, and the MCP wrapper,
// seeing the event committed, records nothing further for the call.
//
// The event carries the same identity, scope, and tool the wrapper would have
// recorded after the fact, read from the slot and the scope builder the
// wrapper attached. Outside the wrapper there is no slot and nothing is
// staged; those callers keep their own recording.
func StageStateChange(ctx context.Context, verb Verb, entity Entity) error {
	if !auditintent.Attached(ctx) {
		return nil
	}
	// An id the entropy source cannot produce fails this one operation; a
	// panic here would take the serving process down on a product write.
	eventID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "audit.intent_id_failed",
			slog.String("verb", string(verb)), slog.String("err", err.Error()))
		return fmt.Errorf("stage audit event %s id: %w", verb, err)
	}
	// An operator acting as the user rides along in Extra and marks the
	// source; the actor stays the user (TACK-424).
	extra, err := stagedExtra(ctx)
	if err != nil {
		return fmt.Errorf("stage audit event %s: %w", verb, err)
	}
	scope := ScopeFromContext(ctx)
	event := Event{
		Verb:    string(verb),
		EventID: eventID,
		Actor: Actor{
			Type: ActorUser, ID: auditintent.Actor(ctx), Email: "", Name: "", SessionID: "",
			IP: "", UserAgent: "", RequestID: telemetry.RequestID(ctx), APITokenLabel: "",
		},
		Entity: entity,
		Context: EventContext{
			OrgID: scope.OrgID, WorkspaceID: scope.WorkspaceID, ScopeID: scope.ScopeID,
			ParentID: scope.ParentID, RequestID: telemetry.RequestID(ctx),
			TraceID: telemetry.TraceID(ctx), Source: stagedSource(ctx), Tool: auditintent.Tool(ctx),
			RPC: "", Reason: "",
		},
		Delta: nil, Outcome: OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: extra,
	}
	payload, err := MarshalEvent(event)
	if err != nil {
		slog.ErrorContext(ctx, "audit.intent_marshal_failed",
			slog.String("verb", string(verb)), slog.String("err", err.Error()))
		return fmt.Errorf("stage audit event %s: %w", verb, err)
	}
	auditintent.Stage(ctx, payload)
	return nil
}

// DescendantDeleteEvent returns the ledger event for one descendant that a
// cascading delete removed. The event copies the actor, context, and extra
// fields of the staged root event in template. It sets a new event ID, the
// descendant as its entity, and the current time.
func DescendantDeleteEvent(template json.RawMessage, entity Entity) (json.RawMessage, error) {
	return descendantEvent(template, VerbNodeDelete, entity, nil)
}

// DescendantMoveEvent returns the node.update ledger event for one child
// that a delete moved from its deleted parent to that parent's parent. The
// event copies the actor, context, and extra fields of the staged root event
// in template, and its delta records the parent_id change.
func DescendantMoveEvent(template json.RawMessage, entity Entity, from, to uuid.UUID) (json.RawMessage, error) {
	before, err := json.Marshal(map[string]string{"parent_id": from.String()})
	if err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("encode the previous parent of moved node %s: %w", entity.ID, err)
	}
	after, err := json.Marshal(map[string]string{"parent_id": to.String()})
	if err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("encode the new parent of moved node %s: %w", entity.ID, err)
	}
	return descendantEvent(template, VerbNodeUpdate, entity, &Delta{Before: before, After: after, Changed: []string{"parent_id"}})
}

func descendantEvent(template json.RawMessage, verb Verb, entity Entity, delta *Delta) (json.RawMessage, error) {
	var event Event
	if err := json.Unmarshal(template, &event); err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("decode the staged delete event for node %s: %w", entity.ID, err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("create the %s event id for node %s: %w", verb, entity.ID, err)
	}
	event.Verb = string(verb)
	event.EventID = eventID
	event.Entity = entity
	event.Delta = delta
	event.OccurredAt = clock.Now().UTC()
	return MarshalEvent(event)
}
