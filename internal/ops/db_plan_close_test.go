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
	// planShortWait is the close wait of the test with no consumer running.
	planShortWait = "3s"
)

// TestDBPlanCloseFailsWhileTheConsumerIsBehind relays the open row of a plan
// to a topic that no consumer reads, stops the relay, and closes the plan
// with a three-second wait. Close fails past the wait, mails that the
// summary is incomplete, and writes no close row; the operator outbox keeps
// every row that the stopped relay did not send.
func TestDBPlanCloseFailsWhileTheConsumerIsBehind(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t)
	stopRelay := pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-g")), ledgerDSN)
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
// session, then closes it from another session and from the same session for
// another accountable operator. Each close writes one refused close row,
// mails the refusal, and fails. The plan stays open, and a close by the
// opener then succeeds.
func TestDBPlanRefusesACloseByAnotherPrincipal(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	depsFor := func(flags []string) dbSQLDeps {
		return pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, flags), ledgerDSN)
	}
	opener := depsFor(planAgentFlags("session-plan-h"))
	opened, err := openPlan(t, opener, writePlanFile(t, "select 1"), "plan test h "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))

	closers := map[string]dbSQLDeps{
		"another session":              depsFor(planAgentFlags("session-plan-other")),
		"another accountable operator": depsFor(planAgentFlagsFor("session-plan-h", planOtherOperatorID, planOtherAccountable)),
	}
	for name, closer := range closers {
		if _, err := closePlan(t, closer, opened.PlanID, "postcheck h"); err == nil ||
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
