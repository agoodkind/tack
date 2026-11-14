package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// dbBreakGlassCaller identifies the command in the alarm email.
	dbBreakGlassCaller = "tack ops db sql"
	// dbBreakGlassRecordTimeout limits each ledger write independently of
	// command cancellation, as the token commands do.
	dbBreakGlassRecordTimeout = 30 * time.Second
	// dbBreakGlassRowLimit caps rows returned by a statement. Export commands
	// support streaming larger result sets.
	dbBreakGlassRowLimit = 1000
)

// dbSQLDeps provides the command's config, audit writer, and operator identity.
type dbSQLDeps struct {
	cfg      *config.Config
	outbox   audit.OutboxWriter
	identity audit.OperatorIdentitySource
}

// dbSQLResult reports the statement's outcome. Positional cells preserve
// duplicate column names. SQL null values use JSON null rather than strings.
type dbSQLResult struct {
	clispec.ResultMarker
	Command      string      `json:"command"`
	DryRun       bool        `json:"dry_run"`
	Statement    string      `json:"statement"`
	Reason       string      `json:"reason"`
	MailedTo     string      `json:"mailed_to,omitempty"`
	CommandTag   string      `json:"command_tag,omitempty"`
	RowsReturned int         `json:"rows_returned"`
	Truncated    bool        `json:"truncated"`
	Columns      []string    `json:"columns,omitempty"`
	Rows         [][]*string `json:"rows,omitempty"`
}

func runDBSQL(
	ctx context.Context,
	deps dbSQLDeps,
	input dbSQLInput,
	sink clispec.ResultSink,
	execute bool,
) error {
	statement := strings.TrimSpace(input.Statement)
	reason := strings.TrimSpace(input.Reason)
	if statement == "" {
		return errors.New("a statement is required")
	}

	if reason == "" {
		return errors.New(
			"a reason is required for the ledger record and the alarm email",
		)
	}

	if deps.cfg.BackupAlarmEmail == "" {
		return errors.New(
			"TACK_BACKUP_ALARM_EMAIL is empty; the command must email the statement before execution",
		)
	}

	result := dbSQLResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.db.sql",
		DryRun:       !execute,
		Statement:    statement,
		Reason:       reason,
		MailedTo:     deps.cfg.BackupAlarmEmail,
		CommandTag:   "",
		RowsReturned: 0,
		Truncated:    false,
		Columns:      nil,
		Rows:         nil,
	}
	if !execute {
		return writeDBSQLResult(ctx, sink, result)
	}

	principal, err := deps.identity.Resolve(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.principal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("resolve the operator for the break-glass statement: %w", err)
	}

	if err := mailDBBreakGlass(ctx, deps.cfg, principal, statement, reason); err != nil {
		return err
	}

	startedAt := clock.Now().UTC()
	extra := dbBreakGlassExtra{
		AttemptID:  uuid.Must(uuid.NewV7()),
		Statement:  dbStatementOperation(statement),
		Reason:     reason,
		MailedTo:   deps.cfg.BackupAlarmEmail,
		SessionID:  principal.SessionID,
		OnBehalfOf: onBehalfOfWithReason(principal, reason),
	}
	if err := recordDBBreakGlass(
		ctx,
		deps.outbox,
		principal,
		extra,
		startedAt,
		audit.OutcomeAttempted,
		nil,
	); err != nil {
		return err
	}

	outcome, runErr := runDBStatement(ctx, deps.cfg.DatabaseURL, statement)
	if dbStatementDenied(runErr) {
		denial := &audit.EventError{
			Code:    "permission_denied",
			Message: "database refused this operation",
		}
		if err := recordDBBreakGlass(
			ctx,
			deps.outbox,
			principal,
			extra,
			startedAt,
			audit.OutcomeRefused,
			denial,
		); err != nil {
			return errors.Join(runErr, err)
		}
	}

	if runErr != nil {
		return runErr
	}

	result.CommandTag, result.Columns, result.Rows = outcome.Tag, outcome.Columns, outcome.Rows
	result.RowsReturned, result.Truncated = len(outcome.Rows), outcome.Truncated

	return writeDBSQLResult(ctx, sink, result)
}

// mailDBBreakGlass emails the operator identity, statement, and reason to the
// alarm address before execution. Email failure prevents statement execution.
func mailDBBreakGlass(
	ctx context.Context,
	cfg *config.Config,
	principal audit.OperatorPrincipal,
	statement string,
	reason string,
) error {
	host := backupAlarmHost()
	identityLine, subjectEmail := dbBreakGlassMailIdentity(principal)
	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: "Break-glass database statement on " + host + " by " + subjectEmail,
		Body: identityLine + "\n" +
			"Host: " + host + "\n" +
			"Time: " + clock.Now().UTC().Format(backupAlarmTimeLayout) + "\n" +
			"Reason: " + reason + "\n" +
			"Statement:\n" + statement + "\n",
		From:   "",
		Name:   "",
		Caller: dbBreakGlassCaller,
	}
	if err := backupAlarmSendFunc(ctx, cfg, message); err != nil {
		slog.ErrorContext(
			ctx,
			"db.break_glass.mail_undelivered",
			slog.String("to", cfg.BackupAlarmEmail),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf(
			"email delivery to %s failed; the break-glass statement did not run: %w",
			cfg.BackupAlarmEmail,
			err,
		)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"db.break_glass.mail_sent",
		slog.String("to", cfg.BackupAlarmEmail),
	)

	return nil
}

// dbBreakGlassMailIdentity returns the first body line and subject email.
// A service principal with an accountable operator reports its agent, session,
// and operator email. Other principals report their own operator line and email.
func dbBreakGlassMailIdentity(principal audit.OperatorPrincipal) (string, string) {
	if principal.ActorType() == audit.ActorService && principal.OnBehalfOf != nil {
		accountable := principal.OnBehalfOf.OperatorEmail
		line := "Agent: " + principal.Name + " (session " + principal.SessionID + ") for " + accountable

		return line, accountable
	}

	line := "Operator: " + principal.Name + " <" + principal.Email + "> (" + principal.Source + ")"

	return line, principal.Email
}

func writeDBSQLResult(ctx context.Context, sink clispec.ResultSink, result dbSQLResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the break-glass report: %w", err)
	}

	return nil
}
