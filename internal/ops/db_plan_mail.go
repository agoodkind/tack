package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// dbPlanCaller is the command label in every plan mail.
const dbPlanCaller = "tack ops db plan"

// mailDBPlanOpened sends the one mail of plan open.
func mailDBPlanOpened(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, extra dbPlanExtra) error {
	return sendDBPlanMail(ctx, cfg, principal, "plan "+extra.PlanID.String()+" opened", extra.PlanID,
		"Expires: "+extra.ExpiresAt.UTC().Format(backupAlarmTimeLayout),
		"Reason: "+extra.Reason,
		"Plan file SHA-256: "+extra.SHA256,
		"Statements:\n"+strings.Join(extra.Statements, "\n"))
}

// mailDBPlanRefusal sends the immediate mail for a statement the plan refused.
func mailDBPlanRefusal(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, planID uuid.UUID, statement, refusal string) error {
	return sendDBPlanMail(ctx, cfg, principal, "statement refused under plan "+planID.String(), planID,
		"Refusal: "+refusal,
		"Statement:\n"+statement)
}

// mailDBPlanCloseRefused sends the immediate mail for a close the plan
// refused.
func mailDBPlanCloseRefused(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, planID uuid.UUID, refusal string) error {
	return sendDBPlanMail(ctx, cfg, principal, "close refused under plan "+planID.String(), planID,
		"Refusal: "+refusal,
		"Status: the plan is still open")
}

// mailDBPlanSummaryIncomplete sends the immediate mail for a close that
// stopped before the summary because the audit consumer had not recorded
// every plan row.
func mailDBPlanSummaryIncomplete(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, planID uuid.UUID, cause error) error {
	return sendDBPlanMail(ctx, cfg, principal, "summary of plan "+planID.String()+" is incomplete", planID,
		"Status: the summary is incomplete, no close row was written, and the plan is still open",
		"Cause: "+cause.Error())
}

// mailDBPlanStatementFailed sends the immediate mail for a planned statement
// that returned an error.
func mailDBPlanStatementFailed(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, planID uuid.UUID, statement string, runErr error) error {
	return sendDBPlanMail(ctx, cfg, principal, "statement failed under plan "+planID.String(), planID,
		"Error: "+runErr.Error(),
		"Statement:\n"+statement)
}

// mailDBPlanSummary sends the close mail: the plan, each run or refused
// statement and its outcome from the ledger rows, and the postcheck text.
func mailDBPlanSummary(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, state dbPlanState, postcheck string, expired bool) error {
	event, status := "plan "+state.open.PlanID.String()+" closed", "Status: closed before expiry"
	if expired {
		event = "plan " + state.open.PlanID.String() + " closed after it expired"
		status = "Status: expired at " + state.open.ExpiresAt.UTC().Format(backupAlarmTimeLayout) + ", closed after expiry"
	}
	lines := []string{
		status,
		"Opened by: " + state.open.Principal.Name,
		"Reason: " + state.open.Reason,
		"Plan file SHA-256: " + state.open.SHA256,
	}
	refused := 0
	for _, attempt := range state.attempts {
		if attempt.Outcome == audit.OutcomeRefused {
			refused++
		}
	}
	lines = append(lines,
		"Statements run: "+strconv.Itoa(len(state.attempts)-refused),
		"Statements refused: "+strconv.Itoa(refused))
	for index, attempt := range state.attempts {
		line := strconv.Itoa(index+1) + ". " + string(attempt.Outcome) + ": " + attempt.Statement
		if attempt.Error != "" {
			line += "\n   Error: " + attempt.Error
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Postcheck:\n"+postcheck)
	return sendDBPlanMail(ctx, cfg, principal, event, state.open.PlanID, lines...)
}

// sendDBPlanMail sends one plan mail to the alarm address. The subject is
// "Break-glass <event> on <host> by <email>". The body starts with the agent
// or operator line of dbBreakGlassMailIdentity, then the accountable
// operator, the host, the time, the plan ID, and lines.
func sendDBPlanMail(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, event string, planID uuid.UUID, lines ...string) error {
	host := backupAlarmHost()
	identityLine, subjectEmail := dbBreakGlassMailIdentity(principal)
	accountable := principal.Email
	if principal.OnBehalfOf != nil {
		accountable = principal.OnBehalfOf.OperatorEmail
	}
	header := []string{
		identityLine,
		"Accountable operator: " + accountable,
		"Host: " + host,
		"Time: " + clock.Now().UTC().Format(backupAlarmTimeLayout),
		"Plan: " + planID.String(),
	}
	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: "Break-glass " + event + " on " + host + " by " + subjectEmail,
		Body:    strings.Join(append(header, lines...), "\n") + "\n",
		From:    "", Name: "", Caller: dbPlanCaller,
	}
	if err := backupAlarmSendFunc(ctx, cfg, message); err != nil {
		slog.ErrorContext(ctx, "db.plan.mail_undelivered", slog.String("plan_id", planID.String()),
			slog.String("to", cfg.BackupAlarmEmail), slog.String("err", err.Error()))
		return fmt.Errorf("the mail for break-glass %s to %s was not delivered: %w", event, cfg.BackupAlarmEmail, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "db.plan.mail_sent",
		slog.String("plan_id", planID.String()), slog.String("to", cfg.BackupAlarmEmail))
	return nil
}
