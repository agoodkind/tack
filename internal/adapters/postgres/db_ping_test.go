package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The pool must reject a silent idle connection before the caller deadline.
func TestPoolRejectsASilentIdleConnectionBeforeTheCallerDeadline(t *testing.T) {
	live := newFakeLedger(t)
	openContext, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	pool, err := NewPool(openContext, fakeLedgerDSN(1, live.addr()), nil)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// Close the ledger sockets before closing the pool.
	defer func() {
		live.shutdown()
		pool.Close()
	}()
	live.goSilent()
	// The pool pings connections only after they remain idle for one second.
	time.Sleep(1500 * time.Millisecond)
	const callerDeadline = 6 * time.Second
	pingContext, cancelPing := context.WithTimeout(context.Background(), callerDeadline)
	defer cancelPing()
	startedAt := time.Now()
	err = pool.Ping(pingContext)
	elapsed := time.Since(startedAt)
	if err == nil {
		t.Fatal("ping against a silent host succeeded, want an error")
	}
	if elapsed >= callerDeadline {
		t.Fatalf("ping used the entire %s caller deadline (%s); pool ping timeout did not apply", callerDeadline, elapsed)
	}
	// Allow the pool ping timeout and one bounded dial to the silent host.
	if budget := acquirePingCeiling + 2*time.Second; elapsed > budget {
		t.Fatalf("ping returned after %s, want within %s", elapsed, budget)
	}
}

// Four silent connections occupy every pool slot during driver cleanup.
// Pool Ping must expire at the health deadline. A fresh dial must use the
// surviving host before that deadline.
func TestPoolPingBlocksOnHeldSlotsAfterAGuestStopsWhileAFreshDialAnswers(t *testing.T) {
	stopped := newFakeLedger(t)
	live := newFakeLedger(t)
	openContext, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	dsn := fakeLedgerDSN(1, stopped.addr(), live.addr()) + "&pool_max_conns=4"
	pool, err := NewPool(openContext, dsn, nil)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// Deferred calls close the ledger sockets before closing the pool.
	defer pool.Close()
	defer stopped.shutdown()
	heldConnections := make([]*pgxpool.Conn, 0, 4)
	for i := range 4 {
		connection, err := pool.Acquire(openContext)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		heldConnections = append(heldConnections, connection)
	}
	for _, connection := range heldConnections {
		connection.Release()
	}
	if idle := pool.Stat().IdleConns(); idle != 4 {
		t.Fatalf("idle connections = %d, want 4 connected to the first host", idle)
	}
	// The pool pings connections only after they remain idle for one second.
	time.Sleep(1500 * time.Millisecond)
	stopped.goSilent()
	pooledContext, cancelPooled := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelPooled()
	startedAt := time.Now()
	err = pool.Ping(pooledContext)
	pooledElapsed := time.Since(startedAt)
	t.Logf("pool Ping with four occupied slots on a stopped host returned after %s: %v", pooledElapsed, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool Ping after %s = %v, want the %s health deadline to expire", pooledElapsed, err, healthProbeDeadline)
	}
	freshContext, cancelFresh := context.WithTimeout(context.Background(), healthProbeDeadline)
	defer cancelFresh()
	startedAt = time.Now()
	err = PingFreshConnection(freshContext, pool)
	freshElapsed := time.Since(startedAt)
	t.Logf("fresh dial after the stopped host answered in %s", freshElapsed)
	if err != nil {
		t.Fatalf("fresh dial after the stopped host after %s: %v", freshElapsed, err)
	}
	if freshElapsed >= healthProbeDeadline {
		t.Fatalf("fresh dial answered after %s, want inside the %s health deadline", freshElapsed, healthProbeDeadline)
	}
}
