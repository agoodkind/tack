package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// dbBreakGlassExtra is the payload the ledger rows store beyond the
// choke-point's pair. It records the statement and the reason. The attempt id
// pairs the intent row with its outcome row. SessionID and OnBehalfOf record
// the agent session and the accountable operator when a service principal
// runs the statement.
type dbBreakGlassExtra struct {
	AttemptID    uuid.UUID            `json:"attempt_id"`
	Statement    string               `json:"statement"`
	Reason       string               `json:"reason"`
	MailedTo     string               `json:"mailed_to"`
	CommandTag   string               `json:"command_tag,omitempty"`
	RowsReturned int                  `json:"rows_returned"`
	Truncated    bool                 `json:"truncated,omitempty"`
	SessionID    string               `json:"session_id,omitempty"`
	OnBehalfOf   *audit.ActProvenance `json:"on_behalf_of,omitempty"`
	// PlanID is the open plan that permitted the statement, or the nil UUID
	// for a statement mailed on its own.
	PlanID uuid.UUID `json:"plan_id,omitzero"`
}

// recordDBBreakGlass writes one detail row. The pending row is written before
// the statement and the ok or error row after, paired by attempt id. A process
// lost mid-statement leaves a pending row with the attempted statement. Both
// rows store the statement and the reason. A statement that the plan refuses
// produces one refused row and no pending row.
func recordDBBreakGlass(
	ctx context.Context,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	extra dbBreakGlassExtra,
	outcome audit.Outcome,
	runErr error,
) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.extra_encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode the break-glass record: %w", err)
	}
	event := audit.Event{
		Verb: string(audit.VerbOpsDBBreakGlass), EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: principal.SessionID, IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "database", NodeType: "", ID: extra.AttemptID, Identifier: "", Name: extra.CommandTag},
		Context: audit.EventContext{
			OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: extra.Reason,
		},
		Delta: nil, Outcome: outcome, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: encoded,
	}
	if runErr != nil {
		code := "statement_failed"
		if outcome == audit.OutcomeRefused {
			code = dbPlanRefusedCode
		}
		event.Error = &audit.EventError{Code: code, Message: runErr.Error()}
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbBreakGlassRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.record_failed",
			slog.String("outcome", string(outcome)), slog.String("err", err.Error()))
		return fmt.Errorf("record the break-glass statement (%s): %w", outcome, err)
	}
	return nil
}
