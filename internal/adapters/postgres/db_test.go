package postgres

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// blackHoleListener accepts TCP connections without answering protocol messages.
// Its open sockets simulate an unresponsive database host.
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

func (listener *blackHoleListener) serve() {
	for {
		connection, err := listener.listener.Accept()
		if err != nil {
			return
		}
		listener.mu.Lock()
		listener.accepted = append(listener.accepted, connection)
		listener.mu.Unlock()
	}
}

func (listener *blackHoleListener) addr() string {
	return listener.listener.Addr().String()
}

func (listener *blackHoleListener) acceptedCount() int {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	return len(listener.accepted)
}

// deployDSN formats hosts in preference order with a connection timeout.
func deployDSN(connectTimeoutSeconds int, hosts ...string) string {
	return fmt.Sprintf(
		"postgres://tack@%s/tack?sslmode=disable&connect_timeout=%d",
		strings.Join(hosts, ","), connectTimeoutSeconds,
	)
}

// fakeLedgerDSN selects the simple Query protocol that fakeLedger supports.
func fakeLedgerDSN(connectTimeoutSeconds int, hosts ...string) string {
	return deployDSN(connectTimeoutSeconds, hosts...) +
		"&default_query_exec_mode=simple_protocol"
}

// configuredPool applies the production pool settings to a parsed DSN.
func configuredPool(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	configuration, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	applyPoolSettings(configuration, nil)
	return configuration
}

// The pool must connect to a surviving host within five seconds when the first
// host accepts TCP connections but does not answer PostgreSQL messages.
func TestNewPoolConnectsToASurvivingHostWhenTheFirstBlackHoles(t *testing.T) {
	blackHole := newBlackHoleListener(t)
	live := newFakeLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	startedAt := time.Now()
	pool, err := NewPool(ctx, fakeLedgerDSN(1, blackHole.addr(), live.addr()), nil)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("open pool across an unresponsive first host: %v", err)
	}
	defer pool.Close()
	if elapsed > 5*time.Second {
		t.Fatalf("pool connected to a surviving host after %s, want at most five seconds", elapsed)
	}
	if blackHole.acceptedCount() == 0 {
		t.Fatal("pool did not dial the unresponsive host; failover did not run")
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping the surviving host: %v", err)
	}
}

// healthProbeDeadline matches the private healthCheckTimeout in cmd/server.
const healthProbeDeadline = 2 * time.Second

// openPoolAcrossBlackHole opens a pool with an unresponsive first host and a
// live second host, then retires its connections. The next Ping must dial.
// The driver applies the connection timeout to each host in preference order.
func openPoolAcrossBlackHole(
	t *testing.T,
	connectTimeoutSeconds int,
) (*pgxpool.Pool, *blackHoleListener) {
	t.Helper()
	blackHole := newBlackHoleListener(t)
	live := newFakeLedger(t)
	openContext, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelOpen)
	pool, err := NewPool(openContext, fakeLedgerDSN(connectTimeoutSeconds, blackHole.addr(), live.addr()), nil)
	if err != nil {
		t.Fatalf("open pool across an unresponsive first host: %v", err)
	}
	t.Cleanup(pool.Close)
	pool.Reset()
	return pool, blackHole
}

// A one-second connection timeout permits failover within the health deadline.
func TestPoolPingConnectsToASurvivingHostInsideTheHealthDeadline(t *testing.T) {
	pool, blackHole := openPoolAcrossBlackHole(t, 1)
	dialsBefore := blackHole.acceptedCount()
	pingContext, cancelPing := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelPing()
	startedAt := time.Now()
	err := pool.Ping(pingContext)
	elapsed := time.Since(startedAt)
	t.Logf("ping across an unresponsive first host at connect_timeout=1 answered in %s", elapsed)
	if err != nil {
		t.Fatalf("ping across an unresponsive first host after %s: %v", elapsed, err)
	}
	if elapsed >= healthProbeDeadline {
		t.Fatalf("ping answered after %s, want inside the %s health deadline", elapsed, healthProbeDeadline)
	}
	if blackHole.acceptedCount() == dialsBefore {
		t.Fatal("pool did not dial the unresponsive host; the health probe dial did not run")
	}
}
