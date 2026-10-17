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
	auditintent.StageMutation(ctx, payload, FinalizeMutationEvent)
	return nil
}

// DescendantDeleteEvent returns the ledger event for a descendant that a
// subtree delete removed. It copies the actor, context, and extra fields from
// the staged root event in template. It sets the descendant event ID, uses
// the descendant as the entity, and sets the occurrence time to the current
// time.
func DescendantDeleteEvent(template json.RawMessage, entity Entity) (json.RawMessage, error) {
	return descendantEvent(template, VerbNodeDelete, entity, nil)
}

// DescendantMoveEvent returns a node.update ledger event for a child that a
// delete moved to its deleted parent's parent. It copies the actor, context,
// and extra fields from the staged root event in template. Its delta records
// the parent_id change in the same form as other node updates.
func DescendantMoveEvent(template json.RawMessage, entity Entity, from, to uuid.UUID) (json.RawMessage, error) {
	source, err := json.Marshal(map[string]map[string]string{recordPropertiesField: {"parent_id": from.String()}})
	if err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("encode the previous parent of moved node %s: %w", entity.ID, err)
	}
	result, err := json.Marshal(map[string]map[string]string{recordPropertiesField: {"parent_id": to.String()}})
	if err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("encode the new parent of moved node %s: %w", entity.ID, err)
	}
	delta, err := recordDelta(source, result)
	if err != nil {
		return nil, err
	}
	return descendantEvent(template, VerbNodeUpdate, entity, delta)
}

// descendantEvent derives a name-based UUID from the root event ID, verb, and
// descendant ID. Each retry of the delete step writes the same event ID. The
// consumer keeps one ledger row per event ID.
func descendantEvent(template json.RawMessage, verb Verb, entity Entity, delta *Delta) (json.RawMessage, error) {
	var event Event
	if err := json.Unmarshal(template, &event); err != nil {
		slog.Error("audit.descendant_event_failed", slog.String("err", err.Error()), slog.String("node_id", entity.ID.String()))
		return nil, fmt.Errorf("decode the staged delete event for node %s: %w", entity.ID, err)
	}
	event.Verb = string(verb)
	event.EventID = uuid.NewSHA1(event.EventID, []byte(string(verb)+":"+entity.ID.String()))
	event.Entity = entity
	event.Delta = delta
	event.OccurredAt = clock.Now().UTC()
	return MarshalEvent(event)
}
