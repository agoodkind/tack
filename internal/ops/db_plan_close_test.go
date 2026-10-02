package ops

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const (
	// planOtherOperatorID and planOtherAccountable identify a second test
	// accountable operator, never a person.
	planOtherOperatorID  = "019ff315-bc5d-7a56-b12a-1a35f280c4de"
	planOtherAccountable = "other-accountable@example.test"
	// planOtherService is a second test agent service.
	planOtherService = "claude-plan-other-test"
	// planShortWait is the close wait of the test with no consumer running.
	planShortWait = "3s"
)

// planOtherPrincipalFlags returns, by name, the operator flags of two callers
// in session that a plan opened by planAgentFlags(session) refuses: another
// agent service for the same accountable operator, and the same agent
// service for another accountable operator.
func planOtherPrincipalFlags(session string) map[string][]string {
	return map[string][]string{
		"another agent service":        planAgentFlagsFor(planOtherService, session, testOperatorID, planAccountable),
		"another accountable operator": planAgentFlagsFor(planService, session, planOtherOperatorID, planOtherAccountable),
	}
}

// TestDBPlanCloseFailsWhileTheConsumerIsBehind relays the open row of a plan
// to a topic that no consumer reads, stops the relay, and closes the plan
// with a three-second wait. Close fails past the wait, mails that the
// summary is incomplete, and writes no close row; the operator outbox keeps
// every row that the stopped relay did not send.
func TestDBPlanCloseFailsWhileTheConsumerIsBehind(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	stopRelay := pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-g")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test g "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	waitForOutboxDrain(t, pool, filter)
	stopRelay()

	var sink bytes.Buffer
	input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck g", Wait: planShortWait}
	err = runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	if err == nil || !strings.Contains(err.Error(), "did not commit past the high-water marks") {
		t.Fatalf("plan close = %v, want the wait for the consumer to fail", err)
	}
	incomplete := mailWithSubject(t, requireMailCount(t, mail, 2), "summary of plan "+opened.PlanID+" is incomplete")
	if !strings.Contains(incomplete.Text, "no close row was written") {
		t.Fatalf("incomplete mail text = %q, want the open plan status", incomplete.Text)
	}
	if rows := opsoutbox.Events(t, pool, filter); len(rows) != 0 {
		t.Fatalf("plan rows in the operator outbox = %+v, want none: a close row would stay there", rows)
	}
}

// TestDBPlanRefusesACloseByAnotherPrincipal opens a plan as one agent
// session, then closes it in the same session as another agent service and
// as the same agent service for another accountable operator. Each close
// writes one refused close row, mails the refusal, and fails. The plan stays
// open, and a close by the opener then succeeds.
func TestDBPlanRefusesACloseByAnotherPrincipal(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	depsFor := func(flags []string) dbSQLDeps {
		return pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, flags))
	}
	opener := depsFor(planAgentFlags("session-plan-h"))
	opened, err := openPlan(t, opener, writePlanFile(t, "select 1"), "plan test h "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))

	for name, flags := range planOtherPrincipalFlags("session-plan-h") {
		if _, err := closePlan(t, depsFor(flags), opened.PlanID, "postcheck h"); err == nil ||
			!strings.Contains(err.Error(), "refused the close: "+dbPlanPrincipalRefusal) {
			t.Fatalf("close by %s = %v, want the close refused", name, err)
		}
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{"ops.db_plan_open ok": 1, "ops.db_plan_close refused": 2})
	if refusals := mailsWithSubject(requireMailCount(t, mail, 3), "close refused under plan "+opened.PlanID); len(refusals) != 2 {
		t.Fatalf("close refusal mails = %+v, want one per refused close", refusals)
	}

	if _, err := closePlan(t, opener, opened.PlanID, "postcheck h"); err != nil {
		t.Fatalf("close by the opener: %v", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 4), "plan "+opened.PlanID+" closed")
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{
		"ops.db_plan_open ok": 1, "ops.db_plan_close refused": 2, "ops.db_plan_close ok": 1,
	})
}

// TestDBPlanCloseWritesTheCloseRowBeforeTheSummaryMail opens a plan and
// closes it with the mail account on a closed local port. The close writes
// the ok close row and returns an error that states the undelivered summary
// mail. The mailbox has the open mail only.
func TestDBPlanCloseWritesTheCloseRowBeforeTheSummaryMail(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-l")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test l "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))

	undelivered := pipeline.configure(planDeps(t, pool, ledgerDSN, unreachableMsmtprc(t), planAgentFlags("session-plan-l")))
	_, err = closePlan(t, undelivered, opened.PlanID, "postcheck l")
	if err == nil || !strings.Contains(err.Error(), "close row is written, but the summary mail failed") ||
		!strings.Contains(err.Error(), "was not delivered") {
		t.Fatalf("plan close = %v, want the undelivered summary mail after the close row", err)
	}
	waitForPlanKinds(t, pool, opened.PlanID, map[string]int{"ops.db_plan_open ok": 1, "ops.db_plan_close ok": 1})
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
}

// waitForOutboxDrain polls public.ops_outbox until the relay has removed
// every row that filter selects.
func waitForOutboxDrain(t *testing.T, pool *pgxpool.Pool, filter opsoutbox.Filter) {
	t.Helper()
	deadline := clock.Now().Add(planPipelineDeadline)
	for len(opsoutbox.Events(t, pool, filter)) != 0 {
		if clock.Now().After(deadline) {
			t.Fatalf("the relay left rows of %s = %s in the operator outbox after %s", filter.Path, filter.Value, planPipelineDeadline)
		}
		time.Sleep(planPipelinePoll)
	}
}
