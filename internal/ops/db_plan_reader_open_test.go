package ops

import (
	"bytes"
	"maps"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const (
	// planReaderOpenWait is the --wait of the reader open test.
	planReaderOpenWait = 3 * time.Second
	// planReaderOpenLimit is planReaderOpenWait plus the time that the incomplete
	// summary mail and the return of close need. A close that opens the reader
	// under the wait context returns within it.
	planReaderOpenLimit = planReaderOpenWait + 5*time.Second
	// planReaderOpenTimeoutPrefix starts the error that awaitDBPlanOutboxDrain
	// returns when the wait context has ended during its first read.
	planReaderOpenTimeoutPrefix = "public.ops_outbox was not read within "
)

// TestDBPlanCloseBoundsTheLedgerReaderOpen opens a plan, then closes it with
// a three-second wait and a ledger reader address that is a listener on the
// IPv6 loopback. The listener accepts connections and never answers. The
// reader pool ping blocks until its context ends, so close returns within
// planReaderOpenLimit only when the open runs under the wait context. Close
// returns the wait timeout and not a read failure, mails that the summary is
// incomplete, and writes no close row.
//
// A connection limit on the reader role cannot block the open. The server
// refuses the login at once, and audit.NewReader logs the failed ping as
// audit.reader.ping_deferred and returns the reader (internal/audit/reader.go
// lines 39 to 44). A held lock cannot block it either. The open path runs only
// the pool ping, which acquires a connection and sends the empty statement
// "-- ping" (pgx v5.10.0 pgxpool/pool.go lines 833 to 840, conn.go lines 453
// to 455, pgconn/pgconn.go lines 2096 to 2098); that statement takes no lock.
func TestDBPlanCloseBoundsTheLedgerReaderOpen(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-r")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test r "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	filter := planRowsFilter(opened.PlanID)
	deleteOutboxRowsAfterTest(t, pool, filter)
	silentAddress := listenAndStaySilent(t)
	deps.cfg.AuditReaderDSN = dsnAtAddress(t, pipeline.readerDSN, silentAddress)
	deps.ledgerReader = nil

	started := clock.Now()
	closed := make(chan error, 1)
	go func() {
		var sink bytes.Buffer
		input := dbPlanCloseInput{PlanID: opened.PlanID, Postcheck: "postcheck r", Wait: planReaderOpenWait.String()}
		closed <- runDBPlanClose(t.Context(), deps, input, &bufferSink{buf: &sink}, true)
	}()
	select {
	case err = <-closed:
	case <-time.After(planReaderOpenLimit):
		t.Fatalf("plan close did not return within %s of the call with a ledger reader that never answers", planReaderOpenLimit)
	}
	requireReturnedWithin(t, "plan close", started, planReaderOpenLimit)
	wantTimeout := planReaderOpenTimeoutPrefix + planReaderOpenWait.String()
	if err == nil || isDBPlanReadError(err) || !strings.Contains(err.Error(), wantTimeout) {
		t.Fatalf("plan close = %v, want the wait timeout %q", err, wantTimeout)
	}
	mailWithSubject(t, requireMailCount(t, mail, 2), "summary of plan "+opened.PlanID+" is incomplete")
	if kinds := planRowKinds(opsoutbox.Events(t, pool, filter)); !maps.Equal(kinds, map[string]int{"ops.db_plan_open ok": 1}) {
		t.Fatalf("plan rows in the operator outbox = %v, want the open row only", kinds)
	}
}

// listenAndStaySilent listens on the IPv6 loopback, accepts every connection,
// and neither reads nor writes. It returns the listener address. The test end
// closes the listener and every accepted connection.
func listenAndStaySilent(t *testing.T) string {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen on the IPv6 loopback: %v", err)
	}
	var mutex sync.Mutex
	var accepted []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mutex.Lock()
		defer mutex.Unlock()
		for _, conn := range accepted {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mutex.Lock()
			accepted = append(accepted, conn)
			mutex.Unlock()
		}
	}()
	return listener.Addr().String()
}

// dsnAtAddress returns a DSN with the user, password, and database of dsn and
// the host and port of address.
func dsnAtAddress(t *testing.T, dsn, address string) string {
	t.Helper()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the reader DSN: %v", err)
	}
	if config.User == "" || config.Database == "" {
		t.Fatalf("reader DSN has user %q and database %q, want both", config.User, config.Database)
	}
	target := url.URL{
		Scheme: "postgres", User: url.UserPassword(config.User, config.Password),
		Host: address, Path: "/" + config.Database,
	}
	if _, parseErr := pgx.ParseConfig(target.String()); parseErr != nil {
		t.Fatalf("parse the silent reader DSN: %v", parseErr)
	}
	return target.String()
}
