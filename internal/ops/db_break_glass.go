// db_break_glass.go runs one operator SQL statement against the database and
// leaves the two traces the raw path never did: a ledger row naming the
// operator, the reason, and the statement, and a mail to the alarm address
// sent before the statement runs. The order is the control: a statement whose
// mail cannot be delivered does not run, so nobody reaches the database
// unobserved even when the ledger is the thing being examined.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// dbBreakGlassCaller names the command in the mail it sends.
const dbBreakGlassCaller = "tack ops db sql"

// dbBreakGlassRecordTimeout bounds the ledger write, detached from the
// command's own cancellation the way the token commands are.
const dbBreakGlassRecordTimeout = 30 * time.Second

// dbSQLDeps is what the command needs, split from the factory so a test can
// hand it a captured outbox and a fixed operator.
type dbSQLDeps struct {
	cfg      *config.Config
	outbox   audit.OutboxWriter
	identity audit.OperatorIdentitySource
}

type dbSQLResult struct {
	clispec.ResultMarker
	Command      string              `json:"command"`
	DryRun       bool                `json:"dry_run"`
	Statement    string              `json:"statement"`
	Reason       string              `json:"reason"`
	MailedTo     string              `json:"mailed_to,omitempty"`
	CommandTag   string              `json:"command_tag,omitempty"`
	RowsReturned int                 `json:"rows_returned"`
	Columns      []string            `json:"columns,omitempty"`
	Rows         []map[string]string `json:"rows,omitempty"`
}

// dbBreakGlassExtra is what the ledger row carries beyond the choke-point's
// pair: the statement and the reason, so the record says what was done and
// why rather than only that the command ran.
type dbBreakGlassExtra struct {
	AttemptID    uuid.UUID `json:"attempt_id"`
	Statement    string    `json:"statement"`
	Reason       string    `json:"reason"`
	MailedTo     string    `json:"mailed_to"`
	CommandTag   string    `json:"command_tag,omitempty"`
	RowsReturned int       `json:"rows_returned"`
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
		CommandTag: "", RowsReturned: 0, Columns: nil, Rows: nil,
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
	attemptID := uuid.Must(uuid.NewV7())
	tag, columns, rows, err := runDBStatement(ctx, deps.cfg.DatabaseURL, statement)
	extra := dbBreakGlassExtra{
		AttemptID: attemptID, Statement: statement, Reason: reason,
		MailedTo: deps.cfg.BackupAlarmEmail, CommandTag: tag, RowsReturned: len(rows),
	}
	if recordErr := recordDBBreakGlass(ctx, deps.outbox, principal, reason, extra, err); recordErr != nil {
		return recordErr
	}
	if err != nil {
		return err
	}
	result.CommandTag, result.Columns, result.Rows, result.RowsReturned = tag, columns, rows, len(rows)
	return writeDBSQLResult(ctx, sink, result)
}

// mailDBBreakGlass tells the alarm address who is about to reach the database
// and why. It runs before the statement, and its failure is the statement's
// failure: the mail is what makes the access observed.
func mailDBBreakGlass(ctx context.Context, cfg *config.Config, principal audit.OperatorPrincipal, statement, reason string) error {
	host := backupAlarmHost()
	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: "Break-glass database statement on " + host + " by " + principal.Email,
		Body: "Operator: " + principal.Name + " <" + principal.Email + "> (" + principal.Source + ")\n" +
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

// runDBStatement runs the statement over the simple protocol, so every column
// comes back as text and the report needs no type knowledge.
func runDBStatement(ctx context.Context, dsn, statement string) (string, []string, []map[string]string, error) {
	pool, err := postgres.NewPool(ctx, dsn, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.pool_failed", slog.String("err", err.Error()))
		return "", nil, nil, fmt.Errorf("open the database for the break-glass statement: %w", err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, statement, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.query_failed", slog.String("err", err.Error()))
		return "", nil, nil, fmt.Errorf("run the break-glass statement: %w", err)
	}
	defer rows.Close()
	columns := make([]string, 0, len(rows.FieldDescriptions()))
	for _, field := range rows.FieldDescriptions() {
		columns = append(columns, field.Name)
	}
	var out []map[string]string
	for rows.Next() {
		row := make(map[string]string, len(columns))
		for index, raw := range rows.RawValues() {
			if raw == nil {
				row[columns[index]] = "NULL"
				continue
			}
			row[columns[index]] = string(raw)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.rows_failed", slog.String("err", err.Error()))
		return "", nil, nil, fmt.Errorf("read the break-glass statement result: %w", err)
	}
	return rows.CommandTag().String(), columns, out, nil
}

// recordDBBreakGlass writes the detail row: the choke-point already recorded
// that the command ran and how it ended; this row says what it ran.
func recordDBBreakGlass(
	ctx context.Context,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	reason string,
	extra dbBreakGlassExtra,
	runErr error,
) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.extra_encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode the break-glass record: %w", err)
	}
	event := audit.Event{
		Verb: string(audit.VerbOpsDBBreakGlass), EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "database", NodeType: "", ID: extra.AttemptID, Identifier: "", Name: extra.CommandTag},
		Context: audit.EventContext{
			OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: reason,
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: encoded,
	}
	if runErr != nil {
		event.Outcome = audit.OutcomeError
		event.Error = &audit.EventError{Code: "statement_failed", Message: runErr.Error()}
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbBreakGlassRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.record_failed", slog.String("err", err.Error()))
		return fmt.Errorf("record the break-glass statement: %w", err)
	}
	return nil
}

func writeDBSQLResult(ctx context.Context, sink clispec.ResultSink, result dbSQLResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "db.break_glass.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the break-glass report: %w", err)
	}
	return nil
}
