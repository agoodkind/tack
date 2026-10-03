package ops

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

// TestDBPlanRefusesACloseOfAnUnverifiablePlan opens a plan, writes a second
// open row of that plan with an extra payload that does not decode, and only
// then starts the relay. The audit consumer records both rows in
// audit.events. Plan close reads the plan rows from the ledger and refuses
// the close: the command writes one refused close row, mails the refusal,
// writes no close row with outcome ok, and returns an error.
func TestDBPlanRefusesACloseOfAnUnverifiablePlan(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-s")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test s "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	writeUndecodablePlanOpenRow(t, pool, opened.PlanID)
	pipeline.startRelay(t, pool)

	_, err = closePlan(t, deps, opened.PlanID, "postcheck s")
	if err == nil || !strings.Contains(err.Error(), "refused the close: "+dbPlanUnverifiedRefusal) {
		t.Fatalf("plan close = %v, want the close refused for the unverifiable plan", err)
	}
	refusal := mailWithSubject(t, requireMailCount(t, mail, 2), "close refused under plan "+opened.PlanID)
	if !strings.Contains(refusal.Text, dbPlanUnverifiedRefusal) {
		t.Fatalf("close refusal mail text = %q, want the unverifiable plan", refusal.Text)
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{"ops.db_plan_open ok": 2, "ops.db_plan_close refused": 1})
}
