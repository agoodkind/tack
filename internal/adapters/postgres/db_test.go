package postgres

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The ledger is reached over a multi-host connection string. Losing one data
// guest black-holes its sockets rather than refusing them, so both a dial to
// that guest and a ping on a connection already held to it hang until some
// deadline fires. These tests stand that shape up over real loopback TCP.

// blackHoleListener completes the TCP handshake and then never speaks the wire
// protocol, which is what a stopped guest's address looks like while the kernel
// still believes its neighbour entry. It holds every accepted connection open.
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

// fakeLedger speaks enough of the wire protocol to complete a startup and
// answer the empty statement the pool uses as its liveness ping. goSilent makes
// it stop answering while leaving the socket open, which is what a pooled
// connection to a lost guest becomes.
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

// shutdown drops every socket the fake holds. A silent fake must be shut down
// before the pool is closed: closing a pool whose connections are black-holed
// makes the driver wait out its own terminate timeout on each one.
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
				// Hold the socket open and answer nothing, exactly as a
				// black-holed connection to a lost guest behaves.
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

// deployDSN renders the connection string in the shape the deploy renders it:
// the ledger hosts in preference order carrying the connect bound.
func deployDSN(connectTimeoutSeconds int, hosts ...string) string {
	return fmt.Sprintf(
		"postgres://tack@%s/tack?sslmode=disable&connect_timeout=%d",
		strings.Join(hosts, ","), connectTimeoutSeconds,
	)
}

// fakeLedgerDSN adds the exec mode the fake server understands. The fake
// answers simple Query messages only.
func fakeLedgerDSN(connectTimeoutSeconds int, hosts ...string) string {
	return deployDSN(connectTimeoutSeconds, hosts...) +
		"&default_query_exec_mode=simple_protocol"
}

// configuredPool builds the pool configuration the same way NewPool does, so
// these assertions run against the configuration the server actually opens.
func configuredPool(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	applyPoolSettings(cfg, nil)
	return cfg
}

// The regression: with the first host black-holed, the pool must reach a
// surviving host well inside the ten-second acceptance bound. With no connect
// bound the dial to the first host waits out the kernel's TCP retry instead of
// ever reaching the second.
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

// The other half of the regression: a connection the pool already holds to a
// host that has gone silent must be surfaced on the pool's own ping bound, not
// on the caller's deadline. A /healthz probe carries a two-second deadline, so
// a caller that spends its whole deadline per dead connection returns 503 for
// as many probes as the pool holds connections.
func TestPoolSurfacesASilentHeldConnectionWithoutSpendingTheCallerDeadline(t *testing.T) {
	live := newFakeLedger(t)

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	pool, err := NewPool(openCtx, fakeLedgerDSN(1, live.addr()), nil)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// The fake must drop its sockets before the pool closes; see shutdown.
	defer func() {
		live.shutdown()
		pool.Close()
	}()

	live.goSilent()
	// The pool only pings a connection idle for longer than a second, which is
	// the state every connection is in behind a once-per-second probe.
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
	// The pool's ping bound, plus one bounded dial to the same silent host.
	if budget := acquirePingCeiling + 2*time.Second; elapsed > budget {
		t.Fatalf("ping returned after %s, want within %s", elapsed, budget)
	}
}

// The connect bound is the one lever the deploy can carry, and it is safe only
// because the driver consumes it. Every connection-string key the driver does
// not recognise is forwarded to the server as a startup parameter, and the
// ledger's SQL layer is PostgreSQL 11 compatible: forwarding a key it does not
// know is a FATAL on every connection, which is worse than the bug this fixes.
// This pins that connect_timeout is consumed and never forwarded.
func TestPoolConfigConsumesConnectTimeoutWithoutForwardingItToTheServer(t *testing.T) {
	// Both shapes the deploy renders: the app gets a URL, and the ops sidecar
	// gets keyword form because a URL rejects a comma list of bracketed IPv6
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
			// The bound applies per host, so the surviving hosts stay reachable.
			if len(cfg.ConnConfig.Fallbacks) != 2 {
				t.Fatalf("fallback hosts = %d, want 2", len(cfg.ConnConfig.Fallbacks))
			}
		})
	}
}

// The pool's ping bound has to leave room for a caller to walk the ENTIRE
// pool dead and still dial a live host inside a two-second health deadline,
// whatever size the pool resolved to: the pool sizes itself to the CPU count,
// so a constant bound breaches the deadline on larger hosts.
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
		t.Fatalf("walking all %d dead connections costs %s, past the %s budget inside the two-second health deadline",
			cfg.MaxConns, walk, acquireWalkBudget)
	}

	// The derivation itself, across pool sizes: small pools cap at the
	// ceiling, large pools divide the budget, absurd pools floor.
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
