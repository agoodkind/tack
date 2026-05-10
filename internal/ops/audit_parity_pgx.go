package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/telemetry"
)

const parityCountsSQL = `
WITH l AS (
    SELECT event_id, actor_id, action, entity_kind, entity_id, context::text AS context_text
      FROM audit.events
     WHERE event_time >= $1 AND event_time < $2
),
v AS (
    SELECT event_id, actor_id, action, entity_kind, entity_id, context::text AS context_text
      FROM audit.events_v2
     WHERE event_time >= $1 AND event_time < $2
)
SELECT
    COUNT(*) FILTER (WHERE l.event_id IS NOT NULL AND v.event_id IS NOT NULL
                       AND l.actor_id     IS NOT DISTINCT FROM v.actor_id
                       AND l.action       IS NOT DISTINCT FROM v.action
                       AND l.entity_kind  IS NOT DISTINCT FROM v.entity_kind
                       AND l.entity_id    IS NOT DISTINCT FROM v.entity_id
                       AND l.context_text IS NOT DISTINCT FROM v.context_text) AS matched,
    COUNT(*) FILTER (WHERE l.event_id IS NOT NULL AND v.event_id IS NULL)       AS only_legacy,
    COUNT(*) FILTER (WHERE l.event_id IS NULL     AND v.event_id IS NOT NULL)   AS only_v2,
    COUNT(*) FILTER (WHERE l.event_id IS NOT NULL AND v.event_id IS NOT NULL
                       AND (l.actor_id     IS DISTINCT FROM v.actor_id
                         OR l.action       IS DISTINCT FROM v.action
                         OR l.entity_kind  IS DISTINCT FROM v.entity_kind
                         OR l.entity_id    IS DISTINCT FROM v.entity_id
                         OR l.context_text IS DISTINCT FROM v.context_text))   AS content_diff
  FROM l FULL OUTER JOIN v ON l.event_id = v.event_id;
`

const parityExamplesSQL = `
WITH l AS (
    SELECT event_id, actor_id, action, entity_kind, entity_id, context::text AS context_text
      FROM audit.events
     WHERE event_time >= $1 AND event_time < $2
),
v AS (
    SELECT event_id, actor_id, action, entity_kind, entity_id, context::text AS context_text
      FROM audit.events_v2
     WHERE event_time >= $1 AND event_time < $2
)
SELECT
    l.event_id,
    l.actor_id, v.actor_id,
    l.action,   v.action,
    l.entity_kind, v.entity_kind,
    l.entity_id,   v.entity_id,
    l.context_text, v.context_text
  FROM l JOIN v ON l.event_id = v.event_id
 WHERE l.actor_id     IS DISTINCT FROM v.actor_id
    OR l.action       IS DISTINCT FROM v.action
    OR l.entity_kind  IS DISTINCT FROM v.entity_kind
    OR l.entity_id    IS DISTINCT FROM v.entity_id
    OR l.context_text IS DISTINCT FROM v.context_text
 ORDER BY l.event_id
 LIMIT $3;
`

type pgxParityScanner struct {
	pool *pgxpool.Pool
}

func (s pgxParityScanner) ScanCounts(ctx context.Context, from time.Time, to time.Time) (*parityCounts, error) {
	row := s.pool.QueryRow(ctx, parityCountsSQL, from, to)
	c := &parityCounts{Matched: 0, OnlyLegacy: 0, OnlyV2: 0, ContentDiff: 0}
	err := row.Scan(&c.Matched, &c.OnlyLegacy, &c.OnlyV2, &c.ContentDiff)
	if err != nil {
		slog.ErrorContext(ctx, "audit.parity.counts_query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity counts: %w", err)
	}
	return c, nil
}

func (s pgxParityScanner) ScanExamples(ctx context.Context, from time.Time, to time.Time, limit int) ([]parityExample, error) {
	rows, err := s.pool.Query(ctx, parityExamplesSQL, from, to, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.parity.examples_query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity examples: %w", err)
	}
	defer rows.Close()
	out := make([]parityExample, 0, limit)
	for rows.Next() {
		var (
			eventID                  uuid.UUID
			lActor, vActor           uuid.UUID
			lAction, vAction         string
			lEntityKind, vEntityKind string
			lEntityID, vEntityID     uuid.UUID
			lContext, vContext       string
		)
		err := rows.Scan(&eventID,
			&lActor, &vActor,
			&lAction, &vAction,
			&lEntityKind, &vEntityKind,
			&lEntityID, &vEntityID,
			&lContext, &vContext,
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.parity.example_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit parity example scan: %w", err)
		}
		out = append(out, parityExample{
			EventID: eventID,
			Diffs: collectFieldDiffs(
				lActor, vActor, lAction, vAction,
				lEntityKind, vEntityKind, lEntityID, vEntityID,
				lContext, vContext,
			),
		})
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.parity.example_rows_iter_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity example rows: %w", err)
	}
	return out, nil
}

func collectFieldDiffs(
	lActor uuid.UUID, vActor uuid.UUID,
	lAction string, vAction string,
	lEntityKind string, vEntityKind string,
	lEntityID uuid.UUID, vEntityID uuid.UUID,
	lContext string, vContext string,
) []parityFieldDiff {
	diffs := make([]parityFieldDiff, 0, 5)
	if lActor != vActor {
		diffs = append(diffs, parityFieldDiff{Field: "actor_id", Legacy: lActor.String(), V2: vActor.String()})
	}
	if lAction != vAction {
		diffs = append(diffs, parityFieldDiff{Field: "action", Legacy: lAction, V2: vAction})
	}
	if lEntityKind != vEntityKind {
		diffs = append(diffs, parityFieldDiff{Field: "entity_kind", Legacy: lEntityKind, V2: vEntityKind})
	}
	if lEntityID != vEntityID {
		diffs = append(diffs, parityFieldDiff{Field: "entity_id", Legacy: lEntityID.String(), V2: vEntityID.String()})
	}
	if lContext != vContext {
		diffs = append(diffs, parityFieldDiff{Field: "context", Legacy: lContext, V2: vContext})
	}
	return diffs
}

func openParityPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "audit.parity.pool_config_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity pool config: %w", err)
	}
	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "audit.parity.pool_open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity pool open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		slog.ErrorContext(ctx, "audit.parity.pool_ping_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit parity pool ping: %w", err)
	}
	return pool, nil
}
