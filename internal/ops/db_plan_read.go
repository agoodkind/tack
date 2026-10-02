package ops

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// dbPlanClockSlack is subtracted from the time in the plan ID to bound the
// row search. It covers a difference between the command host clock and the
// database clock.
const dbPlanClockSlack = 10 * time.Minute

// dbPlanRowsQuery reads the open row, the statement rows, and the close row of
// one plan from audit.events and from public.ops_outbox. The relay deletes an
// outbox row after the broker accepts it. The consumer can write the same
// event to audit.events before that delete. The caller keeps the first row of
// each event ID. The audit.events half uses the (org_id, action, event_time)
// index and reads only the partitions after the plan ID time.
const dbPlanRowsQuery = `
SELECT event_id::text, action, coalesce(outcome, ''), coalesce(extra, '{}'::jsonb)::text,
       coalesce(error->>'message', ''), event_time
  FROM audit.events
 WHERE org_id = $1 AND action = ANY($2::text[]) AND event_time >= $3
   AND extra->>'plan_id' = $4
UNION ALL
SELECT event_id::text, event->>'verb', coalesce(event->>'outcome', ''),
       coalesce(event->'extra', '{}'::jsonb)::text, coalesce(event->'error'->>'message', ''),
       (event->>'occurred_at')::timestamptz
  FROM public.ops_outbox
 WHERE created_at >= $3 AND event->>'verb' = ANY($2::text[])
   AND event->'extra'->>'plan_id' = $4
 ORDER BY 6, 1`

// dbPlanRow is one ledger row of a plan.
type dbPlanRow struct {
	EventID    string
	Verb       string
	Outcome    string
	Extra      string
	Error      string
	OccurredAt time.Time
}

// readDBPlanRows returns the rows of planID in time order, one per event ID.
// Plan open issues only version 7 UUIDs. Any other plan ID returns no rows.
func readDBPlanRows(ctx context.Context, dsn string, planID uuid.UUID) ([]dbPlanRow, error) {
	if planID.Version() != 7 {
		return nil, nil
	}
	seconds, nanoseconds := planID.Time().UnixTime()
	since := time.Unix(seconds, nanoseconds).UTC().Add(-dbPlanClockSlack)
	verbs := []string{string(audit.VerbOpsDBPlanOpen), string(audit.VerbOpsDBBreakGlass), string(audit.VerbOpsDBPlanClose)}
	pool, err := postgres.NewPool(ctx, dsn, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.pool_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the database to read plan %s: %w", planID, err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, dbPlanRowsQuery, audit.SystemOrgID(), verbs, since, planID.String())
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the rows of plan %s: %w", planID, err)
	}
	collected, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dbPlanRow])
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the rows of plan %s: %w", planID, err)
	}
	return uniqueDBPlanRows(collected), nil
}

// readDBPlanState reads and decodes the rows of planID.
func readDBPlanState(ctx context.Context, dsn string, planID uuid.UUID) (dbPlanState, error) {
	rows, err := readDBPlanRows(ctx, dsn, planID)
	if err != nil {
		return dbPlanState{open: nil, closed: false, attempts: nil}, err
	}
	return newDBPlanState(ctx, rows)
}

// mergeDBPlanRows returns the rows of two reads of one plan, one per event
// ID, in time order and then event ID order.
func mergeDBPlanRows(first, second []dbPlanRow) []dbPlanRow {
	merged := uniqueDBPlanRows(slices.Concat(first, second))
	slices.SortStableFunc(merged, func(left, right dbPlanRow) int {
		if order := left.OccurredAt.Compare(right.OccurredAt); order != 0 {
			return order
		}
		return strings.Compare(left.EventID, right.EventID)
	})
	return merged
}

// uniqueDBPlanRows keeps the first row of each event ID in rows.
func uniqueDBPlanRows(rows []dbPlanRow) []dbPlanRow {
	seen := make(map[string]bool, len(rows))
	unique := make([]dbPlanRow, 0, len(rows))
	for _, row := range rows {
		if seen[row.EventID] {
			continue
		}
		seen[row.EventID] = true
		unique = append(unique, row)
	}
	return unique
}
