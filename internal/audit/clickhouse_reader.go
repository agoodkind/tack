package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ClickHouse/clickhouse-go/v2"
	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// ClickHouseReader answers tack_audit_query from the audit.events_olap
// projection. ClickHouse is the fast analytical read tier; it is not the
// canonical store, so the QueryRouter only sends recent-window reads here and
// keeps chain verification and old windows on Yugabyte.
type ClickHouseReader struct {
	conn chdriver.Conn
}

// NewClickHouseReader opens a ClickHouse connection for reads. It does not
// create the schema; the audit-consumer owns audit.events_olap.
func NewClickHouseReader(ctx context.Context, dsn string) (*ClickHouseReader, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_CLICKHOUSE_DSN required")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "audit.clickhouse_reader.dsn_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit clickhouse reader dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		slog.ErrorContext(ctx, "audit.clickhouse_reader.open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit clickhouse reader open: %w", err)
	}
	err = conn.Ping(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.clickhouse_reader.ping_failed", slog.String("err", err.Error()))
		_ = conn.Close()
		return nil, fmt.Errorf("audit clickhouse reader ping: %w", err)
	}
	return &ClickHouseReader{conn: conn}, nil
}

// Close releases the ClickHouse connection. Idempotent.
func (r *ClickHouseReader) Close() {
	if r != nil && r.conn != nil {
		_ = r.conn.Close()
	}
}

// Query returns events matching the filter from audit.events_olap, most recent
// first. The JSONB columns are stored as ClickHouse String, so context filters
// use JSONExtractString and the rows scan back into [json.RawMessage].
//
// The query binds a fixed argument list and disables each optional filter with
// a sentinel guard, so the statement text is constant and plan-cacheable.
func (r *ClickHouseReader) Query(ctx context.Context, f QueryFilter) ([]Row, error) {
	ctx, span := telemetry.StartSpan(ctx, "audit.query_olap",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("org.id", f.OrgID.String()),
			attribute.Int("audit.limit", f.Limit),
		),
	)
	defer span.End()
	if r == nil || r.conn == nil {
		return nil, errors.New("audit clickhouse reader not configured")
	}
	if f.OrgID == uuid.Nil {
		return nil, errors.New("audit query: org_id required")
	}
	if f.Oldest.IsZero() || f.Latest.IsZero() {
		return nil, errors.New("audit query: oldest and latest required")
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	actorSet := 0
	if f.ActorID != uuid.Nil {
		actorSet = 1
	}
	entitySet := 0
	if f.EntityID != uuid.Nil {
		entitySet = 1
	}

	const query = `SELECT org_id, event_time, event_id, seq, shard,
	                      actor_id, actor_kind, action, outcome, error, extra, entity_kind, entity_id,
	                      context, delta, idempotency_key
	               FROM audit.events_olap
	               WHERE org_id = ?
	                 AND event_time >= ? AND event_time < ?
	                 AND (? = '' OR action = ?)
	                 AND (? = 0 OR actor_id = ?)
	                 AND (? = 0 OR entity_id = ?)
	                 AND (? = '' OR JSONExtractString(context, 'request_id') = ?)
	                 AND (? = '' OR JSONExtractString(context, 'trace_id') = ?)
	               ORDER BY event_time DESC, seq DESC
	               LIMIT ?`
	rows, err := r.conn.Query(ctx, query,
		f.OrgID,
		f.Oldest, f.Latest,
		f.Action, f.Action,
		actorSet, f.ActorID,
		entitySet, f.EntityID,
		f.RequestID, f.RequestID,
		f.TraceID, f.TraceID,
		f.Limit,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.query_olap.failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit olap query: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var contextStr, deltaStr, errorStr, extraStr string
		var outcome string
		err = rows.Scan(
			&row.OrgID, &row.EventTime, &row.EventID, &row.Seq, &row.Shard,
			&row.ActorID, &row.ActorKind, &row.Action, &outcome,
			&errorStr, &extraStr, &row.EntityKind, &row.EntityID, &contextStr, &deltaStr, &row.IdempotencyKey,
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.query_olap.scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit olap row scan: %w", err)
		}
		row.Context = json.RawMessage(contextStr)
		row.Delta = json.RawMessage(deltaStr)
		row.Error = json.RawMessage(errorStr)
		row.Extra = json.RawMessage(extraStr)
		row.Outcome = clickHouseOutcomeFromColumn(outcome)
		out = append(out, row)
	}
	err = rows.Err()
	if err != nil {
		slog.ErrorContext(ctx, "audit.query_olap.rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit olap rows: %w", err)
	}
	return out, nil
}

func clickHouseOutcomeFromColumn(stored string) Outcome {
	if stored == "" {
		return outcomeFromColumn(nil)
	}
	return outcomeFromColumn(&stored)
}
