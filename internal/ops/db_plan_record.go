package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// dbPlanPrincipal is the identity stored on a plan row. A planned statement
// runs only for a caller with the same actor ID, actor type, and accountable
// operator ID. The actor ID of an agent is derived from its service name.
type dbPlanPrincipal struct {
	ActorID    uuid.UUID            `json:"actor_id"`
	ActorType  audit.ActorType      `json:"actor_type"`
	Name       string               `json:"name"`
	SessionID  string               `json:"session_id,omitempty"`
	OnBehalfOf *audit.ActProvenance `json:"on_behalf_of,omitempty"`
}

// dbPlanExtra is the extra payload of the plan open and close rows. The open
// row sets the statements, the plan file hash, the expiry, and the reason.
// The close row sets the postcheck and whether the plan had expired.
type dbPlanExtra struct {
	PlanID     uuid.UUID       `json:"plan_id"`
	SHA256     string          `json:"sha256,omitempty"`
	Statements []string        `json:"statements,omitempty"`
	Principal  dbPlanPrincipal `json:"principal"`
	ExpiresAt  time.Time       `json:"expires_at,omitzero"`
	Reason     string          `json:"reason,omitempty"`
	MailedTo   string          `json:"mailed_to"`
	Postcheck  string          `json:"postcheck,omitempty"`
	Expired    bool            `json:"expired,omitempty"`
}

// newDBPlanPrincipal copies the fields of principal that a plan row stores.
func newDBPlanPrincipal(principal audit.OperatorPrincipal, reason string) dbPlanPrincipal {
	return dbPlanPrincipal{
		ActorID: principal.ID, ActorType: principal.ActorType(), Name: principal.Name,
		SessionID: principal.SessionID, OnBehalfOf: onBehalfOfWithReason(principal, reason),
	}
}

// matches reports whether principal has the actor ID, actor type, and
// accountable operator ID of the principal that opened the plan. Another
// session of the same agent for the same accountable operator matches.
func (p dbPlanPrincipal) matches(principal audit.OperatorPrincipal) bool {
	if p.ActorID != principal.ID || p.ActorType != principal.ActorType() {
		return false
	}
	if p.OnBehalfOf == nil || principal.OnBehalfOf == nil {
		return p.OnBehalfOf == nil && principal.OnBehalfOf == nil
	}
	return p.OnBehalfOf.OperatorID == principal.OnBehalfOf.OperatorID
}

// recordDBPlan writes one plan row with verb and outcome to the operator
// outbox. A refused row stores refusal as its error. A failure returns a
// *dbPlanRecordError.
func recordDBPlan(
	ctx context.Context,
	outbox audit.OutboxWriter,
	verb audit.Verb,
	principal audit.OperatorPrincipal,
	extra dbPlanExtra,
	outcome audit.Outcome,
	refusal error,
) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		return &dbPlanRecordError{action: "encode the plan record", planID: extra.PlanID, err: err}
	}
	event := audit.Event{
		Verb: string(verb), EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: principal.SessionID, IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "database_plan", NodeType: "", ID: extra.PlanID, Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: extra.Reason,
		},
		Delta: nil, Outcome: outcome, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: encoded,
	}
	if refusal != nil {
		event.Error = &audit.EventError{Code: dbPlanRefusedCode, Message: refusal.Error()}
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbBreakGlassRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		return &dbPlanRecordError{action: "record " + string(verb), planID: extra.PlanID, err: err}
	}
	return nil
}

// dbPlanRecordError is a failed encode of the extra payload of a plan row or a
// failed write of the row to the operator outbox. The function that returns
// it does not log it; logDBPlanRecordFailure logs it once at the command.
type dbPlanRecordError struct {
	action string
	planID uuid.UUID
	err    error
}

// Error returns the action that failed, the plan, and the cause.
func (e *dbPlanRecordError) Error() string {
	return e.action + " for plan " + e.planID.String() + ": " + e.err.Error()
}

// Unwrap returns the cause.
func (e *dbPlanRecordError) Unwrap() error {
	return e.err
}

// logDBPlanRecordFailure logs the *dbPlanRecordError in err, if any, and
// returns err. Command functions use it on each error that can contain a
// record error; no other function logs one.
func logDBPlanRecordFailure(ctx context.Context, err error) error {
	var recordErr *dbPlanRecordError
	if errors.As(err, &recordErr) {
		slog.ErrorContext(ctx, "db.plan.record_failed",
			slog.String("plan_id", recordErr.planID.String()), slog.String("err", recordErr.Error()))
	}
	return err
}
