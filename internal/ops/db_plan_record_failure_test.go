package ops

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBPlanRefusalMailsWhenItsRowWriteFails opens a plan, then runs an
// unlisted statement and closes the plan as another accountable operator,
// both with the production outbox writer over a closed ledger pool. Each
// refused-row write fails. Each command still mails its refusal and returns
// an error that states the refusal and the record failure. The ledger keeps
// the open row only.
func TestDBPlanRefusalMailsWhenItsRowWriteFails(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-m")))
	reason := "plan test m " + uuid.NewString()[:8]
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	closedPool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool to close: %v", err)
	}
	closedPool.Close()

	statementDeps := deps
	statementDeps.outbox = audit.NewPoolOutbox(closedPool)
	_, err = runPlanned(t, statementDeps, opened.PlanID, "select 2", reason)
	if err == nil || !strings.Contains(err.Error(), "the plan does not list this statement") ||
		!strings.Contains(err.Error(), "record the break-glass statement (refused)") {
		t.Fatalf("refused statement = %v, want the refusal and the record failure", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 2), "statement refused under plan "+opened.PlanID)

	closeFlags := planOtherPrincipalFlags("session-plan-m")["another accountable operator"]
	closeDeps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, closeFlags))
	closeDeps.outbox = audit.NewPoolOutbox(closedPool)
	_, err = closePlan(t, closeDeps, opened.PlanID, "postcheck m")
	if err == nil || !strings.Contains(err.Error(), "refused the close: "+dbPlanPrincipalRefusal) ||
		!strings.Contains(err.Error(), "record "+string(audit.VerbOpsDBPlanClose)+" for plan "+opened.PlanID) {
		t.Fatalf("refused close = %v, want the refusal and the record failure", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 3), "close refused under plan "+opened.PlanID)
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{"ops.db_plan_open ok": 1})
}
