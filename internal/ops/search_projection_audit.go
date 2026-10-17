package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// stageProjectionAudit stages an event for one projection correction with a
// new UUIDv7 that every transaction attempt reuses. The transaction finalizes
// the delta from the definition record it read and the record it wrote.
func stageProjectionAudit(ctx context.Context, definition *node.PropertyDef) (context.Context, error) {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		wrapped := fmt.Errorf("apply search projection for %s: operator principal is missing", definition.ID)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, node.LoggedProjectionError{Cause: wrapped}
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", err.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, fmt.Errorf("create the search projection audit event id for %s: %w", definition.ID, err)
	}
	event := audit.Event{
		Verb:    string(audit.VerbOpsBackfillSearchProjections),
		EventID: eventID,
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "property_def", NodeType: "", ID: definition.ID, Identifier: definition.Name, Name: definition.Name},
		Context: audit.EventContext{
			OrgID: definition.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceSystem, Tool: "ops.backfill.once-search-projections", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		wrapped := fmt.Errorf("encode search projection audit event for %s: %w", definition.ID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, wrapped
	}
	staged := auditintent.WithSlot(ctx, "ops.backfill.once-search-projections", principal.ID)
	if !auditintent.StageMutation(staged, payload, audit.FinalizeMutationEvent) {
		wrapped := fmt.Errorf("stage search projection audit event for %s", definition.ID)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, wrapped
	}
	return staged, nil
}
