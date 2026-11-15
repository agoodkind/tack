package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/response"
)

// dbBreakGlassExtra identifies an emergency database access attempt.
type dbBreakGlassExtra struct {
	AttemptID  uuid.UUID            `json:"attempt_id"`
	Statement  string               `json:"statement"`
	Reason     string               `json:"reason"`
	MailedTo   string               `json:"mailed_to"`
	SessionID  string               `json:"session_id,omitempty"`
	OnBehalfOf *audit.ActProvenance `json:"on_behalf_of,omitempty"`
}

var dbStatementCommands = []string{
	"SELECT",
	"WITH",
	"VALUES",
	"TABLE",
	"SHOW",
	"EXPLAIN",
	"INSERT",
	"UPDATE",
	"DELETE",
	"ALTER",
	"CREATE",
	"DROP",
	"TRUNCATE",
	"SET",
	"COPY",
	"CALL",
	"GRANT",
	"REVOKE",
	"BEGIN",
	"COMMIT",
	"ROLLBACK",
}

// dbStatementOperation returns only a recognized SQL command keyword.
func dbStatementOperation(statement string) string {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "SQL"
	}

	operation := strings.ToUpper(fields[0])
	if slices.Contains(dbStatementCommands, operation) {
		return operation
	}

	return "SQL"
}

// recordDBBreakGlass records an attempt or a database access denial.
func recordDBBreakGlass(
	ctx context.Context,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	extra dbBreakGlassExtra,
	startedAt time.Time,
	outcome audit.Outcome,
	denial *audit.EventError,
) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"db.break_glass.extra_encode_failed",
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("encode the break-glass record: %w", err)
	}

	correlation := response.FromContext(ctx)
	target := externalTarget("yugabyte/database")
	parentID, _ := audit.OpIDFromContext(ctx)
	event := audit.Event{
		Verb:    string(audit.VerbOpsDBBreakGlass),
		EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type:          principal.ActorType(),
			ID:            principal.ID,
			Email:         principal.Email,
			Name:          principal.Name,
			SessionID:     principal.SessionID,
			IP:            "",
			UserAgent:     "",
			RequestID:     correlation.RequestID,
			APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type:       target.Kind,
			NodeType:   "",
			ID:         target.ID,
			Identifier: target.Reference,
			Name:       extra.Statement,
		},
		Context: audit.EventContext{
			OrgID:              audit.SystemOrgID(),
			WorkspaceID:        uuid.Nil,
			ScopeID:            uuid.Nil,
			ParentID:           uuid.Nil,
			RequestID:          correlation.RequestID,
			TraceID:            correlation.TraceID,
			Source:             audit.SourceSystem,
			Tool:               "",
			RPC:                "",
			Reason:             extra.Reason,
			AuditFormatVersion: audit.AuditFormatComplete,
			OperationClass:     audit.OperationRead,
			Phase:              audit.PhaseAttempt,
			Target:             &target,
			APITokenID:         uuid.Nil,
			CommitVersion:      "",
			CompletedAt:        time.Time{},
			Read:               nil,
			CommittedSteps:     nil,
			Selection:          nil,
			HTTPMethod:         "",
			RequestPath:        "",
			StartedAt:          startedAt,
			OperationID:        extra.AttemptID,
			ParentOperationID:  parentID,
		},
		Delta:          nil,
		Outcome:        outcome,
		Error:          denial,
		IdempotencyKey: "",
		OccurredAt:     clock.Now().UTC(),
		Extra:          encoded,
	}

	if denial != nil {
		event.Context.Phase = audit.PhaseRead
		event.Context.CompletedAt = event.OccurredAt
	}

	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbBreakGlassRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(
			ctx,
			"db.break_glass.record_failed",
			slog.String("outcome", string(outcome)),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("record the break-glass statement (%s): %w", outcome, err)
	}

	return nil
}
