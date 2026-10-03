package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

const (
	// dbPlanRefusedCode is the error code of a refused plan row.
	dbPlanRefusedCode = "plan_refused"
	// dbPlanUnverifiedRefusal starts the refusal of a statement under a plan
	// with a row that does not decode. The decode error follows it.
	dbPlanUnverifiedRefusal = "the stored plan cannot be verified: "
)

// parseDBPlanID parses the --plan-id value. An empty value returns the nil
// UUID, which selects the one-mail-per-statement path.
func parseDBPlanID(ctx context.Context, text string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return uuid.Nil, nil
	}
	planID, err := uuid.Parse(trimmed)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.bad_plan_id", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("--plan-id %q is not a UUID: %w", trimmed, err)
	}
	return planID, nil
}

// observeDBStatement runs before a statement. Without a plan it mails the
// statement to the alarm address. With a plan it checks the plan rows.
func observeDBStatement(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	statement, reason string,
) error {
	if planID == uuid.Nil {
		return mailDBBreakGlass(ctx, deps.cfg, principal, statement, reason)
	}
	return authorizePlannedStatement(ctx, deps, principal, planID, statement, reason)
}

// mailPlannedStatementFailure mails runErr to the alarm address when a
// statement under a plan failed, and returns runErr joined with any mail
// error. It returns runErr unchanged for a statement without a plan.
func mailPlannedStatementFailure(
	ctx context.Context,
	cfg *config.Config,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	statement string,
	runErr error,
) error {
	if runErr == nil || planID == uuid.Nil {
		return runErr
	}
	failed := errors.Join(runErr, mailDBPlanStatementFailed(ctx, cfg, principal, planID, statement, runErr))
	slog.ErrorContext(ctx, "db.plan.statement_failed",
		slog.String("plan_id", planID.String()), slog.String("err", failed.Error()))
	return failed
}

// authorizePlannedStatement reads the plan rows, waiting up to
// dbPlanOpenRowWait for the open row, and returns nil when the plan with ID
// planID permits principal to run statement. A failed read of the plan rows
// returns that error through dbPlanReadFailed, which logs it once, with no
// row and no mail. A plan row that does not decode is a refusal: the stored
// plan cannot be verified. A refusal writes a refused break-glass row with the
// plan ID, then mails the refusal to the alarm address, and returns an error.
// The caller runs the statement only on nil. A failed row write or refusal
// mail is part of the returned error.
func authorizePlannedStatement(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	statement, reason string,
) error {
	state, err := awaitDBPlanOpenRow(ctx, deps, planID)
	if isDBPlanReadError(err) {
		return dbPlanReadFailed(ctx, "ops db sql", planID, err)
	}
	var refusal string
	if err != nil {
		refusal = dbPlanUnverifiedRefusal + err.Error()
	} else {
		refusal = state.refusal(principal, statement, clock.Now().UTC())
	}
	if refusal == "" {
		return nil
	}
	refused := errors.New("plan " + planID.String() + " refused the statement: " + refusal)
	slog.ErrorContext(ctx, "db.plan.statement_refused",
		slog.String("plan_id", planID.String()), slog.String("err", refused.Error()))
	extra := dbBreakGlassExtra{
		AttemptID: uuid.Must(uuid.NewV7()), Statement: statement, Reason: reason,
		MailedTo: deps.cfg.BackupAlarmEmail, CommandTag: "", RowsReturned: 0, Truncated: false,
		SessionID: principal.SessionID, OnBehalfOf: onBehalfOfWithReason(principal, reason), PlanID: planID,
	}
	recordErr := recordDBBreakGlass(ctx, deps.outbox, principal, extra, audit.OutcomeRefused, refused)
	mailErr := mailDBPlanRefusal(ctx, deps.cfg, principal, planID, statement, refusal)
	return errors.Join(refused, recordErr, mailErr)
}
