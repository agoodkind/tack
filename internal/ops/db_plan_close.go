package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

type dbPlanCloseInput struct {
	clispec.InputMarker
	PlanID    string
	Postcheck string
	// Wait is the Go duration that close waits for the audit consumer to
	// record the plan rows. An empty value waits dbPlanProjectionWait.
	Wait string `exhaustruct:"optional"`
}

// dbPlanCloseResult reports the closed plan and each statement run or
// refused under it.
type dbPlanCloseResult struct {
	clispec.ResultMarker
	Command    string          `json:"command"`
	DryRun     bool            `json:"dry_run"`
	PlanID     string          `json:"plan_id"`
	Postcheck  string          `json:"postcheck"`
	Expired    bool            `json:"expired"`
	Statements []dbPlanAttempt `json:"statements,omitempty"`
	MailedTo   string          `json:"mailed_to"`
}

// runDBPlanClose closes plan input.PlanID. With execute it reads the audit
// topic high-water marks and waits until the audit consumer has committed
// past them. It then refuses a closer that is not the opener principal, mails
// the summary, and writes the close row. A wait past the bound, a refusal, or
// a summary mail failure returns an error before the close row exists, and
// the plan stays open.
func runDBPlanClose(ctx context.Context, deps dbSQLDeps, input dbPlanCloseInput, sink clispec.ResultSink, execute bool) error {
	planID, err := parseDBPlanID(ctx, input.PlanID)
	if err != nil {
		return err
	}
	if planID == uuid.Nil {
		return errors.New("a plan ID is required")
	}
	postcheck := strings.TrimSpace(input.Postcheck)
	if postcheck == "" {
		return errors.New("a postcheck is required; it is the closing check the summary mail reports")
	}
	if deps.cfg.BackupAlarmEmail == "" {
		return errors.New("TACK_BACKUP_ALARM_EMAIL is empty; a break-glass plan summary must be mailed")
	}
	if err := requireDBPlanProjectionConfig(deps.cfg); err != nil {
		return err
	}
	wait, err := parseDBPlanWait(ctx, input.Wait)
	if err != nil {
		return err
	}
	result := dbPlanCloseResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.db.plan.close", DryRun: !execute,
		PlanID: planID.String(), Postcheck: postcheck, Expired: false, Statements: nil,
		MailedTo: deps.cfg.BackupAlarmEmail,
	}
	if !execute {
		return writeDBPlanResult(ctx, sink, result)
	}
	principal, err := deps.identity.Resolve(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.principal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("resolve the operator closing plan %s: %w", planID, err)
	}
	state, err := closeDBPlanRows(ctx, deps, principal, planID, postcheck, wait)
	if err != nil {
		return err
	}
	result.Expired, result.Statements = state.expired(clock.Now().UTC()), state.attempts
	if err := mailDBPlanSummary(ctx, deps.cfg, principal, state, postcheck, result.Expired); err != nil {
		return err
	}
	extra := newDBPlanCloseExtra(deps, principal, state, postcheck)
	extra.Expired = result.Expired
	if err := recordDBPlan(ctx, deps.outbox, audit.VerbOpsDBPlanClose, principal, extra, audit.OutcomeOK, nil); err != nil {
		return err
	}
	return writeDBPlanResult(ctx, sink, result)
}

// closeDBPlanRows returns the plan state that the summary reports. It reads
// the plan rows, waits for the audit consumer, reads the plan rows again,
// and merges both reads. The first read contains a row that the relay sends
// from the operator outbox to the topic after the high-water mark read. The
// second read contains a row that was on the topic below the mark. It then
// checks the open row, the close row, and the closer principal.
func closeDBPlanRows(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	postcheck string,
	wait time.Duration,
) (dbPlanState, error) {
	empty := dbPlanState{open: nil, closed: false, attempts: nil}
	before, err := readDBPlanRows(ctx, deps.cfg.DatabaseURL, planID)
	if err != nil {
		return empty, err
	}
	if err := awaitDBPlanProjection(ctx, deps.cfg, planID, wait); err != nil {
		incomplete := errors.Join(err, mailDBPlanSummaryIncomplete(ctx, deps.cfg, principal, planID, err))
		slog.ErrorContext(ctx, "db.plan.summary_incomplete",
			slog.String("plan_id", planID.String()), slog.String("err", incomplete.Error()))
		return empty, incomplete
	}
	after, err := readDBPlanRows(ctx, deps.cfg.DatabaseURL, planID)
	if err != nil {
		return empty, err
	}
	state, err := newDBPlanState(ctx, mergeDBPlanRows(before, after))
	if err != nil {
		return state, err
	}
	if state.open == nil {
		return state, errors.New("plan " + planID.String() + " has no open row in the ledger")
	}
	if state.closed {
		return state, errors.New("plan " + planID.String() + " is already closed")
	}
	if !state.open.Principal.matches(principal) {
		return state, refuseDBPlanClose(ctx, deps, principal, state, postcheck)
	}
	return state, nil
}

// refuseDBPlanClose writes a refused close row, then mails the refusal to
// the alarm address, and returns the refusal error with any row or mail
// failure. The plan stays open.
func refuseDBPlanClose(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	state dbPlanState,
	postcheck string,
) error {
	planID := state.open.PlanID
	refused := errors.New("plan " + planID.String() + " refused the close: " + dbPlanPrincipalRefusal)
	slog.ErrorContext(ctx, "db.plan.close_refused",
		slog.String("plan_id", planID.String()), slog.String("err", refused.Error()))
	extra := newDBPlanCloseExtra(deps, principal, state, postcheck)
	recordErr := recordDBPlan(ctx, deps.outbox, audit.VerbOpsDBPlanClose, principal, extra, audit.OutcomeRefused, refused)
	mailErr := mailDBPlanCloseRefused(ctx, deps.cfg, principal, planID, dbPlanPrincipalRefusal)
	return errors.Join(refused, recordErr, mailErr)
}

// newDBPlanCloseExtra returns the extra payload of a close row by principal.
func newDBPlanCloseExtra(deps dbSQLDeps, principal audit.OperatorPrincipal, state dbPlanState, postcheck string) dbPlanExtra {
	return dbPlanExtra{
		PlanID: state.open.PlanID, SHA256: "", Statements: nil,
		Principal: newDBPlanPrincipal(principal, state.open.Reason), ExpiresAt: state.open.ExpiresAt,
		Reason: state.open.Reason, MailedTo: deps.cfg.BackupAlarmEmail, Postcheck: postcheck, Expired: false,
	}
}
