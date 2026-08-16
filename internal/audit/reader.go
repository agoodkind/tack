package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// Reader queries audit.events through the audit_reader_app pool. Read-only
// by construction: the role's grants block any DML, and the policies in
// migrations/002_audit.sql block UPDATE/DELETE for everyone.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader opens a pool against the audit_reader DSN.
func NewReader(ctx context.Context, dsn string) (*Reader, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_READER_DSN required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("audit reader pool config: %w", err)
	}
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit reader pool open: %w", err)
	}
	// Connect lazily. On a fresh environment the audit_reader role may not exist
	// yet (seed-roles runs after the app starts). A failed ping logs a deferred
	// warning but still returns the reader, so the audit query/get MCP tools
	// register at startup; the pool connects on first query once the role exists,
	// removing the need to restart the app after seed-roles (TACK-319).
	if err := pool.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "audit.reader.ping_deferred", slog.String("err", err.Error()))
	}
	return &Reader{pool: pool}, nil
}

func (r *Reader) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// QueryFilter mirrors Slack's getLogs shape. Oldest and Latest bound the
// time range and are mandatory; an unbounded scan would foot-gun production.
// Limit is capped at 1000 by the caller.
type QueryFilter struct {
	OrgID     uuid.UUID
	Oldest    time.Time
	Latest    time.Time
	Action    string
	ActorID   uuid.UUID
	EntityID  uuid.UUID
	RequestID string
	TraceID   string
	Limit     int
}

// Query returns events matching the filter, most recent first. The caller
// is responsible for upper-bounding the limit.
func (r *Reader) Query(ctx context.Context, f QueryFilter) ([]Row, error) {
	ctx, span := telemetry.StartSpan(ctx, "audit.query",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("org.id", f.OrgID.String()),
			attribute.Int("audit.limit", f.Limit),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("org_id", f.OrgID.String()))
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
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

	q, args := buildAuditQuery(f)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit query: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var outcome *string
		var contextRaw, deltaRaw, errorRaw, extraRaw []byte
		if err := rows.Scan(
			&row.OrgID, &row.EventTime, &row.EventID, &row.Seq, &row.Shard,
			&row.ActorID, &row.ActorKind, &row.Action, &outcome,
			&row.EntityKind, &row.EntityID, &contextRaw, &deltaRaw, &errorRaw, &extraRaw, &row.PIIRef,
			&row.PrevHash, &row.RowHash, &row.HashVersion, &row.IdempotencyKey,
		); err != nil {
			return nil, fmt.Errorf("audit row scan: %w", err)
		}
		if err := unmarshalRowPayloads(&row, contextRaw, deltaRaw, errorRaw, extraRaw); err != nil {
			return nil, err
		}
		row.Outcome = outcomeFromColumn(outcome)
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetByID returns one event regardless of org. Caller must enforce org
// authorization before exposing the row to the user.
func (r *Reader) GetByID(ctx context.Context, eventID uuid.UUID) (*Row, error) {
	ctx, span := telemetry.StartSpan(ctx, "audit.get_by_id",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("audit.event_id", eventID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("event_id", eventID.String()))
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}
	var row Row
	var outcome *string
	var contextRaw, deltaRaw, errorRaw, extraRaw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, outcome, entity_kind, entity_id,
		       context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
		       idempotency_key
		FROM audit.events
		WHERE event_id = $1
		LIMIT 1
	`, eventID).Scan(
		&row.OrgID, &row.EventTime, &row.EventID, &row.Seq, &row.Shard,
		&row.ActorID, &row.ActorKind, &row.Action, &outcome,
		&row.EntityKind, &row.EntityID, &contextRaw, &deltaRaw, &errorRaw, &extraRaw, &row.PIIRef,
		&row.PrevHash, &row.RowHash, &row.HashVersion, &row.IdempotencyKey,
	)
	if err != nil {
		return nil, err
	}
	if err := unmarshalRowPayloads(&row, contextRaw, deltaRaw, errorRaw, extraRaw); err != nil {
		return nil, err
	}
	row.Outcome = outcomeFromColumn(outcome)
	return &row, nil
}

// outcomeFromColumn maps the stored outcome to its value. A NULL means the row
// was written before the ledger stored outcomes, so it reads as unrecorded
// rather than being reported as a success nobody observed.
func outcomeFromColumn(stored *string) Outcome {
	if stored == nil {
		return OutcomeUnrecorded
	}
	return Outcome(*stored)
}
