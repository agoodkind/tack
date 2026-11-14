// reader_stream.go reads matching ledger rows without collecting the result set.

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/telemetry"
)

// RowVisitor receives one ledger row valid for the duration of the call.
// The visitor must copy any values it retains. An error stops the stream.
type RowVisitor func(Row) error

// StreamQuery calls visit for each row that matches the Query filter. It keeps
// one decoded row and the connection's read buffer rather than collecting rows.
// A zero limit selects every match for compliance exports; Query instead
// defaults a zero limit to one page.
func (r *Reader) StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.stream_query",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("org.id", f.OrgID.String()),
			attribute.Int("audit.limit", f.Limit),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("org_id", f.OrgID.String()))
	if err := r.checkQueryFilter(f); err != nil {
		return err
	}

	query, args := buildAuditQuery(f)
	rows, err := r.pool.Rows(r.pool.unsanitized.Query(ctx, query, args...))
	if err != nil {
		slog.ErrorContext(ctx, "audit.stream_query.failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit stream query: %w", err)
	}
	defer rows.Close()

	streamed := 0

	for rows.Next() {
		row, readErr := readAuditRow(rows)
		if readErr != nil {
			return readErr
		}

		if visitErr := visit(row); visitErr != nil {
			return fmt.Errorf("audit stream query row %s: %w", row.EventID, visitErr)
		}

		streamed++
	}

	// Check rows.Err after iteration to distinguish completion from a dropped
	// connection or canceled context. A partial export must return an error.
	if err := rows.Err(); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.stream_query.interrupted",
			slog.Int("rows_streamed", streamed),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit stream query after %d rows: %w", streamed, err)
	}

	return nil
}
