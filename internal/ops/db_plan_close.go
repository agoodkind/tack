package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

type dbPlanCloseInput struct {
	clispec.InputMarker
	PlanID    string
	Postcheck string
	// Wait is the Go duration that close waits for the relay and the audit
	// consumer to record the plan rows. An empty value waits
	// dbPlanProjectionWait.
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

// runDBPlanClose closes plan input.PlanID. With execute it waits until the
// relay has sent the events in the operator outbox and the audit consumer has
// committed past the audit topic high-water marks, then reads the plan rows
// through the ledger reader. It then refuses a closer that is not the opener
// principal, writes the close row to the operator outbox, and mails the
// summary. A failed ledger read, a wait past the bound, or a plan row in
// audit.events_dlq returns an error and writes no close row; a failed ledger
// read sends no mail. A refusal writes an ops.db_plan_close row with outcome
// refused, writes no close row with outcome ok, and returns an error. In each
// of these cases the plan stays open. A summary mail failure after the close
// row returns an error that states the mail failure; the plan is closed.
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
	extra := newDBPlanCloseExtra(deps, principal, state, postcheck)
	extra.Expired = result.Expired
	if err := recordDBPlan(ctx, deps.outbox, audit.VerbOpsDBPlanClose, principal, extra, audit.OutcomeOK, nil); err != nil {
		return err
	}
	if err := mailDBPlanSummary(ctx, deps.cfg, principal, state, postcheck, result.Expired); err != nil {
		slog.ErrorContext(ctx, "db.plan.summary_undelivered", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("plan %s is closed and its close row is written, but the summary mail failed: %w", planID, err)
	}
	return writeDBPlanResult(ctx, sink, result)
}

// closeDBPlanRows returns the plan state that the summary reports. It waits
// for the relay and the audit consumer, then reads the plan rows from
// audit.events and audit.events_dlq through the ledger reader. Every ledger
// read in close stops at the first failure: a failed read of
// public.ops_outbox, audit.consumer_offsets, or the plan rows returns that
// error at once, logged here, with no close row and no mail. A wait past its
// bound or a plan row in audit.events_dlq mails that the summary is
// incomplete and returns an error. A plan row that does not decode refuses
// the close: the stored plan cannot be verified.
// It then checks the open row, the close row, and the closer principal.
func closeDBPlanRows(
	ctx context.Context,
	deps dbSQLDeps,
	principal audit.OperatorPrincipal,
	planID uuid.UUID,
	postcheck string,
	wait time.Duration,
) (dbPlanState, error) {
	empty := dbPlanState{open: nil, closed: false, attempts: nil}
	if err := awaitDBPlanProjection(ctx, deps, planID, wait); err != nil {
		if isDBPlanReadError(err) {
			return empty, dbPlanReadFailed(ctx, "plan close", planID, err)
		}
		return empty, dbPlanSummaryIncomplete(ctx, deps.cfg, principal, planID, err)
	}
	state, deadLetters, err := readDBPlanLedger(ctx, deps.cfg.AuditReaderDSN, planID)
	if isDBPlanReadError(err) {
		return state, dbPlanReadFailed(ctx, "plan close", planID, err)
	}
	if err != nil {
		return empty, refuseUnverifiableDBPlanClose(ctx, deps, principal, planID, postcheck, err)
	}
	if len(deadLetters) > 0 {
		cause := errors.New("audit.events_dlq has " + strconv.Itoa(len(deadLetters)) + " rows of plan " +
			planID.String() + ": " + strings.Join(deadLetters, "; "))
		return empty, dbPlanSummaryIncomplete(ctx, deps.cfg, principal, planID, cause)
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

// dbPlanSummaryIncomplete mails that the summary of planID is incomplete
// with cause, and returns cause joined with any mail failure.
func dbPlanSummaryIncomplete(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, planID uuid.UUID, cause error) error {
	incomplete := errors.Join(cause, mailDBPlanSummaryIncomplete(ctx, cfg, principal, planID, cause))
	slog.ErrorContext(ctx, "db.plan.summary_incomplete",
		slog.String("plan_id", planID.String()), slog.String("err", incomplete.Error()))
	return incomplete
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
