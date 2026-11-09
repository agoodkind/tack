package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These constants limit the pool's ping of an idle connection before
// acquisition.
//
// TACK-464 recorded a lost ledger guest accepting socket writes without
// replying instead of refusing connections. Without a ping timeout, the
// driver waited for the caller's deadline before reporting an error. Each
// caller spent its entire deadline rejecting one dead connection. The
// two-second /healthz probes returned 503 once per pooled connection. A ping
// timeout lets one caller reject several dead connections within a separate
// ping budget.
//
// The ping budget does not bound the complete health probe. The driver can
// reserve a destroyed connection's slot while its cancel request to the dead
// host uses a fifteen-second deadline. PingFreshConnection opens a
// connection outside the pool to avoid that wait. A pool containing only
// dead connections can require MaxConns pings.
//
// The calculation divides acquireWalkBudget by the pool size before applying
// the ceiling and floor. The default pool size follows the CPU count with a
// minimum of four. A fixed ping timeout would increase the total ping budget
// with the CPU count. The ceiling limits each ping for small pools. The
// original floor rationale allowed an order of magnitude above a healthy
// round trip on the same network. The pool size times the 25 ms floor
// exceeds the 1600 ms full-pool ping budget above 64 connections. After a
// ping timeout, the pool destroys the connection and retries acquisition;
// the ping error does not itself fail the request.
//
// pgx v5.11.0 accepts eight pool_* connection-string keys, including
// pool_ping_timeout. It treats other unrecognized parameters as server
// startup parameters. applyPoolSettings overrides the parsed timeout with
// the value derived from the pool size.
const (
	acquireWalkBudget  = 1600 * time.Millisecond
	acquirePingCeiling = 200 * time.Millisecond
	acquirePingFloor   = 25 * time.Millisecond
)

// acquirePingTimeoutFor divides the full-pool ping budget by maxConns and
// clamps the per-connection result to the ceiling and floor. The floor
// permits the full-pool total to exceed that budget above 64 connections.
func acquirePingTimeoutFor(maxConns int32) time.Duration {
	if maxConns < 1 {
		maxConns = 1
	}
	timeout := acquireWalkBudget / time.Duration(maxConns)
	if timeout > acquirePingCeiling {
		return acquirePingCeiling
	}
	if timeout < acquirePingFloor {
		return acquirePingFloor
	}
	return timeout
}

// applyPoolSettings sets the derived ping timeout after ParseConfig resolves
// MaxConns, including its default when pool_max_conns is absent. It
// overrides any parsed pool_ping_timeout. tracer may be nil; callers can
// supply a tracer to log each query.
func applyPoolSettings(cfg *pgxpool.Config, tracer pgx.QueryTracer) {
	cfg.PingTimeout = acquirePingTimeoutFor(cfg.MaxConns)
	if tracer != nil {
		cfg.ConnConfig.Tracer = tracer
	}
}

// NewPool creates a connection pool without running migrations.
//
// It pings the pool before returning it. Callers can supply tracer to log
// each query.
//
//   - A nil tracer omits caller-supplied query tracing.
func NewPool(ctx context.Context, dsn string, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool parse config: %w", err)
	}
	applyPoolSettings(cfg, tracer)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return pool, nil
}

// PingFreshConnection opens a standalone connection from the pool's
// connection configuration, pings it, and closes it under ctx.
//
// It does not wait for a pool slot. The pool's own Ping acquires a slot
// first. A lost ledger guest can leave every existing connection to that
// guest unresponsive.
//
// TACK-464 recorded this sequence during the guest failure:
//
//   - Pool acquisition pinged each idle connection under its ping timeout
//     and destroyed each failed connection.
//   - The driver reserved each slot while its cancel request to the dead
//     host used a fifteen-second deadline.
//   - Acquisition waited on the pool's semaphore while every slot awaited
//     cleanup. The two-second /healthz probe returned 503 on four
//     consecutive probes.
//
// A fresh dial checks whether this instance can open a ledger connection at
// the time of the probe. Each probe attempts one standalone connection using
// the configured host fallbacks and deadlines.
func PingFreshConnection(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.fresh_connect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("postgres connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := conn.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "postgres.fresh_ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("postgres ping: %w", err)
	}
	return nil
}
