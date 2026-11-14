// Package ops registers and runs maintenance operations against Tack stores.
// The CLI selects operations by lowercase, dot-separated names such as
// reindex and backfill.default_children. Run initializes shared configuration,
// a Postgres pool, and FDB stores before execution, then closes the environment.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// Operation defines a maintenance operation. The framework initializes Env
// before calling Run and closes it afterward.
type Operation struct {
	Name        string
	Lifetime    clispec.Lifetime
	Audit       audit.Spec
	Description string
	Run         func(ctx context.Context, env *Env) error
	// DryRun reports the work that Run would perform without writing state.
	// The batch command calls DryRun when the operator omits --execute.
	DryRun func(ctx context.Context, env *Env) error `exhaustruct:"optional"`
}

// Env provides shared dependencies for an operation. NewEnv opens Stores and
// Pool; Env.Close releases the pool after execution.
type Env struct {
	Cfg    *config.Config
	Pool   *pgxpool.Pool
	Stores *fdbadapter.Stores
	Log    *slog.Logger
}

// Close releases the pool. The FDB bindings do not provide a per-process close.
func (e *Env) Close() {
	if e == nil || e.Pool == nil {
		return
	}

	e.Pool.Close()
}

// NewEnv opens a postgres pool and FDB stores for op use. Callers must Close
// the returned env.
func NewEnv(ctx context.Context, cfg *config.Config) (*Env, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}

	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("foundationdb: %w", err)
	}

	if cfg.SearchEndpoint != "" {
		stores.EnableSearchWork()
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

	return runOperation(ctx, cfg, op, op.Run)
}

// RunDryRun looks up the operation by name and calls its DryRun function. It
// returns an error if the operation does not exist, does not define DryRun,
// or fails during DryRun.
func RunDryRun(ctx context.Context, cfg *config.Config, name string) error {
	op, ok := Get(name)
	if !ok {
		return fmt.Errorf("unknown op: %s", name)
	}

	if op.DryRun == nil {
		return fmt.Errorf("op %s does not declare a dry run", name)
	}

	return runOperation(ctx, cfg, op, op.DryRun)
}

func runOperation(
	ctx context.Context,
	cfg *config.Config,
	op Operation,
	work func(ctx context.Context, env *Env) error,
) error {
	ctx, span := telemetry.StartSpan(
		ctx,
		"ops.run",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("ops.name", op.Name)),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("op", op.Name))
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		return fmt.Errorf("ops.NewEnv: %w", err)
	}
	defer env.Close()
	env.Log = telemetry.L(ctx)

	env.Log.InfoContext(ctx, "ops.start", slog.String("op", op.Name))
	if err := work(ctx, env); err != nil {
		env.Log.ErrorContext(
			ctx,
			"ops.failed",
			slog.String("op", op.Name),
			slog.String("err", err.Error()),
		)

		return err
	}

	env.Log.InfoContext(ctx, "ops.completed", slog.String("op", op.Name))

	return nil
}
