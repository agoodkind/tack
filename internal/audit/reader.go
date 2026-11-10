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

// Reader queries audit.events through a pool configured with the reader DSN.
// The audit_reader role grants SELECT access without data changes. Row-level
// security policies do not permit ordinary roles to UPDATE or DELETE events.
// Superusers can bypass these policies.
type Reader struct {
	pool *readerPool
}

// NewReader opens a pool using dsn for audit reads.
//
// The caller supplies the audit_reader connection string. An empty string or
// invalid pool configuration returns an error. A failed initial ping logs a
// warning and still returns the reader.
//
//   - A successful ping establishes connectivity during construction.
//   - After a failed ping, later queries can attempt to connect again.
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
	// The ping attempts a connection. A failed ping logs a warning and still
	// returns the reader; later queries can attempt to connect again.
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

// QueryFilter follows Slack's getLogs filter shape. Oldest and Latest must
// bound the time range to prevent a scan without time limits. Callers must cap
// Limit at 1000.
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
	// BeforeTime and BeforeSeq resume a page strictly before the specified
	// (event_time, seq) position in descending order. A zero BeforeTime disables
	// the cursor.
	BeforeTime time.Time `exhaustruct:"optional"`
	BeforeSeq  int64     `exhaustruct:"optional"`
	// NilOrg explicitly selects rows under the nil organization, including
	// history from before every event recorded an organization. These rows use
	// the same per-(org, shard) chain structure as other organizations. The filter
	// rejects a nil OrgID without NilOrg and rejects NilOrg beside a non-nil OrgID.
	// These checks require an explicit nil-organization choice and reject a flag
	// that conflicts with an organization ID.
	NilOrg bool `exhaustruct:"optional"`
}

// Validate rejects missing or conflicting organization choices and missing
// time bounds.
//
// [Reader.Query] and [Reader.StreamQuery] invoke Validate through their shared
// filter check. Other row sources can enforce the same restrictions.
//
//   - A nil OrgID requires NilOrg.
//   - A non-nil OrgID forbids NilOrg.
//   - Oldest and Latest must both be nonzero.
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

// checkQueryFilter rejects an unconfigured reader and then validates f.
// Query and StreamQuery use this check to reject the same inputs.
func (r *Reader) checkQueryFilter(f QueryFilter) error {
	if r == nil || r.pool == nil {
		return errors.New("audit reader not configured")
	}

	return f.Validate()
}

// Query returns matching events with the most recent events first and stores
// the result rows in memory.
//
// The caller must cap positive limits. A nonpositive limit uses the default
// page size. [Reader.StreamQuery] reads the complete matching set without
// collecting every row in a slice.
//
//   - Results sort by event_time descending, then seq descending.
//   - A row or query error stops the read and returns an error.
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

// GetByID returns one event without filtering by organization.
//
// The caller must enforce organization authorization before exposing the row to
// a user. An absent event returns pgx.ErrNoRows.
//
//   - The query matches eventID and limits the result to one row.
//   - Connection, query, and row decoding failures return errors.
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

// outcomeFromColumn converts a stored outcome to Outcome. A NULL represents
// a row from before the ledger stored outcomes and returns OutcomeUnrecorded.
// The absent evidence does not establish that the event succeeded.
func outcomeFromColumn(stored *string) Outcome {
	if stored == nil {
		return OutcomeUnrecorded
	}

	return Outcome(*stored)
}
