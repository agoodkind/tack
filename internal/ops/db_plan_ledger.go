package ops

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
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

// dbPlanDeadLettersQuery reads the key, the failure, and the payload of each
// row of audit.events_dlq that the consumer received after $1 and with a
// payload that contains the plan ID text $2. The byte search only narrows the
// rows; readDBPlanLedger decodes each payload to decide whether it is a plan
// row. A dead-letter payload can be bytes that are not JSON.
const dbPlanDeadLettersQuery = `
SELECT topic || '/' || partition::text || '/' || "offset"::text || ': ' || error, payload
  FROM audit.events_dlq
 WHERE received_at >= $1 AND position(convert_to($2, 'UTF8') IN payload) > 0
 ORDER BY received_at, topic, partition, "offset"`

// dbPlanDeadLetter is one candidate row of dbPlanDeadLettersQuery.
type dbPlanDeadLetter struct {
	Line    string
	Payload []byte
}

// readDBPlanLedger reads the rows of planID from audit.events and the
// dead-letter rows of planID from audit.events_dlq on dsn, the ledger reader.
// It returns the decoded plan and one line per dead-letter row of planID. A
// failed read returns a *dbPlanReadError.
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
		return empty, nil, &dbPlanReadError{action: "read the dead letters of plan " + planID.String(), err: err}
	}
	candidates, err := pgx.CollectRows(letters, pgx.RowToStructByPos[dbPlanDeadLetter])
	if err != nil {
		return empty, nil, &dbPlanReadError{action: "read the dead letters of plan " + planID.String(), err: err}
	}
	var deadLetters []string
	for _, candidate := range candidates {
		if isDBPlanDeadLetter(candidate.Payload, planID) {
			deadLetters = append(deadLetters, candidate.Line)
		}
	}
	state, err := newDBPlanState(ctx, rows)
	return state, deadLetters, err
}

// isDBPlanDeadLetter reports whether payload, a dead-letter payload that
// contains the text of planID, is a row of planID. The relay sends each event
// to the audit topic as the JSON of an [audit.Event], and the consumer and
// the dead-letter replay decode it with [json.Unmarshal]. A payload that
// decodes is a plan row when its verb is a plan verb and its extra.plan_id is
// planID. A payload that does not decode counts as a plan row. A plan verb
// with an extra that does not decode also counts as a plan row. Close then
// fails closed on a row that it cannot rule out.
func isDBPlanDeadLetter(payload []byte, planID uuid.UUID) bool {
	var event audit.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return true
	}
	if !slices.Contains(dbPlanVerbs(), event.Verb) {
		return false
	}
	var extra struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		return true
	}
	return extra.PlanID == planID.String()
}
