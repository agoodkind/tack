package ops

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// planIDPattern matches the plan ID in the undelivered open mail error.
var planIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TestDBPlanUndeliveredOpenMailBlocksThePlan opens a plan with the mail
// account on a closed local port. The command fails, the outbox has no open
// row, and a later statement under that plan ID is refused with one refused
// row and one mail.
func TestDBPlanUndeliveredOpenMailBlocksThePlan(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	flags := planAgentFlags("session-plan-d")
	reason := "plan test d " + uuid.NewString()[:8]

	undelivered := planDeps(t, pool, ledgerDSN, unreachableMsmtprc(t), flags)
	_, err := openPlan(t, undelivered, writePlanFile(t, "select 1"), reason, "1h")
	if err == nil || !strings.Contains(err.Error(), "was not delivered") {
		t.Fatalf("err = %v, want the undelivered open mail to fail the command", err)
	}
	planID := planIDPattern.FindString(err.Error())
	if planID == "" {
		t.Fatalf("err = %v, want the plan ID in the error", err)
	}
	if rows := planRows(t, pool, planID); len(rows) != 0 {
		t.Fatalf("plan rows = %+v, want no open row after the undelivered mail", rows)
	}

	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, flags)
	_, err = runPlanned(t, deps, planID, "select 1", reason)
	if err == nil || !strings.Contains(err.Error(), "no open row") {
		t.Fatalf("err = %v, want the statement refused for the missing open row", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "statement refused under plan "+planID)
	rows := planRows(t, pool, planID)
	if len(rows) != 1 || rows[0].Outcome != audit.OutcomeRefused || rows[0].Error == nil ||
		!strings.Contains(rows[0].Error.Message, "no open row") {
		t.Fatalf("plan rows = %+v, want one refused row for the refused statement", rows)
	}
}

// TestDBPlanRefusesAnotherPrincipal runs a listed statement in the session
// that opened the plan, once as another agent service and once as the same
// agent service for another accountable operator. Each run writes one refused
// row, mails the refusal, and runs no statement.
func TestDBPlanRefusesAnotherPrincipal(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	reason := "plan test e " + uuid.NewString()[:8]
	opener := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-e"))
	opened, err := openPlan(t, opener, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}

	for name, flags := range planOtherPrincipalFlags("session-plan-e") {
		other := planDeps(t, pool, ledgerDSN, mail.Msmtprc, flags)
		if _, err := runPlanned(t, other, opened.PlanID, "select 1", reason); err == nil ||
			!strings.Contains(err.Error(), dbPlanPrincipalRefusal) {
			t.Fatalf("statement by %s = %v, want it refused", name, err)
		}
	}
	refusals := mailsWithSubject(requireMailCount(t, mail, 3), "statement refused under plan "+opened.PlanID)
	if len(refusals) != 2 {
		t.Fatalf("statement refusal mails = %+v, want one per refused caller", refusals)
	}
	kinds := planRowKinds(planRows(t, pool, opened.PlanID))
	if len(kinds) != 2 || kinds["ops.db_plan_open ok"] != 1 || kinds["ops.db_break_glass refused"] != 2 {
		t.Fatalf("plan rows = %v, want the open row and two refused rows", kinds)
	}
}

// TestDBPlanRefusesAnExpiredPlan opens a plan that expires after one second,
// waits past the expiry, and runs a listed statement. The command writes a
// refused row and mails the refusal. Plan close then mails a summary marked
// expired that lists the refused statement, and writes the close row.
func TestDBPlanRefusesAnExpiredPlan(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	reason := "plan test f " + uuid.NewString()[:8]
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-f")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), reason, "1s")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	time.Sleep(opened.ExpiresAt.Sub(clock.Now()) + 200*time.Millisecond)

	_, err = runPlanned(t, deps, opened.PlanID, "select 1", reason)
	if err == nil || !strings.Contains(err.Error(), "the plan expired at") {
		t.Fatalf("err = %v, want the expired plan refused", err)
	}
	closed, err := closePlan(t, deps, opened.PlanID, "postcheck f")
	if err != nil || !closed.Expired || len(closed.Statements) != 1 || closed.Statements[0].Outcome != audit.OutcomeRefused {
		t.Fatalf("plan close = %+v, %v; want an expired plan with one refused statement", closed, err)
	}
	messages := requireMailCount(t, mail, 3)
	mailWithSubject(t, messages, "statement refused under plan "+opened.PlanID)
	summary := mailWithSubject(t, messages, "plan "+opened.PlanID+" closed after it expired")
	if !strings.Contains(summary.Text, "Status: expired at") || !strings.Contains(summary.Text, "1. refused: select 1") {
		t.Fatalf("summary mail text = %q, want the expired status and the refused statement", summary.Text)
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{
		"ops.db_plan_open ok": 1, "ops.db_break_glass refused": 1, "ops.db_plan_close ok": 1,
	})
}
