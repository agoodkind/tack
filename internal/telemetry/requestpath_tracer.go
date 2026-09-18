package telemetry

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CountingQueryTracer is the [pgx.QueryTracer] the app's ledger pool carries.
// It counts every query on the request attached to the query's context, then
// hands the query to Next, the tracer that logs it. Next may be nil.
type CountingQueryTracer struct {
	Next pgx.QueryTracer
}

// TraceQueryStart counts the query and delegates to Next.
func (t *CountingQueryTracer) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	CountLedgerQuery(ctx, data.SQL)
	if t.Next == nil {
		return ctx
	}
	return t.Next.TraceQueryStart(ctx, conn, data)
}

// TraceQueryEnd delegates to Next.
func (t *CountingQueryTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if t.Next == nil {
		return
	}
	t.Next.TraceQueryEnd(ctx, conn, data)
}
