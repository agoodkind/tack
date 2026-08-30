package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	// Connect lazily. A failed ping logs a deferred warning but still returns
	// the reader; the pool connects on first query.
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
	// BeforeTime and BeforeSeq resume a page: only rows strictly before this
	// (event_time, seq) position in the DESC ordering return. Zero BeforeTime
	// disables the cursor. The Yugabyte reader honors it; the OLAP reader
	// does not, so paging callers read the canonical store.
	BeforeTime time.Time `exhaustruct:"optional"`
	BeforeSeq  int64     `exhaustruct:"optional"`
}

// checkQueryFilter rejects a filter no ledger read can run: a reader that was
// never configured, a missing org, or a time range with an open end. Shared by
// Query and StreamQuery so both refuse the same inputs.
func (r *Reader) checkQueryFilter(f QueryFilter) error {
	if r == nil || r.pool == nil {
		return errors.New("audit reader not configured")
	}
	if f.OrgID == uuid.Nil {
		return errors.New("audit query: org_id required")
	}
	if f.Oldest.IsZero() || f.Latest.IsZero() {
		return errors.New("audit query: oldest and latest required")
	}
	return nil
}

// Query returns events matching the filter, most recent first, holding every
// matching row in memory. It is the page-sized read; a caller that wants the
// whole match set streams it through StreamQuery instead. The caller is
// responsible for upper-bounding the limit.
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
	if err := r.checkQueryFilter(f); err != nil {
		return nil, err
	}
	if f.Limit <= 0 {
		f.Limit = auditQueryPageDefault
	}

	q, args := buildAuditQuery(f)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit query: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		row, err := readAuditRow(rows)
		if err != nil {
			return nil, err
		}
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
	rows, err := r.pool.Query(ctx, `
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, outcome, entity_kind, entity_id,
		       context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
		       idempotency_key
		FROM audit.events
		WHERE event_id = $1
		LIMIT 1
	`, eventID)
	if err != nil {
		slog.ErrorContext(ctx, "audit.get_query_failed",
			slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit get %s: %w", eventID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			slog.ErrorContext(ctx, "audit.get_rows_failed",
				slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit get %s: %w", eventID, err)
		}
		return nil, pgx.ErrNoRows
	}
	row, err := readAuditRow(rows)
	if err != nil {
		return nil, err
	}
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
