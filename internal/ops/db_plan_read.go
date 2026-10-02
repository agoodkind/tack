package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// dbPlanClockSlack is subtracted from the time in the plan ID to bound the
	// row search. It covers a difference between the command host clock and
	// the database clock.
	dbPlanClockSlack = 10 * time.Minute
	// dbPlanOpenRowWait bounds the wait of a planned statement for the open
	// row of its plan in public.ops_outbox or audit.events. The relay deletes
	// the outbox row after the broker accepts it, and the consumer writes the
	// audit.events row later.
	dbPlanOpenRowWait = 30 * time.Second
)

// dbPlanRowsQuery reads the open row, the statement rows, and the close row of
// one plan from audit.events and from public.ops_outbox. The relay deletes an
// outbox row after the broker accepts it. The consumer can write the same
// event to audit.events before that delete. The caller keeps the first row of
// each event ID.
const dbPlanRowsQuery = dbPlanEventsSelect + `
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

// openDBPlanPool opens a pool on dsn to read the rows of planID.
func openDBPlanPool(ctx context.Context, dsn string, planID uuid.UUID) (*pgxpool.Pool, error) {
	pool, err := postgres.NewPool(ctx, dsn, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.pool_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the database to read plan %s: %w", planID, err)
	}
	return pool, nil
}

// dbPlanSince returns the earliest time a row of planID can carry: the time
// in the plan ID less dbPlanClockSlack.
func dbPlanSince(planID uuid.UUID) time.Time {
	seconds, nanoseconds := planID.Time().UnixTime()
	return time.Unix(seconds, nanoseconds).UTC().Add(-dbPlanClockSlack)
}

// queryDBPlanRows runs query, dbPlanRowsQuery or dbPlanLedgerRowsQuery, on
// pool and returns the rows of planID in time order, one per event ID. Plan
// open issues only version 7 UUIDs. Any other plan ID returns no rows.
func queryDBPlanRows(ctx context.Context, pool *pgxpool.Pool, query string, planID uuid.UUID) ([]dbPlanRow, error) {
	if planID.Version() != 7 {
		return nil, nil
	}
	verbs := []string{string(audit.VerbOpsDBPlanOpen), string(audit.VerbOpsDBBreakGlass), string(audit.VerbOpsDBPlanClose)}
	rows, err := pool.Query(ctx, query, audit.SystemOrgID(), verbs, dbPlanSince(planID), planID.String())
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

// awaitDBPlanOpenRow reads the rows of planID from audit.events and
// public.ops_outbox on dsn every dbPlanProjectionPoll until they contain the
// open row, and returns the decoded plan. After dbPlanOpenRowWait it returns
// the plan without an open row. A read failure or a plan ID that plan open
// does not issue returns at once.
func awaitDBPlanOpenRow(ctx context.Context, dsn string, planID uuid.UUID) (dbPlanState, error) {
	empty := dbPlanState{open: nil, closed: false, attempts: nil}
	if planID.Version() != 7 {
		return empty, nil
	}
	pool, err := openDBPlanPool(ctx, dsn, planID)
	if err != nil {
		return empty, err
	}
	defer pool.Close()
	waitCtx, cancel := context.WithTimeout(ctx, dbPlanOpenRowWait)
	defer cancel()
	ticker := time.NewTicker(dbPlanProjectionPoll)
	defer ticker.Stop()
	for {
		rows, err := queryDBPlanRows(ctx, pool, dbPlanRowsQuery, planID)
		if err != nil {
			return empty, err
		}
		state, err := newDBPlanState(ctx, rows)
		if err != nil || state.open != nil {
			return state, err
		}
		select {
		case <-waitCtx.Done():
			return state, nil
		case <-ticker.C:
		}
	}
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
