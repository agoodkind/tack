package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
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
// connections (TACK-464). Bounding the ping lets a single caller retire every
// dead connection inside the walk budget instead of one per caller deadline.
//
// The bound does not put a health probe inside its deadline. A destroyed
// connection's slot stays held while the driver runs its cancel request
// against the dead host under a fifteen-second deadline, and
// PingFreshConnection dials outside the pool for that reason. The worst case
// for a caller is walking the entire pool dead, which costs MaxConns pings.
// The per-ping bound is therefore the walk budget divided by the pool size
// rather than a constant: the pool sizes itself to the CPU count with a floor
// of four, and a constant would scale the walk with the machine. The ceiling
// keeps small pools from growing the ping, and the floor stays an order of
// magnitude above a healthy same-network round trip. Exceeding a ping bound
// costs a reconnect, never a failed request, because the pool destroys the
// connection and retries the acquire.
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

// PingFreshConnection opens one standalone connection from the pool's
// connection configuration, pings it, and closes it, all under ctx. It never
// waits on a pool slot.
//
// The pool's own Ping acquires a slot first. After a ledger guest stops, every
// pooled connection to it is dead: the next acquire pings each one under the
// bound above and destroys it, and the driver's close then runs a cancel
// request against the dead host under its own fifteen-second deadline while
// the slot stays held. With every slot held that way, an acquire blocks on the
// pool's semaphore for the whole cleanup, and a /healthz probe on a two-second
// deadline answered 503 four probes in a row (TACK-464). A fresh dial answers
// the question the probe asks, whether this instance can open a ledger
// connection now, and costs one bounded dial per probe.
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
