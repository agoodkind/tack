package ops

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// refuseUnverifiableDBPlanClose refuses the close of planID when a plan row
// does not decode: the stored plan cannot be verified. It writes a refused
// close row, then mails the refusal to the alarm address, and returns the
// refusal error with any row or mail failure. It writes no close row with
// outcome ok, and the plan stays open.
func refuseUnverifiableDBPlanClose(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	postcheck string,
	decodeErr error,
) error {
	reason := dbPlanUnverifiedRefusal + decodeErr.Error()
	refused := errors.New("plan " + planID.String() + " refused the close: " + reason)
	slog.ErrorContext(ctx, "db.plan.close_refused",
		slog.String("plan_id", planID.String()), slog.String("err", refused.Error()))
	extra := dbPlanExtra{
		PlanID: planID, SHA256: "", Statements: nil, Principal: newDBPlanPrincipal(principal, ""),
		ExpiresAt: time.Time{}, Reason: "", MailedTo: deps.cfg.BackupAlarmEmail, Postcheck: postcheck, Expired: false,
	}
	recordErr := recordDBPlan(ctx, deps.outbox, audit.VerbOpsDBPlanClose, principal, extra, audit.OutcomeRefused, refused)
	mailErr := mailDBPlanCloseRefused(ctx, deps.cfg, principal, planID, reason)
	return errors.Join(refused, recordErr, mailErr)
}
