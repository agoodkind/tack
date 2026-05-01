package telemetry

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// QueryTracer implements pgx.QueryTracer to log every database query.
// Attaches to the pgxpool so all queries from any repo are covered.
type QueryTracer struct{}

type queryStartKey struct{}
type querySpanKey struct{}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx = context.WithValue(ctx, queryStartKey{}, time.Now())
	operation := queryOperation(data.SQL)
	ctx, span := StartSpan(ctx, "db.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.operation", operation)),
	)
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, _ := ctx.Value(queryStartKey{}).(time.Time)
	latency := time.Since(start)
	span, _ := ctx.Value(querySpanKey{}).(trace.Span)
	if span != nil {
		span.SetAttributes(
			attribute.String("db.response.command_tag", data.CommandTag.String()),
			attribute.Int64("db.response.rows_affected", data.CommandTag.RowsAffected()),
			attribute.Int64("db.query.duration_ms", latency.Milliseconds()),
		)
		if data.Err != nil {
			span.RecordError(data.Err)
			span.SetStatus(codes.Error, data.Err.Error())
		} else {
			span.SetStatus(codes.Ok, "ok")
		}
		span.End()
	}

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

func queryOperation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "unknown"
	}
	return strings.ToUpper(fields[0])
}
