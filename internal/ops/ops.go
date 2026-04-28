// Package ops is the registry for one-off and scheduled maintenance
// operations that run against a live Tack server. Every op shares the same
// environment (config, postgres pool, FDB stores) so a new operation is just
// a function plus an init() registration.
//
// Two callers exist today.
//
// First, the cmd/server ops subcommand. It accepts an op name on the command
// line, looks the op up in the registry, opens the shared env, runs the op,
// and closes everything cleanly. This is the path used for ad-hoc backfills
// and reindexes from the deploy host.
//
// Second, future schedulers. A cron job inside the app container can pick an
// op by name and dispatch through the same Run path so periodic maintenance
// stays out of the request hot path.
//
// Naming. Op names are lowercase, dot-separated (e.g. reindex,
// backfill.default_children) so they can be consistently logged and
// discovered via ops list.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
)

// Operation describes a single maintenance op. Run is invoked with a fully
// initialised Env; the framework owns lifecycle, the op owns logic.
type Operation struct {
	Name        string
	Description string
	Run         func(ctx context.Context, env *Env) error
}

// Env is the shared dependency bundle every op receives. Stores and Pool are
// opened by NewEnv and closed by Env.Close. Add more fields here when a new
// op needs them; the entry-point pays the cost of openers it does not need
// to keep all ops uniform.
type Env struct {
	Cfg    *config.Config
	Pool   *pgxpool.Pool
	Stores *fdbadapter.Stores
	Log    *slog.Logger
}

// Close releases the pool. FDB has no per-process close in the bindings.
func (e *Env) Close() {
	if e == nil || e.Pool == nil {
		return
	}
	e.Pool.Close()
}

// NewEnv opens a postgres pool and FDB stores for op use. Callers must Close
// the returned env.
func NewEnv(ctx context.Context, cfg *config.Config) (*Env, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("foundationdb: %w", err)
	}
	return &Env{
		Cfg:    cfg,
		Pool:   pool,
		Stores: stores,
		Log:    slog.Default(),
	}, nil
}

var registry = map[string]Operation{}

// Register installs op into the registry. Panics on duplicate names so a
// double-registration is caught at init time, not at first dispatch.
func Register(op Operation) {
	if op.Name == "" {
		panic("ops.Register: name required")
	}
	if op.Run == nil {
		panic("ops.Register: " + op.Name + " missing Run")
	}
	if _, exists := registry[op.Name]; exists {
		panic("ops.Register: duplicate name: " + op.Name)
	}
	registry[op.Name] = op
}

// Get returns the op registered under name.
func Get(name string) (Operation, bool) {
	op, ok := registry[name]
	return op, ok
}

// List returns every registered op, sorted by name.
func List() []Operation {
	out := make([]Operation, 0, len(registry))
	for _, op := range registry {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run looks up name, opens an env, runs the op, and closes the env. Returns
// an error if the op is unknown or fails.
func Run(ctx context.Context, cfg *config.Config, name string) error {
	op, ok := Get(name)
	if !ok {
		return fmt.Errorf("unknown op: %s", name)
	}
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		return fmt.Errorf("ops.NewEnv: %w", err)
	}
	defer env.Close()

	env.Log.InfoContext(ctx, "ops.start", slog.String("op", op.Name))
	if err := op.Run(ctx, env); err != nil {
		env.Log.ErrorContext(ctx, "ops.failed", slog.String("op", op.Name), slog.String("err", err.Error()))
		return err
	}
	env.Log.InfoContext(ctx, "ops.completed", slog.String("op", op.Name))
	return nil
}
