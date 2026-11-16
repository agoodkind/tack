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

// Reader queries audit.events using audit_reader_app grants that prohibit
// data changes. Ledger policies also prohibit row updates and deletion.
type Reader struct {
	pool *readerPool
}

// NewReader opens a pool against the audit_reader DSN.
func NewReader(ctx context.Context, dsn string) (*Reader, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_READER_DSN required")
	}

	cfg, err := parseReaderDSN(dsn)
	if err != nil {
		return nil, err
	}

	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	opened, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit reader pool open: %w", sanitizeReaderError(err))
	}

	pool := &readerPool{unsanitized: opened}
	// A failed startup ping logs a warning; the pool retries on the first query.
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

// QueryFilter requires an org selection and both time bounds. Page callers
// must enforce their upper row limit.
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
	// BeforeTime and BeforeSeq select rows strictly before this (event_time,
	// seq) position in descending order. Zero BeforeTime disables the cursor.
	BeforeTime time.Time `exhaustruct:"optional"`
	BeforeSeq  int64     `exhaustruct:"optional"`
	// NilOrg explicitly selects historical rows under the nil org. A nil OrgID
	// without this flag fails validation. Combining it with a non-nil OrgID also
	// fails; the flag must not disable org isolation.
	NilOrg bool `exhaustruct:"optional"`
}

// Validate requires explicit org selection and both time bounds, including
// explicit nil-org access. Row sources can enforce the same rules as Reader.
func (f QueryFilter) Validate() error {
	if f.NilOrg && f.OrgID != uuid.Nil {
		return fmt.Errorf("audit query: nil_org set beside org %s", f.OrgID)
	}

	if f.OrgID == uuid.Nil && !f.NilOrg {
		return errors.New("audit query: org_id required")
	}

	if f.Oldest.IsZero() || f.Latest.IsZero() {
		return errors.New("audit query: oldest and latest required")
	}

	return nil
}

// checkQueryFilter checks reader configuration and shared filter validation
// for Query and StreamQuery.
func (r *Reader) checkQueryFilter(f QueryFilter) error {
	if r == nil || r.pool == nil {
		return errors.New("audit reader not configured")
	}

	return f.Validate()
}

// Query collects matching events in descending time order for a page. The
// caller must cap the limit; whole-range reads must use StreamQuery instead.
func (r *Reader) Query(ctx context.Context, f QueryFilter) ([]Row, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.query",
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
		f.Limit = DefaultQueryPageLimit
	}

	q, args := buildAuditQuery(f)

	rows, err := r.pool.Rows(r.pool.unsanitized.Query(ctx, q, args...))
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

// GetByID reads an event without org filtering. The caller must authorize
// access to its org before returning the row to a user.
func (r *Reader) GetByID(ctx context.Context, eventID uuid.UUID) (*Row, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.get_by_id",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("audit.event_id", eventID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("event_id", eventID.String()))
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}

	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, outcome, entity_kind, entity_id,
		       context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
		       idempotency_key
		FROM audit.events
		WHERE event_id = $1
		LIMIT 1
	`,
			eventID,
		),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.get_query_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit get %s: %w", eventID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.get_rows_failed",
				slog.String("event_id", eventID.String()),
				slog.String("err", err.Error()),
			)

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

// outcomeFromColumn reports historical SQL NULL as unrecorded, not success.
func outcomeFromColumn(stored *string) Outcome {
	if stored == nil {
		return OutcomeUnrecorded
	}

	return Outcome(*stored)
}
