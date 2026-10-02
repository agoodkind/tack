package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

const (
	// bootstrapRowsDeadline bounds the wait for the relay and the consumer to
	// project the recorded events into audit.events.
	bootstrapRowsDeadline = 90 * time.Second
	// bootstrapRowCount is the provision pair, the node-prepare pair, and the
	// one node-wait read row.
	bootstrapRowCount = 5
	// bootstrapRowsQuery reads the ledger rows of the three commands.
	bootstrapRowsQuery = `SELECT action, COALESCE(outcome, ''), COALESCE(extra->>'op_id', '')
		FROM audit.events WHERE action = ANY($1::text[]) ORDER BY event_time, seq`
)

// bootstrapLedgerRow is one audit.events row of a bootstrap command.
type bootstrapLedgerRow struct {
	Action  string
	Outcome string
	OpID    string
}

// waitForBootstrapRows polls audit.events until the three commands' rows are
// projected, and fails with the rows it observed when the deadline passes
// first.
func waitForBootstrapRows(t *testing.T, admin *pgxpool.Pool) []bootstrapLedgerRow {
	t.Helper()
	verbs := []string{
		string(audit.VerbOpsProvision), string(audit.VerbOpsLedgerNodePrepare), string(audit.VerbOpsLedgerNodeWait),
	}
	var observed []bootstrapLedgerRow
	var lastErr error
	projected := waitFor(t, bootstrapRowsDeadline, func() bool {
		rows, err := admin.Query(t.Context(), bootstrapRowsQuery, verbs)
		if err != nil {
			lastErr = err
			return false
		}
		observed, lastErr = pgx.CollectRows(rows, pgx.RowToStructByPos[bootstrapLedgerRow])
		return lastErr == nil && len(observed) >= bootstrapRowCount
	})
	if !projected || len(observed) != bootstrapRowCount {
		t.Fatalf("audit.events contains %d bootstrap rows after %s, want %d (last error %v): %+v",
			len(observed), bootstrapRowsDeadline, bootstrapRowCount, lastErr, observed)
	}
	return observed
}

// bootstrapRowsOfVerb returns the rows that record verb.
func bootstrapRowsOfVerb(rows []bootstrapLedgerRow, verb audit.Verb) []bootstrapLedgerRow {
	var matched []bootstrapLedgerRow
	for _, row := range rows {
		if row.Action == string(verb) {
			matched = append(matched, row)
		}
	}
	return matched
}

// requireBootstrapIntentAndOutcome requires one pending intent row and one ok
// outcome row of verb that share one op id.
func requireBootstrapIntentAndOutcome(t *testing.T, rows []bootstrapLedgerRow, verb audit.Verb) {
	t.Helper()
	matched := bootstrapRowsOfVerb(rows, verb)
	if len(matched) != 2 {
		t.Fatalf("%s rows = %+v, want an intent row and an outcome row", verb, matched)
	}
	outcomes := []string{matched[0].Outcome, matched[1].Outcome}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []string{string(audit.OutcomeOK), string(audit.OutcomePending)}) {
		t.Fatalf("%s outcomes = %v, want one %s and one %s", verb, outcomes, audit.OutcomePending, audit.OutcomeOK)
	}
	requireBootstrapOpID(t, verb, matched[0])
	if matched[0].OpID != matched[1].OpID {
		t.Fatalf("%s op ids = %s and %s, want one op id on both rows", verb, matched[0].OpID, matched[1].OpID)
	}
}

// requireBootstrapReadRow requires exactly one ok row of verb with an op id.
func requireBootstrapReadRow(t *testing.T, rows []bootstrapLedgerRow, verb audit.Verb) {
	t.Helper()
	matched := bootstrapRowsOfVerb(rows, verb)
	if len(matched) != 1 || matched[0].Outcome != string(audit.OutcomeOK) {
		t.Fatalf("%s rows = %+v, want one %s read row", verb, matched, audit.OutcomeOK)
	}
	requireBootstrapOpID(t, verb, matched[0])
}

// requireBootstrapOpID requires the row's extra payload to carry a UUID op id.
func requireBootstrapOpID(t *testing.T, verb audit.Verb, row bootstrapLedgerRow) {
	t.Helper()
	if _, err := uuid.Parse(row.OpID); err != nil {
		t.Fatalf("%s row op id = %q, want a UUID: %v", verb, row.OpID, err)
	}
}
