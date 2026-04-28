package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit reader pool open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("audit reader pool ping: %w", err)
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
	OrgID    uuid.UUID
	Oldest   time.Time
	Latest   time.Time
	Action   string
	ActorID  uuid.UUID
	EntityID uuid.UUID
	Limit    int
}

// Row is a flattened audit row sized for the MCP tool surface. The JSONB
// columns come back as raw bytes so callers can re-render in their own
// shape (markdown table, JSON, ...).
type Row struct {
	OrgID          uuid.UUID
	EventTime      time.Time
	EventID        uuid.UUID
	Seq            int64
	Shard          int16
	ActorID        uuid.UUID
	ActorKind      int16
	Action         string
	EntityKind     string
	EntityID       uuid.UUID
	Context        json.RawMessage
	Delta          json.RawMessage
	IdempotencyKey string
}

// Query returns events matching the filter, most recent first. The caller
// is responsible for upper-bounding the limit.
func (r *Reader) Query(ctx context.Context, f QueryFilter) ([]Row, error) {
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

	q := `SELECT org_id, event_time, event_id, seq, shard,
	             actor_id, actor_kind, action, entity_kind, entity_id,
	             context, delta, idempotency_key
	      FROM audit.events
	      WHERE org_id = $1
	        AND event_time >= $2 AND event_time < $3`
	args := []any{f.OrgID, f.Oldest, f.Latest}
	if f.Action != "" {
		args = append(args, f.Action)
		q += fmt.Sprintf(" AND action = $%d", len(args))
	}
	if f.ActorID != uuid.Nil {
		args = append(args, f.ActorID)
		q += fmt.Sprintf(" AND actor_id = $%d", len(args))
	}
	if f.EntityID != uuid.Nil {
		args = append(args, f.EntityID)
		q += fmt.Sprintf(" AND entity_id = $%d", len(args))
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY event_time DESC, seq DESC LIMIT $%d", len(args))

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit query: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		if err := rows.Scan(
			&row.OrgID, &row.EventTime, &row.EventID, &row.Seq, &row.Shard,
			&row.ActorID, &row.ActorKind, &row.Action, &row.EntityKind, &row.EntityID,
			&row.Context, &row.Delta, &row.IdempotencyKey,
		); err != nil {
			return nil, fmt.Errorf("audit row scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetByID returns one event regardless of org. Caller must enforce org
// authorization before exposing the row to the user.
func (r *Reader) GetByID(ctx context.Context, eventID uuid.UUID) (*Row, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}
	row := &Row{}
	err := r.pool.QueryRow(ctx, `
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, entity_kind, entity_id,
		       context, delta, idempotency_key
		FROM audit.events
		WHERE event_id = $1
		LIMIT 1
	`, eventID).Scan(
		&row.OrgID, &row.EventTime, &row.EventID, &row.Seq, &row.Shard,
		&row.ActorID, &row.ActorKind, &row.Action, &row.EntityKind, &row.EntityID,
		&row.Context, &row.Delta, &row.IdempotencyKey,
	)
	if err != nil {
		return nil, err
	}
	return row, nil
}
