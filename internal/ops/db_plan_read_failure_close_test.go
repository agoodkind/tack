package ops

import (
	"bytes"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

// consumerOffsetsRead matches the consumer offset read of
// awaitDBPlanProjection.
const consumerOffsetsRead = "%FROM audit.consumer_offsets%"

// TestDBPlanCloseStopsAtAClosedLedgerReader opens a plan with no relay
// running, then closes it through a production ledger reader on the test
// ledger that the test closed before close starts. Close returns the failed
// event ID read of public.ops_outbox within planReadFailureLimit. It writes no
// close row and sends no mail after the open mail.
func TestDBPlanCloseStopsAtAClosedLedgerReader(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-p")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test p "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	reader, err := audit.NewReader(t.Context(), pipeline.readerDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	reader.Close()
	deps.ledgerReader = reader

	started := clock.Now()
	var sink bytes.Buffer
	input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck p", Wait: planReadFailureWait.String()}
	err = runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	requireReturnedWithin(t, "plan close", started, planReadFailureLimit)
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) || !strings.Contains(err.Error(), "read the event IDs in public.ops_outbox") {
		t.Fatalf("plan close = %v, want the failed event ID read", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if kinds := planRowKinds(opsoutbox.Events(t, pool, filter)); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows in the operator outbox = %v, want the open row only", kinds)
	}
}

// TestDBPlanCloseStopsAtAConsumerOffsetReadFailure opens a plan while the
// relay runs and no consumer reads the topic, and waits until the relay has
// sent the open row. Plan close then polls audit.consumer_offsets through a
// production ledger reader on the test ledger. After the first consumer offset
// read, the test stops the relay and closes the reader. Close returns the
// failed consumer offset read within planReadFailureLimit of the reader close.
// It writes no close row and sends no mail after the open mail.
func TestDBPlanCloseStopsAtAConsumerOffsetReadFailure(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	stopRelay := pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-q")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test q "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	waitForOutboxDrain(t, pool, filter)
	reader, err := audit.NewReader(t.Context(), pipeline.readerDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(reader.Close)
	deps.ledgerReader = reader

	closed := make(chan error, 1)
	go func() {
		var sink bytes.Buffer
		input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck q", Wait: planReadFailureWait.String()}
		closed <- runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	}()
	waitForReaderQuery(t, pool, pipeline.readerDSN, consumerOffsetsRead)
	stopRelay()
	reader.Close()
	readerClosed := clock.Now()
	err = <-closed
	requireReturnedWithin(t, "plan close", readerClosed, planReadFailureLimit)
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) || !strings.Contains(err.Error(), "read audit.consumer_offsets") {
		t.Fatalf("plan close = %v, want the failed consumer offset read", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if rows := opsoutbox.Events(t, pool, filter); len(rows) != 0 {
		t.Fatalf("plan rows in the operator outbox = %+v, want none: the stopped relay would leave a close row there", rows)
	}
}
