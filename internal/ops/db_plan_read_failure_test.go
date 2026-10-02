package ops

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const (
	// planReadFailureWait is the close wait of the read failure test. A close
	// that polls past a failed read ends at this bound.
	planReadFailureWait = "10s"
	// planSessionPoll is how often the test reads pg_stat_activity. It is
	// shorter than dbPlanProjectionPoll, the interval between outbox reads.
	planSessionPoll = 20 * time.Millisecond
	// outboxEventIDReadQuery counts the sessions of login $1 with the event ID
	// read of awaitDBPlanOutboxDrain as the latest statement.
	outboxEventIDReadQuery = `SELECT count(*) FROM pg_stat_activity
 WHERE usename = $1 AND query = 'SELECT event_id FROM public.ops_outbox'`
)

// TestDBPlanCloseStopsAtALedgerReadFailure opens a plan with no relay
// running. The open row stays in the operator outbox. Plan close waits for
// that row through the production ledger reader on the test ledger, and the
// test closes the reader pool after the first outbox read. Close returns the
// read failure at the next outbox read. It writes no close row and sends no
// mail after the open mail.
func TestDBPlanCloseStopsAtALedgerReadFailure(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-n")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test n "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	reader, err := audit.NewReader(t.Context(), pipeline.readerDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(reader.Close)
	deps.ledgerReader = reader

	closed := make(chan error, 1)
	go func() {
		var sink bytes.Buffer
		input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck n", Wait: planReadFailureWait}
		closed <- runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	}()
	waitForOutboxEventIDRead(t, pool, pipeline.readerDSN)
	reader.Close()
	err = <-closed
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("plan close = %v, want the failed outbox read", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if kinds := planRowKinds(opsoutbox.Events(t, pool, filter)); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows in the operator outbox = %v, want the open row only", kinds)
	}
}

// TestDBPlanStatementStopsAtALedgerReadFailure opens a plan that lists an
// insert into a marker table, then runs that statement with --plan-id while
// the plan rows are read through a production plan pool on the test ledger
// that the test has closed. The command returns the read failure. It writes
// no refused row and no pending row, sends no mail after the open mail, and
// inserts no marker row.
func TestDBPlanStatementStopsAtALedgerReadFailure(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	table := "public.plan_marker_" + uuid.NewString()[:8]
	if _, err := pool.Exec(t.Context(), "CREATE TABLE "+table+" (marker text)"); err != nil {
		t.Fatalf("create the marker table: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), "DROP TABLE IF EXISTS "+table) })
	planned := "insert into " + table + " values ('planned')"
	reason := "plan test o " + uuid.NewString()[:8]
	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-o"))
	opened, err := openPlan(t, deps, writePlanFile(t, planned), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	planID, err := uuid.Parse(opened.PlanID)
	if err != nil {
		t.Fatalf("parse plan ID %q: %v", opened.PlanID, err)
	}
	closedPool, err := openDBPlanPool(t.Context(), ledgerDSN, planID)
	if err != nil {
		t.Fatalf("open the plan pool to close: %v", err)
	}
	closedPool.Close()
	deps.planRowsPool = closedPool

	_, err = runPlanned(t, deps, opened.PlanID, planned, reason)
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("planned statement = %v, want the failed plan row read", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if kinds := planRowKinds(planRows(t, pool, opened.PlanID)); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows = %v, want the open row only", kinds)
	}
	var markers int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("marker rows = %d (%v), want 0 because the statement did not run", markers, err)
	}
}

// waitForOutboxEventIDRead polls pg_stat_activity through pool until a
// session of the login in readerDSN has sent the event ID read of
// public.ops_outbox.
func waitForOutboxEventIDRead(t *testing.T, pool *pgxpool.Pool, readerDSN string) {
	t.Helper()
	config, err := pgx.ParseConfig(readerDSN)
	if err != nil {
		t.Fatalf("parse the reader DSN: %v", err)
	}
	deadline := clock.Now().Add(planPipelineDeadline)
	for {
		var sessions int
		if err := pool.QueryRow(t.Context(), outboxEventIDReadQuery, config.User).Scan(&sessions); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if sessions > 0 {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("login %s sent no event ID read of public.ops_outbox within %s", config.User, planPipelineDeadline)
		}
		time.Sleep(planSessionPoll)
	}
}
