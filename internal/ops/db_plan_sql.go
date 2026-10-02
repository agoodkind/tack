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

// dbPlanRefusedCode is the error code of a refused plan row.
const dbPlanRefusedCode = "plan_refused"

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

// authorizePlannedStatement returns nil when plan planID permits principal to
// run statement. In every other case it writes a refused break-glass row with
// the plan ID, then mails the refusal to the alarm address, and returns an
// error; the caller does not run the statement. A failed row write or refusal
// mail is part of the returned error.
func authorizePlannedStatement(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	statement, reason string,
) error {
	refusal := dbPlanRefusal(ctx, deps.cfg.DatabaseURL, principal, planID, statement)
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

// dbPlanRefusal reads the plan rows and returns the refusal reason, or an
// empty string when the plan permits the statement. A read failure is a
// refusal.
func dbPlanRefusal(ctx context.Context, dsn string, principal audit.OperatorPrincipal, planID uuid.UUID, statement string) string {
	state, err := readDBPlanState(ctx, dsn, planID)
	if err != nil {
		return "the plan rows could not be read: " + err.Error()
	}
	return state.refusal(principal, statement, clock.Now().UTC())
}
