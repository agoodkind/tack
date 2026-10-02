package ops

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

// TestDBPlanRefusesAStatementUnderAnUnverifiablePlan opens a plan that lists
// an insert into a marker table, then writes a second open row of that plan
// with an extra payload that does not decode. The planned statement is
// refused: the command writes one refused row, mails the refusal, inserts no
// marker row, and returns an error.
func TestDBPlanRefusesAStatementUnderAnUnverifiablePlan(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	table := "public.plan_marker_" + uuid.NewString()[:8]
	if _, err := pool.Exec(t.Context(), "CREATE TABLE "+table+" (marker text)"); err != nil {
		t.Fatalf("create the marker table: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), "DROP TABLE IF EXISTS "+table) })
	planned := "insert into " + table + " values ('planned')"
	reason := "plan test r " + uuid.NewString()[:8]
	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-r"))
	opened, err := openPlan(t, deps, writePlanFile(t, planned), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	writeUndecodablePlanOpenRow(t, pool, opened.PlanID)

	_, err = runPlanned(t, deps, opened.PlanID, planned, reason)
	if err == nil || !strings.Contains(err.Error(), dbPlanUnverifiedRefusal) {
		t.Fatalf("planned statement = %v, want it refused for the unverifiable plan", err)
	}
	refusal := mailWithSubject(t, requireMailCount(t, mail, 2), "statement refused under plan "+opened.PlanID)
	if !strings.Contains(refusal.Text, dbPlanUnverifiedRefusal) || !strings.Contains(refusal.Text, planned) {
		t.Fatalf("refusal mail text = %q, want the unverifiable plan and the statement", refusal.Text)
	}
	kinds := planRowKinds(planRows(t, pool, opened.PlanID))
	if !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 2, "ops.db_break_glass refused": 1}) {
		t.Fatalf("plan rows = %v, want the two open rows and one refused row", kinds)
	}
	var markers int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("marker rows = %d (%v), want 0: the statement must not run", markers, err)
	}
}

// writeUndecodablePlanOpenRow copies the open row of planID and writes the
// copy to public.ops_outbox through the production outbox writer. The copy
// has a new event ID, an occurred_at one second before the open row, and an
// extra payload with the number 1 in the statements field. The plan state
// decoder reads plan rows oldest first and reads the copy first. A number
// does not decode into the list of statements.
func writeUndecodablePlanOpenRow(t *testing.T, pool *pgxpool.Pool, planID string) {
	t.Helper()
	filter := opsoutbox.Filter{Verb: audit.VerbOpsDBPlanOpen, Path: []string{"extra", "plan_id"}, Value: planID}
	rows := opsoutbox.Events(t, pool, filter)
	if len(rows) != 1 {
		t.Fatalf("open rows of plan %s in public.ops_outbox = %+v, want one", planID, rows)
	}
	undecodable := rows[0]
	undecodable.EventID = uuid.Must(uuid.NewV7())
	undecodable.OccurredAt = undecodable.OccurredAt.Add(-time.Second)
	undecodable.Extra = []byte(`{"plan_id":"` + planID + `","statements":1}`)
	if err := audit.NewPoolOutbox(pool).WriteOutbox(t.Context(), undecodable); err != nil {
		t.Fatalf("write the undecodable open row of plan %s: %v", planID, err)
	}
}
