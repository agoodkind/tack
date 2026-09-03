// reader_stream.go is the whole-match-set read. Query builds a slice, which
// bounds it to what fits in memory; this hands each row to the caller as it is
// decoded and keeps none, so a read of a production-sized org costs the same as
// a read of one row.

package audit

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/telemetry"
)

// RowVisitor receives one ledger row. The row is valid for the duration of the
// call and is released afterwards, so a visitor that needs to keep something
// must copy what it needs. Returning an error stops the stream.
type RowVisitor func(Row) error

// StreamQuery runs the same filter Query runs and hands every matching row to
// visit as it arrives off the connection. Peak memory is one row plus the
// connection's read buffer, whatever the filter matches, because nothing here
// accumulates: pgx reads one protocol message per row rather than collecting
// the result set.
//
// A zero limit means every matching row, which is where this differs from
// Query. Query defaults a missing limit to a page because its callers render a
// page; the caller here is the compliance export, and an export that stopped at
// a page would be a truncated bundle that reads as a complete one.
func (r *Reader) StreamQuery(ctx context.Context, f QueryFilter, visit RowVisitor) error {
	ctx, span := telemetry.StartSpan(ctx, "audit.stream_query",
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
	rows, err := r.pool.Query(ctx, query, args...)
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
	// rows.Err is what separates a stream that ended from one that was cut off
	// mid-result by a dropped connection or a cancelled context. Reading only
	// the loop's exit would turn a partial read into a short, silent success,
	// which for an export is a bundle missing its tail.
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.stream_query.interrupted",
			slog.Int("rows_streamed", streamed), slog.String("err", err.Error()))
		return fmt.Errorf("audit stream query after %d rows: %w", streamed, err)
	}
	return nil
}
