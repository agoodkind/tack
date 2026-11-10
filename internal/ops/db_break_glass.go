// db_break_glass.go runs one operator SQL statement against the database and
// leaves the traces the raw path never did: a mail to the alarm address sent
// before the statement runs, a ledger row recording the intent before the
// statement runs, and an outcome row after. The order is the control: a
// statement whose mail cannot be delivered or whose intent cannot be recorded
// does not run, so nobody reaches the database unobserved even when the
// ledger is the thing being examined, and a crash mid-statement leaves a
// pending row naming exactly what was attempted.

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
	// dbBreakGlassCaller names the command in the mail it sends.
	dbBreakGlassCaller = "tack ops db sql"
	// dbBreakGlassRecordTimeout bounds each ledger write, detached from the
	// command's own cancellation the way the token commands are.
	dbBreakGlassRecordTimeout = 30 * time.Second
	// dbBreakGlassRowLimit caps what one statement returns to the operator.
	// A break-glass read is a look, not an export; the export commands stream.
	dbBreakGlassRowLimit = 1000
)

// dbSQLDeps is what the command needs, split from the factory so a test can
// hand it a captured outbox and a fixed operator.
type dbSQLDeps struct {
	cfg      *config.Config
	outbox   audit.OutboxWriter
	identity audit.OperatorIdentitySource
}

// dbSQLResult reports the statement's outcome. Cells are positional under
// Columns, so a query with two columns of one name keeps both, and a SQL null
// is a JSON null rather than a string that could also be a value.
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

func runDBSQL(ctx context.Context, deps dbSQLDeps, input dbSQLInput, sink clispec.ResultSink, execute bool) error {
	statement := strings.TrimSpace(input.Statement)
	reason := strings.TrimSpace(input.Reason)
	if statement == "" {
		return errors.New("a statement is required")
	}
	if reason == "" {
		return errors.New("a reason is required; it is recorded in the ledger and mailed to the alarm address")
	}
	if deps.cfg.BackupAlarmEmail == "" {
		return errors.New("TACK_BACKUP_ALARM_EMAIL is empty; a break-glass statement must be mailed before it runs")
	}
	result := dbSQLResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.db.sql", DryRun: !execute,
		Statement: statement, Reason: reason, MailedTo: deps.cfg.BackupAlarmEmail,
		CommandTag: "", RowsReturned: 0, Truncated: false, Columns: nil, Rows: nil,
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
	extra := dbBreakGlassExtra{
		AttemptID: uuid.Must(uuid.NewV7()), Statement: statement, Reason: reason,
		MailedTo: deps.cfg.BackupAlarmEmail, CommandTag: "", RowsReturned: 0, Truncated: false,
		SessionID: principal.SessionID, OnBehalfOf: onBehalfOfWithReason(principal, reason),
	}
	if err := recordDBBreakGlass(ctx, deps.outbox, principal, extra, audit.OutcomePending, nil); err != nil {
		return err
	}
	outcome, runErr := runDBStatement(ctx, deps.cfg.DatabaseURL, statement)
	extra.CommandTag, extra.RowsReturned, extra.Truncated = outcome.Tag, len(outcome.Rows), outcome.Truncated
	recorded := audit.OutcomeOK
	if runErr != nil {
		recorded = audit.OutcomeError
	}
	if err := recordDBBreakGlass(ctx, deps.outbox, principal, extra, recorded, runErr); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil {
		return runErr
	}
	result.CommandTag, result.Columns, result.Rows = outcome.Tag, outcome.Columns, outcome.Rows
	result.RowsReturned, result.Truncated = len(outcome.Rows), outcome.Truncated
	return writeDBSQLResult(ctx, sink, result)
}

// mailDBBreakGlass tells the alarm address who is about to reach the database
// and why. It runs before the statement, and its failure is the statement's
// failure: the mail is what makes the access observed.
func mailDBBreakGlass(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, statement, reason string) error {
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
		From: "", Name: "", Caller: dbBreakGlassCaller,
	}
	if err := backupAlarmSendFunc(ctx, cfg, message); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.mail_undelivered",
			slog.String("to", cfg.BackupAlarmEmail), slog.String("err", err.Error()))
		return fmt.Errorf("the break-glass mail to %s was not delivered, so the statement did not run: %w", cfg.BackupAlarmEmail, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "db.break_glass.mail_sent", slog.String("to", cfg.BackupAlarmEmail))
	return nil
}

// dbBreakGlassMailIdentity returns the first body line of the alarm mail and
// the email address at the end of the subject. A service principal with an
// accountable operator reports the agent, its session, and the operator
// email. Every other principal reports the operator line and its own email.
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
