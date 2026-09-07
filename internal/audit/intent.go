package audit

import (
	"context"
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
			TraceID: telemetry.TraceID(ctx), Source: SourceMCP, Tool: auditintent.Tool(ctx),
			RPC: "", Reason: "",
		},
		Delta: nil, Outcome: OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: nil,
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
