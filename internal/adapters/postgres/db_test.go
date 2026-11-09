package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The ledger connection string lists multiple hosts. The guest-loss case
// left sockets open without replies instead of refusing connections. Both a
// dial to that guest and a ping on an existing connection waited for a
// deadline. These tests reproduce that case with real loopback TCP.

// blackHoleListener completes the TCP handshake and does not answer the
// PostgreSQL protocol. It reproduces the stopped-guest address while the
// kernel retains its neighbor entry. The listener leaves every accepted
// connection open.
type blackHoleListener struct {
	listener net.Listener

	mu       sync.Mutex
	accepted []net.Conn
}

func newBlackHoleListener(t *testing.T) *blackHoleListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	blackHole := &blackHoleListener{listener: listener}
	go blackHole.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		blackHole.mu.Lock()
		defer blackHole.mu.Unlock()
		for _, connection := range blackHole.accepted {
			_ = connection.Close()
		}
	})
	return blackHole
}

func (b *blackHoleListener) serve() {
	for {
		connection, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		b.accepted = append(b.accepted, connection)
		b.mu.Unlock()
	}
}

func (b *blackHoleListener) addr() string {
	return b.listener.Addr().String()
}

func (b *blackHoleListener) acceptedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.accepted)
}

// fakeLedger implements enough of the PostgreSQL protocol to complete
// startup and answer the empty statement that the pool uses for its liveness
// ping. goSilent stops replies without closing sockets, reproducing a pooled
// connection after a guest stops.
type fakeLedger struct {
	listener   net.Listener
	silent     chan struct{}
	done       chan struct{}
	silentOnce sync.Once
	doneOnce   sync.Once

	mu       sync.Mutex
	accepted []net.Conn
}

func newFakeLedger(t *testing.T) *fakeLedger {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ledger := &fakeLedger{
		listener: listener,
		silent:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	go ledger.serve()
	t.Cleanup(ledger.shutdown)
	return ledger
}

// shutdown closes every socket that the fake accepted. A silent fake must
// close its sockets before the pool closes. Otherwise, the driver waits for
// its termination timeout on each unresponsive connection.
func (f *fakeLedger) shutdown() {
	f.doneOnce.Do(func() { close(f.done) })
	_ = f.listener.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, connection := range f.accepted {
		_ = connection.Close()
	}
	f.accepted = nil
}

func (f *fakeLedger) addr() string {
	return f.listener.Addr().String()
}

func (f *fakeLedger) goSilent() {
	f.silentOnce.Do(func() { close(f.silent) })
}

func (f *fakeLedger) isSilent() bool {
	select {
	case <-f.silent:
		return true
	default:
		return false
	}
}

func (f *fakeLedger) serve() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.accepted = append(f.accepted, connection)
		f.mu.Unlock()
		go f.serveConnection(connection)
	}
}

func (f *fakeLedger) serveConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	backend := pgproto3.NewBackend(connection, connection)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	if f.isSilent() {
		<-f.done
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: make([]byte, 4)})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return
	}
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch message.(type) {
		case *pgproto3.Query:
			if f.isSilent() {
				// The fake leaves the socket open without replying, reproducing an
				// unresponsive connection after a guest stops.
				<-f.done
				return
			}
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if backend.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// deployDSN reproduces the deployment connection string with ledger hosts in
// preference order and a connection timeout.
func deployDSN(connectTimeoutSeconds int, hosts ...string) string {
	return fmt.Sprintf(
		"postgres://tack@%s/tack?sslmode=disable&connect_timeout=%d",
		strings.Join(hosts, ","), connectTimeoutSeconds,
	)
}

// fakeLedgerDSN selects the execution mode that the fake server supports.
// The fake answers only simple Query messages.
func fakeLedgerDSN(connectTimeoutSeconds int, hosts ...string) string {
	return deployDSN(connectTimeoutSeconds, hosts...) +
		"&default_query_exec_mode=simple_protocol"
}

// configuredPool uses ParseConfig and applyPoolSettings in the same order as
// NewPool. The assertions check the configuration that the server uses to
// open its pool.
func configuredPool(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	applyPoolSettings(cfg, nil)
	return cfg
}

// The regression requires the pool to connect to a surviving host well
// before the ten-second acceptance limit when the first host does not reply.
// This test requires success within five seconds. Without an explicit
// connection timeout, the pool supplies the driver's two-minute fallback
// limit. A silent first host consumes that limit before the driver tries the
// next host.
func TestNewPoolReachesASurvivingHostWhenTheFirstBlackHoles(t *testing.T) {
	blackHole := newBlackHoleListener(t)
	live := newFakeLedger(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	startedAt := time.Now()
	pool, err := NewPool(ctx, fakeLedgerDSN(1, blackHole.addr(), live.addr()), nil)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("open pool across a black-holed first host: %v", err)
	}
	defer pool.Close()

	if elapsed > 5*time.Second {
		t.Fatalf("pool reached a surviving host after %s, want well inside the ten-second bound", elapsed)
	}
	if blackHole.acceptedCount() == 0 {
		t.Fatal("the black-holed host was never dialled, so this did not exercise failover")
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping the surviving host: %v", err)
	}
}

// healthProbeDeadline repeats the two-second deadline that GET /healthz
// uses. The healthCheckTimeout constant in the runtime package is private.
const healthProbeDeadline = 2 * time.Second

// openPoolAcrossBlackHole opens a pool with an unresponsive first host and a
// live second host. It resets every pooled connection to force the next Ping
// to dial. The driver tries hosts in list order and applies the connection
// timeout to each host. The first host consumes that timeout before the live
// host answers. A /healthz probe makes this kind of dial after the pool
// retires its connections to a stopped ledger guest.
func openPoolAcrossBlackHole(t *testing.T, connectTimeoutSeconds int) (*pgxpool.Pool, *blackHoleListener) {
	t.Helper()
	blackHole := newBlackHoleListener(t)
	live := newFakeLedger(t)

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelOpen)
	pool, err := NewPool(openCtx, fakeLedgerDSN(connectTimeoutSeconds, blackHole.addr(), live.addr()), nil)
	if err != nil {
		t.Fatalf("open pool across a black-holed first host: %v", err)
	}
	t.Cleanup(pool.Close)
	pool.Reset()
	return pool, blackHole
}

// This test checks that a one-second connection timeout permits Pool.Ping to
// connect after an unresponsive first host within the health deadline. A
// /healthz probe needs this timing to return 200 while one ledger guest has
// stopped.
func TestPoolPingReachesASurvivingHostInsideTheHealthDeadline(t *testing.T) {
	pool, blackHole := openPoolAcrossBlackHole(t, 1)
	dialsBefore := blackHole.acceptedCount()

	pingCtx, cancelPing := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelPing()

	startedAt := time.Now()
	err := pool.Ping(pingCtx)
	elapsed := time.Since(startedAt)

	t.Logf("ping across a black-holed first host at connect_timeout=1 answered in %s", elapsed)
	if err != nil {
		t.Fatalf("ping across a black-holed first host after %s: %v", elapsed, err)
	}
	if elapsed >= healthProbeDeadline {
		t.Fatalf("ping answered after %s, want inside the %s health deadline", elapsed, healthProbeDeadline)
	}
	if blackHole.acceptedCount() == dialsBefore {
		t.Fatal("the black-holed host was never dialled; this did not exercise the health probe's dial")
	}
}

// QA measured this failure on 2026-09-20 (TACK-464). The connection timeout
// equaled the two-second health deadline. The unresponsive first host
// consumed that deadline, and the probe returned 503 with "yugabyte
// unhealthy" and "context deadline exceeded". This test preserves that
// failure expectation. A driver that tries the next host sooner causes this
// test to fail.
func TestPoolPingMissesTheHealthDeadlineWhenTheConnectBoundEqualsIt(t *testing.T) {
	pool, _ := openPoolAcrossBlackHole(t, 2)

	pingCtx, cancelPing := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelPing()

	startedAt := time.Now()
	err := pool.Ping(pingCtx)
	elapsed := time.Since(startedAt)

	t.Logf("ping across a black-holed first host at connect_timeout=2 returned after %s: %v", elapsed, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ping after %s = %v, want the %s health deadline to expire", elapsed, err, healthProbeDeadline)
	}
}

// This regression requires the pool to detect an existing silent connection
// within its own ping timeout instead of spending the caller's deadline. The
// historical /healthz probe used a two-second deadline. Spending that
// deadline on each dead connection caused one 503 response per pooled
// connection.
func TestPoolSurfacesASilentHeldConnectionWithoutSpendingTheCallerDeadline(t *testing.T) {
	live := newFakeLedger(t)

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	pool, err := NewPool(openCtx, fakeLedgerDSN(1, live.addr()), nil)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// The fake must close its sockets through shutdown before the pool closes.
	defer func() {
		live.shutdown()
		pool.Close()
	}()

	live.goSilent()
	// The pool pings a connection only after more than one second of idle time.
	// The once-per-second probe case uses idle connections.
	time.Sleep(1500 * time.Millisecond)

	const callerDeadline = 6 * time.Second
	pingCtx, cancelPing := context.WithTimeout(context.Background(), callerDeadline)
	defer cancelPing()

	startedAt := time.Now()
	err = pool.Ping(pingCtx)
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatal("ping against a silent host succeeded, want an error")
	}
	if elapsed >= callerDeadline {
		t.Fatalf("ping spent the whole %s caller deadline (%s); the pool is not bounding its own ping",
			callerDeadline, elapsed)
	}
	// The budget includes the pool's ping timeout and one bounded dial to the
	// same silent host.
	if budget := acquirePingCeiling + 2*time.Second; elapsed > budget {
		t.Fatalf("ping returned after %s, want within %s", elapsed, budget)
	}
}

// QA measured this defect on 2026-09-20 (TACK-464). The QA owner guest first
// answered four pooled connections and then stopped. The second host
// remained live. The next Pool.Ping tested each idle connection within its
// ping timeout and destroyed each failed connection. The driver's cancel
// request to the dead host used a fifteen-second deadline while the slot
// remained reserved. With all four slots reserved, Ping waited on the pool's
// semaphore beyond the health deadline. A fresh dial from the same
// configuration spent the connection timeout on the stopped host and
// connected to the live host within the deadline.
func TestPoolPingBlocksOnHeldSlotsAfterAGuestStopsWhileAFreshDialAnswers(t *testing.T) {
	stopped := newFakeLedger(t)
	live := newFakeLedger(t)

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	dsn := fakeLedgerDSN(1, stopped.addr(), live.addr()) + "&pool_max_conns=4"
	pool, err := NewPool(openCtx, dsn, nil)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// The silent fake must close its sockets through shutdown before the pool
	// closes.
	defer pool.Close()
	defer stopped.shutdown()

	held := make([]*pgxpool.Conn, 0, 4)
	for i := range 4 {
		conn, err := pool.Acquire(openCtx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		held = append(held, conn)
	}
	for _, conn := range held {
		conn.Release()
	}
	if idle := pool.Stat().IdleConns(); idle != 4 {
		t.Fatalf("idle connections = %d, want 4 held to the first host", idle)
	}
	// The pool pings a connection only after more than one second of idle time.
	time.Sleep(1500 * time.Millisecond)
	stopped.goSilent()

	pooledCtx, cancelPooled := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelPooled()
	startedAt := time.Now()
	err = pool.Ping(pooledCtx)
	pooledElapsed := time.Since(startedAt)
	t.Logf("pool Ping with four held slots to a stopped host returned after %s: %v", pooledElapsed, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool Ping after %s = %v, want the %s health deadline to expire", pooledElapsed, err, healthProbeDeadline)
	}

	freshCtx, cancelFresh := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelFresh()
	startedAt = time.Now()
	err = PingFreshConnection(freshCtx, pool)
	freshElapsed := time.Since(startedAt)
	t.Logf("fresh dial past the stopped host answered in %s", freshElapsed)
	if err != nil {
		t.Fatalf("fresh dial past the stopped host after %s: %v", freshElapsed, err)
	}
	if freshElapsed >= healthProbeDeadline {
		t.Fatalf("fresh dial answered after %s, want inside the %s health deadline", freshElapsed, healthProbeDeadline)
	}
}

// This test verifies that the driver consumes connect_timeout instead of
// sending it to the server as a startup parameter. Deployment used this
// setting to limit host failover time. The original rationale concerned the
// ledger's PostgreSQL 11-compatible SQL layer: an unknown startup parameter
// caused FATAL on every connection, preventing connections rather than
// delaying failover. Unrecognized connection parameters still become server
// startup parameters. pgx also supports pool_ping_timeout; connect_timeout
// is not its only timeout setting.
func TestPoolConfigConsumesConnectTimeoutWithoutForwardingItToTheServer(t *testing.T) {
	// The deployment forms under test use URL syntax for the app and keyword
	// syntax for the ops sidecar. The original deployment used keyword syntax
	// because URL parsing rejects a comma-separated list of bracketed IPv6
	// hosts.
	testCases := []struct {
		name string
		dsn  string
	}{
		{
			name: "app URL form",
			dsn:  deployDSN(2, "yb1:5433", "yb2:5433", "yb3:5433"),
		},
		{
			name: "ops keyword form",
			dsn: "host=yb1,yb2,yb3 port=5433 user=tack dbname=tack " +
				"sslmode=disable connect_timeout=2",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := configuredPool(t, testCase.dsn)

			if cfg.ConnConfig.ConnectTimeout != 2*time.Second {
				t.Fatalf("ConnectTimeout = %s, want %s",
					cfg.ConnConfig.ConnectTimeout, 2*time.Second)
			}
			for name, value := range cfg.ConnConfig.RuntimeParams {
				t.Errorf("DSN key %q=%q is forwarded to the server as a startup parameter",
					name, value)
			}
			// The connection timeout applies to each host separately. The configured
			// alternatives permit attempts to surviving hosts.
			if len(cfg.ConnConfig.Fallbacks) != 2 {
				t.Fatalf("fallback hosts = %d, want 2", len(cfg.ConnConfig.Fallbacks))
			}
		})
	}
}

// This test checks the derived ping timeout and the total ping budget for
// the resolved pool size. The default pool size follows the CPU count. A
// fixed per-connection timeout would increase the total with the CPU count.
// The calculation's floor permits a total above the budget for more than 64
// connections; the explicit size cases restrict their total-budget assertion
// accordingly.
//
// Slot cleanup can exceed the health deadline even when the pings fit their
// budget. The health probe dials outside the pool to avoid waiting for
// cleanup.
// TestPoolPingBlocksOnHeldSlotsAfterAGuestStopsWhileAFreshDialAnswers checks
// that distinction.
func TestPoolConfigBoundsTheFullPoolWalkUnderTheHealthDeadline(t *testing.T) {
	cfg := configuredPool(t, deployDSN(2, "yb1:5433"))
	if cfg.PingTimeout <= 0 {
		t.Fatal("PingTimeout is unset, so a dead connection is surfaced on the caller's deadline")
	}
	if cfg.PingTimeout != acquirePingTimeoutFor(cfg.MaxConns) {
		t.Fatalf("PingTimeout = %s, want %s for a pool of %d",
			cfg.PingTimeout, acquirePingTimeoutFor(cfg.MaxConns), cfg.MaxConns)
	}
	if walk := time.Duration(cfg.MaxConns) * cfg.PingTimeout; walk > acquireWalkBudget {
		t.Fatalf("walking all %d dead connections costs %s, past the %s walk budget",
			cfg.MaxConns, walk, acquireWalkBudget)
	}

	// The size cases check the ceiling for small pools, division by pool size
	// for larger pools, and the floor for very large pools.
	for _, tc := range []struct {
		maxConns int32
		want     time.Duration
	}{
		{maxConns: 1, want: acquirePingCeiling},
		{maxConns: 4, want: acquirePingCeiling},
		{maxConns: 8, want: acquireWalkBudget / 8},
		{maxConns: 16, want: acquireWalkBudget / 16},
		{maxConns: 128, want: acquirePingFloor},
	} {
		if got := acquirePingTimeoutFor(tc.maxConns); got != tc.want {
			t.Errorf("acquirePingTimeoutFor(%d) = %s, want %s", tc.maxConns, got, tc.want)
		}
		if walk := time.Duration(tc.maxConns) * acquirePingTimeoutFor(tc.maxConns); tc.maxConns <= 64 && walk > acquireWalkBudget {
			t.Errorf("pool of %d walks dead in %s, past the %s budget", tc.maxConns, walk, acquireWalkBudget)
		}
	}
}
