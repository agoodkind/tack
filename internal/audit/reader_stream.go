// StreamQuery reads the complete matching set without collecting result rows.
// Query builds a slice limited by available memory. StreamQuery invokes a
// visitor for each decoded row and does not retain the rows between calls.
// Total decoding and visitor work increases with the number of matching rows.

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/telemetry"
)

// RowVisitor receives one ledger row for the duration of a call. A visitor
// that retains row data must copy the values it needs. An error stops the
// stream.
type RowVisitor func(Row) error

// StreamQuery applies the same filter as [Reader.Query] and invokes visit for
// each decoded row without accumulating a result slice.
//
// A zero limit selects every matching row. Query instead defaults a missing
// limit to a page for callers that render pages. The compliance export requires
// the complete matching set; a default page would create a truncated bundle
// that appears complete.
//
//   - The method retains the current row and connection buffers, rather than
//     all returned rows. Row and protocol message sizes affect memory use.
//   - pgx decodes rows from protocol messages rather than collecting the full
//     result set. The visitor controls any additional retained data.
//   - A query, decoding, visitor, or interrupted-result error stops the stream.
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

	// rows.Err distinguishes a completed result from an interruption, including
	// a dropped connection or canceled context. Ignoring this error would report
	// a partial export as a successful complete read.
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
