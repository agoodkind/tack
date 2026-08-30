package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// The pool pings an idle pooled connection before handing it out, and these
// values bound that ping.
//
// Losing a ledger guest black-holes its sockets rather than refusing them, so
// a pooled connection to that guest accepts a write and then never answers.
// The driver only surfaces that as an error when some deadline fires. With no
// bound the deadline is the caller's, so one caller retires one dead
// connection and pays its whole budget doing it; a /healthz probe on a
// two-second deadline therefore served 503 for as many probes as the pool held
// connections (TACK-464). Bounding the ping lets a single caller walk past
// every dead connection and reach a live one inside its own deadline.
//
// The worst case is one caller walking the entire pool dead, which costs
// MaxConns pings, so the per-ping bound is the walk budget divided by the pool
// size rather than a constant: the pool sizes itself to the CPU count with a
// floor of four, and a constant would scale the walk with the machine and
// breach the health deadline on larger hosts. The budget leaves room inside
// the two-second health deadline for the bounded dial that replaces the last
// destroyed connection; the ceiling keeps small pools from growing the ping,
// and the floor stays an order of magnitude above a healthy same-network round
// trip. Exceeding a ping bound costs a reconnect, never a failed request,
// because the pool destroys the connection and retries the acquire.
//
// This cannot move into the connection string. The pool parses exactly seven
// pool_* keys out of a DSN and passes every other unrecognized key to the
// server as a startup parameter, and no key names this timeout.
const (
	acquireWalkBudget  = 1600 * time.Millisecond
	acquirePingCeiling = 200 * time.Millisecond
	acquirePingFloor   = 25 * time.Millisecond
)

// acquirePingTimeoutFor is the per-connection ping bound for a pool of
// maxConns connections: the full-pool walk fits the walk budget, clamped to
// the ceiling and floor.
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

// applyPoolSettings applies the settings the connection string cannot carry.
// It runs after ParseConfig, which has already resolved MaxConns to its
// default when the string does not set pool_max_conns. tracer may be nil;
// when provided, every query is logged via it.
func applyPoolSettings(cfg *pgxpool.Config, tracer pgx.QueryTracer) {
	cfg.PingTimeout = acquirePingTimeoutFor(cfg.MaxConns)
	if tracer != nil {
		cfg.ConnConfig.Tracer = tracer
	}
}

// NewPool creates a connection pool without running migrations.
// tracer may be nil; when provided, every query is logged via it.
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

// Migrate runs pending goose migrations. Called by the `migrate` subcommand only,
// never on HTTP server startup (required for safe horizontal scaling).
func Migrate(ctx context.Context, dsn string, migrationsFS fs.FS) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db, err := goose.OpenDBWithDriver("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return goose.UpContext(ctx, db, ".")
}
