package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

// planDeadLetterYears is how far ahead the dead-letter test dates a plan row.
// The ledger has no audit.events partition that far ahead.
const planDeadLetterYears = 10

// TestDBPlanStatementWaitsForTheOpenRow opens a plan while the relay runs and
// no consumer reads the topic, waits until the relay has removed the open row
// from public.ops_outbox, starts the audit consumer, and runs a listed
// statement at once. At that moment the open row is in neither
// public.ops_outbox nor audit.events. The statement waits for the consumer to
// write the open row, runs, and sends no refusal mail.
func TestDBPlanStatementWaitsForTheOpenRow(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-i"))
	statement := "select 'plan-i' as step"
	reason := "plan test i " + uuid.NewString()[:8]
	opened, err := openPlan(t, deps, writePlanFile(t, statement), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	waitForOutboxDrain(t, pool, filter)
	pipeline.startConsumer(t, ledgerDSN)

	result, err := runPlanned(t, deps, opened.PlanID, statement, reason)
	if err != nil || result.RowsReturned != 1 {
		t.Fatalf("planned statement = %+v, %v; want one row and no refusal", result, err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
}

// TestDBPlanCloseFailsOnADeadLetteredPlanRow runs a listed statement while the
// relay is stopped, dates the ok row of the statement ten years ahead in
// public.ops_outbox, and starts the relay again. The audit consumer finds no
// audit.events partition for that date and writes the row to
// audit.events_dlq. Close then fails, mails that the summary is incomplete,
// and writes no close row.
func TestDBPlanCloseFailsOnADeadLetteredPlanRow(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	stopRelay := pipeline.startRelay(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM audit.events_dlq WHERE topic = $1`, pipeline.topic)
	})
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-j")))
	reason := "plan test j " + uuid.NewString()[:8]
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	waitForOutboxDrain(t, pool, filter)
	stopRelay()
	if _, err := runPlanned(t, deps, opened.PlanID, "select 1", reason); err != nil {
		t.Fatalf("planned statement: %v", err)
	}
	redatePlanOutcomeRow(t, pool, opened.PlanID)
	pipeline.startRelay(t, pool)

	_, err = closePlan(t, deps, opened.PlanID, "postcheck j")
	if err == nil || !strings.Contains(err.Error(), "audit.events_dlq has 1 rows of plan "+opened.PlanID) {
		t.Fatalf("plan close = %v, want the dead-lettered plan row to fail the close", err)
	}
	incomplete := mailWithSubject(t, requireMailCount(t, mail, 2), "summary of plan "+opened.PlanID+" is incomplete")
	if !strings.Contains(incomplete.Text, "audit.events_dlq") || !strings.Contains(incomplete.Text, "no close row was written") {
		t.Fatalf("incomplete mail text = %q, want the dead letter and the open plan status", incomplete.Text)
	}
	if kinds := planKinds(t, pool, opened.PlanID); kinds["ops.db_plan_close ok"] != 0 {
		t.Fatalf("rows of plan %s = %v, want no close row", opened.PlanID, kinds)
	}
}

// redatePlanOutcomeRow deletes the ok break-glass row of planID from
// public.ops_outbox and writes it again through the production outbox writer
// with an occurred_at planDeadLetterYears ahead.
func redatePlanOutcomeRow(t *testing.T, pool *pgxpool.Pool, planID string) {
	t.Helper()
	filter := opsoutbox.Filter{Verb: audit.VerbOpsDBBreakGlass, Path: []string{"extra", "plan_id"}, Value: planID}
	var outcome *audit.Event
	for _, row := range opsoutbox.Events(t, pool, filter) {
		if row.Outcome == audit.OutcomeOK {
			outcome = &row
		}
	}
	if outcome == nil {
		t.Fatalf("public.ops_outbox has no ok break-glass row of plan %s", planID)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM public.ops_outbox WHERE event_id = $1`, outcome.EventID); err != nil {
		t.Fatalf("delete the ok row of plan %s: %v", planID, err)
	}
	outcome.OccurredAt = clock.Now().UTC().AddDate(planDeadLetterYears, 0, 0)
	if err := audit.NewPoolOutbox(pool).WriteOutbox(t.Context(), *outcome); err != nil {
		t.Fatalf("write the redated ok row of plan %s: %v", planID, err)
	}
}
