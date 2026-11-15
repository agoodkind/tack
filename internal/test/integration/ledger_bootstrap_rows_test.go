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

// waitForBootstrapRows polls audit.events until each of the three commands has
// a pending intent row and a completed result row with an op id and a step
// row, and fails with the rows it observed when the deadline passes first.
// The producer picks the Kafka partition of each event from its own event ID.
// The consumer can project the result row of a run before its intent and
// step rows.
func waitForBootstrapRows(t *testing.T, admin *pgxpool.Pool) []bootstrapLedgerRow {
	t.Helper()
	verbs := []audit.Verb{
		audit.VerbOpsProvision,
		audit.VerbOpsLedgerNodePrepare,
		audit.VerbOpsLedgerNodeWait,
	}

	names := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		names = append(names, string(verb))
	}

	var observed []bootstrapLedgerRow
	var lastErr error
	projected := waitFor(
		t,
		bootstrapRowsDeadline,
		func() bool {
			rows, err := admin.Query(t.Context(), bootstrapRowsQuery, names)
			if err != nil {
				lastErr = err
				return false
			}

			observed, lastErr = pgx.CollectRows(rows, pgx.RowToStructByPos[bootstrapLedgerRow])
			if lastErr != nil {
				return false
			}

			for _, verb := range verbs {
				if !bootstrapRunProjected(bootstrapRowsOfVerb(observed, verb)) {
					return false
				}
			}

			return true
		},
	)
	if !projected {
		t.Fatalf(
			"audit.events lacks a completed result row of each bootstrap command after %s (last error %v): %+v",
			bootstrapRowsDeadline,
			lastErr,
			observed,
		)
	}

	return observed
}

// bootstrapRunProjected reports whether rows contain the pending intent row
// and the completed result row of a run, both with an op id, and at least
// one step row without an op id.
func bootstrapRunProjected(rows []bootstrapLedgerRow) bool {
	intent := slices.ContainsFunc(
		rows,
		func(row bootstrapLedgerRow) bool {
			return row.Outcome == string(audit.OutcomeAttempted) && row.OpID != ""
		},
	)
	step := slices.ContainsFunc(
		rows,
		func(row bootstrapLedgerRow) bool {
			return row.OpID == "" && row.Outcome == string(audit.OutcomeAttempted)
		},
	)

	return intent && step
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

// requireBootstrapRun requires one external run of verb: a pending intent row
// and a completed result row that share one op id, and at least one step row
// without an op id. Every step row must be pending or completed.
func requireBootstrapRun(t *testing.T, rows []bootstrapLedgerRow, verb audit.Verb) {
	t.Helper()
	var run, steps []bootstrapLedgerRow

	for _, row := range bootstrapRowsOfVerb(rows, verb) {
		if row.OpID == "" {
			steps = append(steps, row)
		} else {
			run = append(run, row)
		}
	}

	if len(run) != 1 || run[0].Outcome != string(audit.OutcomeAttempted) {
		t.Fatalf("%s rows = %+v, want one attempt with an op id", verb, rows)
	}

	requireBootstrapOpID(t, verb, run[0])

	if len(steps) == 0 {
		t.Fatalf("%s did not record a step row", verb)
	}

	for _, step := range steps {
		if step.Outcome != string(audit.OutcomeAttempted) {
			t.Fatalf(
				"%s step row outcome = %s, want %s",
				verb,
				step.Outcome,
				audit.OutcomeAttempted,
			)
		}
	}
}

// requireBootstrapOpID requires the row's extra payload to carry a UUID op id.
func requireBootstrapOpID(t *testing.T, verb audit.Verb, row bootstrapLedgerRow) {
	t.Helper()
	if _, err := uuid.Parse(row.OpID); err != nil {
		t.Fatalf("%s row op id = %q, want a UUID: %v", verb, row.OpID, err)
	}
}
