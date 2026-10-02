package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/telemetry"
)

// dbStatementResult is the output of one statement.
type dbStatementResult struct {
	Tag       string
	Columns   []string
	Rows      [][]*string
	Truncated bool
}

// runDBStatement runs the statement through the extended protocol as one
// unnamed prepared statement. The server refuses more than one command in an
// unnamed prepared statement, and that refusal enforces the one-statement
// contract without parsing here. The query requests text results, and every
// cell is the server's text rendering.
func runDBStatement(ctx context.Context, dsn, statement string) (dbStatementResult, error) {
	none := dbStatementResult{Tag: "", Columns: nil, Rows: nil, Truncated: false}
	pool, err := postgres.NewPool(ctx, dsn, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.pool_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("open the database for the break-glass statement: %w", err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, statement, pgx.QueryExecModeExec, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		slog.ErrorContext(ctx, "db.break_glass.query_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("run the break-glass statement: %w", err)
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
		slog.ErrorContext(ctx, "db.break_glass.rows_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("run the break-glass statement: %w", err)
	}
	outcome.Tag = rows.CommandTag().String()
	return outcome, nil
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
