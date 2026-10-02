package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBPlanSendsOneMailAtOpenAndOneAtClose opens a plan of three
// statements, runs each with --plan-id, and closes the plan, against the real
// ledger, the real SQL outbox, the real relay and audit consumer on the real
// Kafka broker, and the real SMTP server. Close returns after the consumer
// commits past the topic high-water marks. The alarm address receives two
// mails: the open mail and the close summary. The ledger contains the open
// row, a pending and an ok row per statement, and the close row, each with
// the plan ID.
func TestDBPlanSendsOneMailAtOpenAndOneAtClose(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-a")))
	statements := []string{
		"select 'plan-a' as step, 1 as n", "select 'plan-a' as step, 2 as n", "select 'plan-a' as step, 3 as n",
	}
	path := writePlanFile(t, append([]string{"-- three read-only statements", ""}, statements...)...)
	reason := "plan test a " + uuid.NewString()[:8]

	opened, err := openPlan(t, deps, path, reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	if len(opened.Statements) != 3 || opened.PlanID == "" || len(opened.SHA256) != 64 {
		t.Fatalf("plan open report = %+v, want three statements, a plan ID, and a SHA-256", opened)
	}
	for _, statement := range statements {
		result, err := runPlanned(t, deps, opened.PlanID, "  "+statement+"  ", reason)
		if err != nil || result.RowsReturned != 1 || result.PlanID != opened.PlanID {
			t.Fatalf("planned statement %q = %+v, %v; want one row under the plan", statement, result, err)
		}
	}
	closed, err := closePlan(t, deps, opened.PlanID, "postcheck a: three rows read")
	if err != nil {
		t.Fatalf("plan close: %v", err)
	}
	if closed.Expired || len(closed.Statements) != 3 || closed.Statements[2].Outcome != audit.OutcomeOK {
		t.Fatalf("plan close report = %+v, want three ok statements and no expiry", closed)
	}

	messages := requireMailCount(t, mail, 2)
	agentLine := "Agent: " + planService + " (session session-plan-a) for " + planAccountable
	openMail := mailWithSubject(t, messages, "plan "+opened.PlanID+" opened")
	if !strings.Contains(openMail.Text, agentLine) || !strings.Contains(openMail.Text, opened.SHA256) ||
		!strings.Contains(openMail.Text, statements[2]) {
		t.Fatalf("open mail text = %q, want the agent line, the SHA-256, and the statements", openMail.Text)
	}
	closeMail := mailWithSubject(t, messages, "plan "+opened.PlanID+" closed")
	if strings.Contains(closeMail.Subject, "expired") || !strings.Contains(closeMail.Text, "3. ok: "+statements[2]) ||
		!strings.Contains(closeMail.Text, "Statements run: 3") || !strings.Contains(closeMail.Text, "postcheck a: three rows read") {
		t.Fatalf("close mail = %+v, want each statement with its outcome and the postcheck", closeMail)
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{
		"ops.db_plan_open ok": 1, "ops.db_break_glass pending": 3, "ops.db_break_glass ok": 3, "ops.db_plan_close ok": 1,
	})
}

// TestDBPlanRefusesAStatementOutsideThePlan runs a statement that the open
// plan does not list. The command writes one refused row with the plan ID,
// mails the refusal, and returns an error before the pending row; the marker
// row that the statement would insert is absent. The close summary lists the
// refused statement.
func TestDBPlanRefusesAStatementOutsideThePlan(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-b")))
	table := "public.plan_marker_" + uuid.NewString()[:8]
	if _, err := pool.Exec(t.Context(), "CREATE TABLE "+table+" (marker text)"); err != nil {
		t.Fatalf("create the marker table: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), "DROP TABLE IF EXISTS "+table) })
	reason := "plan test b " + uuid.NewString()[:8]
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))

	unplanned := "insert into " + table + " values ('unplanned')"
	_, err = runPlanned(t, deps, opened.PlanID, unplanned, reason)
	if err == nil || !strings.Contains(err.Error(), "the plan does not list this statement") {
		t.Fatalf("err = %v, want the unplanned statement refused", err)
	}
	var markers int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("marker rows = %d (%v), want 0 because the statement did not run", markers, err)
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{"ops.db_plan_open ok": 1, "ops.db_break_glass refused": 1})
	refusal := mailWithSubject(t, requireMailCount(t, mail, 2), "statement refused under plan "+opened.PlanID)
	if !strings.Contains(refusal.Text, unplanned) || !strings.Contains(refusal.Text, "does not list") {
		t.Fatalf("refusal mail text = %q, want the statement and the reason", refusal.Text)
	}

	closed, err := closePlan(t, deps, opened.PlanID, "postcheck b")
	if err != nil || len(closed.Statements) != 1 || closed.Statements[0].Outcome != audit.OutcomeRefused {
		t.Fatalf("plan close = %+v, %v; want the refused statement in the summary", closed, err)
	}
	summary := mailWithSubject(t, requireMailCount(t, mail, 3), "plan "+opened.PlanID+" closed")
	if !strings.Contains(summary.Text, "1. refused: "+unplanned) || !strings.Contains(summary.Text, "Statements refused: 1") {
		t.Fatalf("summary mail text = %q, want the refused statement", summary.Text)
	}
}

// TestDBPlanMailsAFailedStatement runs a planned statement that the server
// rejects. The command writes the error outcome row and mails the error.
func TestDBPlanMailsAFailedStatement(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-c"))
	reason := "plan test c " + uuid.NewString()[:8]
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1/0"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}

	_, err = runPlanned(t, deps, opened.PlanID, "select 1/0", reason)
	if err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("err = %v, want the server error", err)
	}
	failure := mailWithSubject(t, requireMailCount(t, mail, 2), "statement failed under plan "+opened.PlanID)
	if !strings.Contains(failure.Text, "division by zero") || !strings.Contains(failure.Text, "select 1/0") {
		t.Fatalf("failure mail text = %q, want the error and the statement", failure.Text)
	}
	rows := planRows(t, pool, opened.PlanID)
	kinds := planRowKinds(rows)
	if len(rows) != 3 || kinds["ops.db_break_glass pending"] != 1 || kinds["ops.db_break_glass error"] != 1 {
		t.Fatalf("plan rows = %v, want the open row, a pending row, and an error row", kinds)
	}
	if last := rows[2]; last.Error == nil || !strings.Contains(last.Error.Message, "division by zero") {
		t.Fatalf("error row = %+v, want the server error", last)
	}
}
