package ops

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// planBlockedReadHold is how long the test session keeps the only
	// connection of the plan pool acquired. It ends after dbPlanOpenRowWait.
	planBlockedReadHold = dbPlanOpenRowWait + 15*time.Second
	// planBlockedReadLimit is the longest a planned statement with a blocked
	// plan row read may take. It ends before planBlockedReadHold.
	planBlockedReadLimit = dbPlanOpenRowWait + 5*time.Second
)

// TestDBPlanStatementStopsAtTheOpenRowWait opens a plan with no relay
// running. The open row stays in public.ops_outbox. The test reads the plan
// rows through a production pool on the test ledger with one connection, and
// a second session keeps that connection acquired for planBlockedReadHold.
// The planned statement returns [context.DeadlineExceeded] within
// planBlockedReadLimit, before the session releases the connection. It writes
// no refused row and no pending row and sends no mail after the open mail.
// A table or row lock cannot block the read: YugabyteDB 2024.2 supports only
// ACCESS SHARE in LOCK, and its reads do not wait on row locks.
func TestDBPlanStatementStopsAtTheOpenRowWait(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	reason := "plan test v " + uuid.NewString()[:8]
	deps := planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-v"))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), reason, "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deps.planRowsPool = heldSingleConnectionPool(t, ledgerDSN)

	started := clock.Now()
	_, err = runPlanned(t, deps, opened.PlanID, "select 1", reason)
	if elapsed := clock.Since(started); elapsed > planBlockedReadLimit {
		t.Fatalf("planned statement returned after %s, want within %s while the plan row read is blocked", elapsed, planBlockedReadLimit)
	}
	var readErr *dbPlanReadError
	if !errors.As(err, &readErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("planned statement = %v, want the plan row read cut off at %s", err, dbPlanOpenRowWait)
	}
	mailWithSubject(t, requireMailCount(t, mail, 1), "plan "+opened.PlanID+" opened")
	if kinds := planRowKinds(planRows(t, pool, opened.PlanID)); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows = %v, want the open row only", kinds)
	}
}

// heldSingleConnectionPool opens a pool on the ledger at dsn with one
// connection and acquires that connection in a session that runs BEGIN. The
// session rolls back after planBlockedReadHold or at the test end. A read
// through the pool waits for the connection until the rollback.
func heldSingleConnectionPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	config.MaxConns = 1
	heldPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open the one-connection plan pool: %v", err)
	}
	t.Cleanup(heldPool.Close)
	session, err := heldPool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the session on the plan pool connection: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() { _ = session.Rollback(context.WithoutCancel(t.Context())) })
	}
	timer := time.AfterFunc(planBlockedReadHold, release)
	t.Cleanup(func() {
		timer.Stop()
		release()
	})
	return heldPool
}
