package ops

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// planPostWaitWait is the --wait of the post-wait read test. The relay and
	// the audit consumer finish the open row well inside it.
	planPostWaitWait = 10 * time.Second
	// planPostWaitLimit is planPostWaitWait plus the time that the return of
	// close needs after the bound ends the read of the plan rows.
	planPostWaitLimit = planPostWaitWait + 5*time.Second
)

// TestDBPlanCloseBoundsThePostWaitLedgerRead opens a plan while the relay and
// the audit consumer run, then closes it with a production ledger reader on
// the test ledger. The projection wait completes against the real ledger. The
// ledger reader address in the configuration is a listener on the IPv6
// loopback that accepts connections and never answers, which blocks the pool
// ping that opens the post-wait read (internal/adapters/postgres/db.go line
// 90). Close returns within planPostWaitLimit only when that read runs under
// the --wait bound. It returns a *dbPlanReadError that wraps
// [context.DeadlineExceeded], mails nothing after the open mail, and writes
// no close row.
func TestDBPlanCloseBoundsThePostWaitLedgerRead(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-s")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test s "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	reader, err := audit.NewReader(t.Context(), pipeline.readerDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(reader.Close)
	deps.ledgerReader = reader
	deps.cfg.AuditReaderDSN = dsnAtAddress(t, pipeline.readerDSN, listenAndStaySilent(t))

	started := clock.Now()
	closed := make(chan error, 1)
	go func() {
		var sink bytes.Buffer
		input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck s", Wait: planPostWaitWait.String()}
		closed <- runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	}()
	select {
	case err = <-closed:
	case <-time.After(planPostWaitLimit):
		t.Fatalf("plan close did not return within %s with a post-wait ledger read that never answers", planPostWaitLimit)
	}
	requireReturnedWithin(t, "plan close", started, planPostWaitLimit)
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plan close = %v, want a *dbPlanReadError that wraps the deadline error", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if kinds := planKinds(t, pool, opened.PlanID); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows = %v, want the open row only", kinds)
	}
}
