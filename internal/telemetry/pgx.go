package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryTracer implements pgx.QueryTracer to log every database query.
// Attaches to the pgxpool so all queries from any repo are covered.
type QueryTracer struct{}

type queryStartKey struct{}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, time.Now())
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, _ := ctx.Value(queryStartKey{}).(time.Time)
	latency := time.Since(start)

	l := L(ctx)

	if data.Err != nil {
		l.Error("db query",
			slog.String("sql", data.CommandTag.String()),
			slog.Duration("latency", latency),
			slog.Any("err", data.Err),
		)
		return
	}

	l.Debug("db query",
		slog.String("sql", data.CommandTag.String()),
		slog.Duration("latency", latency),
		slog.Int64("rows", data.CommandTag.RowsAffected()),
	)
}
