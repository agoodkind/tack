package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/telemetry"
)

// dbStatementResult is the output of one statement.
type dbStatementResult struct {
	Tag       string
	Columns   []string
	Rows      [][]*string
	Truncated bool
}

const (
	dbReadOnlySetting = "default_transaction_read_only"
	// dbReadOnlySQLState is read_only_sql_transaction.
	dbReadOnlySQLState = "25006"
)

var errDBStatementWrites = errors.New(
	"ops db sql is read-only and the statement changes data or schema; " +
		"a database write needs a migration or a reviewed ops command")

// runDBStatement sends the statement with the extended protocol as one
// unnamed prepared statement. The server refuses more than one command in an
// unnamed prepared statement, and that refusal enforces the one-statement
// contract without parsing here. Every cell is the server's text rendering.
func runDBStatement(ctx context.Context, dsn, statement string) (dbStatementResult, error) {
	none := dbStatementResult{Tag: "", Columns: nil, Rows: nil, Truncated: false}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.dsn_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("parse the database address for the break-glass statement: %w", err)
	}
	connConfig.RuntimeParams[dbReadOnlySetting] = "on"
	connConfig.Tracer = &telemetry.QueryTracer{}
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.connect_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("open the database for the break-glass statement: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	rows, err := conn.Query(ctx, statement, pgx.QueryExecModeExec, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return none, dbStatementError(ctx, err)
	}
	defer rows.Close()
	outcome := dbStatementResult{Tag: "", Columns: nil, Rows: nil, Truncated: false}
	for _, field := range rows.FieldDescriptions() {
		outcome.Columns = append(outcome.Columns, field.Name)
	}
	for rows.Next() {
		if len(outcome.Rows) == dbBreakGlassRowLimit {
			outcome.Truncated = true
			break
		}
		outcome.Rows = append(outcome.Rows, textCells(rows.RawValues()))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return none, dbStatementError(ctx, err)
	}
	outcome.Tag = rows.CommandTag().String()
	return outcome, nil
}

func dbStatementError(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "db.break_glass.query_failed", slog.String("err", err.Error()))
	var serverErr *pgconn.PgError
	if errors.As(err, &serverErr) && serverErr.Code == dbReadOnlySQLState {
		return fmt.Errorf("%w: %w", errDBStatementWrites, err)
	}
	return fmt.Errorf("run the break-glass statement: %w", err)
}

// textCells copies one row out of the connection's buffers, keeping a SQL
// null as a nil cell.
func textCells(raw [][]byte) []*string {
	cells := make([]*string, 0, len(raw))
	for _, value := range raw {
		if value == nil {
			cells = append(cells, nil)
			continue
		}
		text := string(value)
		cells = append(cells, &text)
	}
	return cells
}
