package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dbPlanEventsSelect reads the open row, the statement rows, and the close
// row of one plan from audit.events. It uses the (org_id, action, event_time)
// index and reads only the partitions after the plan ID time.
const dbPlanEventsSelect = `
SELECT event_id::text, action, coalesce(outcome, ''), coalesce(extra, '{}'::jsonb)::text,
       coalesce(error->>'message', ''), event_time
  FROM audit.events
 WHERE org_id = $1 AND action = ANY($2::text[]) AND event_time >= $3
   AND extra->>'plan_id' = $4`

// dbPlanLedgerRowsQuery reads the rows of one plan from audit.events in time
// order.
const dbPlanLedgerRowsQuery = dbPlanEventsSelect + `
 ORDER BY 6, 1`

// dbPlanDeadLettersQuery reads the key and the failure of each row of
// audit.events_dlq that the consumer received after $1 and with a payload
// that contains the plan ID $2. The query searches the payload bytes for the
// plan ID text. A dead-letter payload can be bytes that are not JSON.
const dbPlanDeadLettersQuery = `
SELECT topic || '/' || partition::text || '/' || "offset"::text || ': ' || error
  FROM audit.events_dlq
 WHERE received_at >= $1 AND position(convert_to($2, 'UTF8') IN payload) > 0
 ORDER BY received_at, topic, partition, "offset"`

// readDBPlanLedger reads the rows of planID from audit.events and the
// dead-letter rows of planID from audit.events_dlq on dsn, the ledger reader.
// It returns the decoded plan and one line per dead-letter row.
func readDBPlanLedger(ctx context.Context, dsn string, planID uuid.UUID) (dbPlanState, []string, error) {
	empty := dbPlanState{open: nil, closed: false, attempts: nil}
	pool, err := openDBPlanPool(ctx, dsn, planID)
	if err != nil {
		return empty, nil, err
	}
	defer pool.Close()
	rows, err := queryDBPlanRows(ctx, pool, dbPlanLedgerRowsQuery, planID)
	if err != nil {
		return empty, nil, err
	}
	letters, err := pool.Query(ctx, dbPlanDeadLettersQuery, dbPlanSince(planID), planID.String())
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.dead_letters_failed", slog.String("err", err.Error()))
		return empty, nil, fmt.Errorf("read the dead letters of plan %s: %w", planID, err)
	}
	deadLetters, err := pgx.CollectRows(letters, pgx.RowTo[string])
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.dead_letters_failed", slog.String("err", err.Error()))
		return empty, nil, fmt.Errorf("read the dead letters of plan %s: %w", planID, err)
	}
	state, err := newDBPlanState(ctx, rows)
	return state, deadLetters, err
}
