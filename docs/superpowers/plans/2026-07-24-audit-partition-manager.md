# Implement audit partition maintenance

Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`
to implement each task. Track completion with checkboxes. Register pg_partman
on `audit.events` with a 12-week forward buffer and disabled retention. Run
`PartitionManager` in audit-consumer at startup and daily, using a
`SECURITY DEFINER` wrapper and the existing worker lifecycle.

Use Go, YugabyteDB `2024.2.8.0` (PostgreSQL 11.2), pg_partman 4.7.4, goose,
pgx v5, and expvar telemetry.

## Constraints

- Build with `make build`, including baseline checks for vet, golangci,
  staticcheck-extra, and govulncheck. Do not invoke `go build` directly.
- Run migrations only through `./server migrate`, not HTTP startup.
- Use pgx SQL rather than `os/exec` or CLI subprocesses in Go.
- Preserve the append-only compliance ledger and disable pg_partman retention.
- Use `audit_writer` with INSERT/SELECT/UPDATE, without CREATE on `audit`.
  Run DDL only through the wrapper with a pinned `search_path`.
- Keep implementation files at most 200 lines and split by responsibility.
- Use slog attributes and `noun.verb` messages. Use Info for normal work,
  Debug for detail, and Error for failure. Include identifiers and wrapped
  context in errors, as in `fmt.Errorf("...%s: %w", id, err)`.
- Gate real YugabyteDB integration tests on `AUDIT_CHAIN_TEST_DSN` and skip
  when absent, matching `chain_append_integration_test.go`.
- Require gpt-subagent, review-bugbot, and code-review approval and passing CI
  before merge. Validate production changes on disposable QA first.

## Task 1: Register existing partitions

Create `migrations/004_audit_partman.sql` and
`internal/audit/partition_adopt_integration_test.go`. Preserve existing weekly
`events_YYYY_MM_DD` children. Set `p_start_partition` after current coverage to
avoid overlap; new children may use pg_partman's convention without renaming
old children. Register `premake = 12`, disable retention, and grant
`audit_writer` EXECUTE on `audit.run_partition_maintenance()`.

- [ ] Add the failing integration test:

```go
//go:build integration

package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// partmanTestDSN selects a post-004 database for adoption tests.
// Tests skip when the environment does not supply it.
const partmanTestDSN = "AUDIT_CHAIN_TEST_DSN"

func partmanTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(partmanTestDSN)
	if dsn == "" {
		t.Skipf("set %s to a migrated audit DSN to run", partmanTestDSN)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestPartmanAdoptionCreatesFutureWeek verifies registration, disabled
// retention, executable maintenance, and exactly one future partition.
func TestPartmanAdoptionCreatesFutureWeek(t *testing.T) {
	pool := partmanTestPool(t)
	ctx := context.Background()

	var registered int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM partman.part_config WHERE parent_table = 'audit.events'`,
	).Scan(&registered); err != nil {
		t.Fatalf("part_config query: %v", err)
	}
	if registered != 1 {
		t.Fatalf("audit.events registered in part_config = %d, want 1", registered)
	}

	var retention *string
	if err := pool.QueryRow(ctx,
		`SELECT retention FROM partman.part_config WHERE parent_table = 'audit.events'`,
	).Scan(&retention); err != nil {
		t.Fatalf("retention query: %v", err)
	}
	if retention != nil {
		t.Fatalf("retention = %q, want NULL (never auto-drop audit data)", *retention)
	}

	if _, err := pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("run_partition_maintenance: %v", err)
	}

	future := time.Now().UTC().AddDate(0, 0, 56)
	var covering int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		AND $1::timestamptz >= (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz
		AND $1::timestamptz <  (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([0-9 :.+-]+)'''))[1]::timestamptz
	`, future).Scan(&covering); err != nil {
		t.Fatalf("covering-partition query: %v", err)
	}
	if covering != 1 {
		t.Fatalf("partitions covering %s = %d, want 1", future.Format("2006-01-02"), covering)
	}
}
```

- [ ] Start the test database with migrations through 003 only:

```bash
make test-fdb-down 2>/dev/null || true
docker compose -f docker-compose.test.yml up -d yugabyte
# Wait for health and apply migrations through 003 before testing.
AUDIT_CHAIN_TEST_DSN="postgres://yugabyte@127.0.0.1:5433/tack?sslmode=disable" \
  go test -tags integration ./internal/audit/ -run TestPartmanAdoptionCreatesFutureWeek -v
```

Require failure because registration and the wrapper do not exist.

- [ ] Add the migration below. Preserve every goose directive exactly and use
  block comments for explanatory text:

```sql
-- +goose Up
-- +goose StatementBegin
/* Register native weekly management without renaming existing
   events_YYYY_MM_DD children. Start after existing coverage to avoid
   overlapping ranges. Disable automatic deletion of audit data. */
CREATE SCHEMA IF NOT EXISTS partman;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pg_partman WITH SCHEMA partman;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    max_upper DATE;
    start_part DATE;
BEGIN
    /* Read the highest existing partition upper bound. */
    SELECT max((regexp_match(pg_get_expr(c.relpartbound, c.oid),
                             'TO \(''([0-9-]+)'''))[1]::date)
      INTO max_upper
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_namespace n ON n.oid = p.relnamespace
     WHERE n.nspname = 'audit' AND p.relname = 'events';

    /* Start at the later of existing coverage and the current week.
       An empty database also starts at the current week. */
    start_part := greatest(coalesce(max_upper, date_trunc('week', now())::date),
                           date_trunc('week', now())::date);

    PERFORM partman.create_parent(
        p_parent_table    => 'audit.events',
        p_control         => 'event_time',
        p_type            => 'native',
        p_interval        => '1 week',
        p_premake         => 12,
        p_start_partition => start_part::text
    );
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE partman.part_config
   SET retention = NULL,
       retention_keep_table = true,
       infinite_time_partitions = true
 WHERE parent_table = 'audit.events';
-- +goose StatementEnd

-- +goose StatementBegin
/* Permit audit_writer maintenance without CREATE privileges.
   Pin search_path to prevent object substitution and restrict the
   wrapper to audit.events. */
CREATE OR REPLACE FUNCTION audit.run_partition_maintenance()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, partman, audit
AS $$
BEGIN
    PERFORM partman.run_maintenance(p_parent_table => 'audit.events');
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION audit.run_partition_maintenance() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.run_partition_maintenance() TO audit_writer;

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS audit.run_partition_maintenance();
-- +goose StatementEnd

-- +goose StatementBegin
/* Remove registration and preserve all existing partitions. */
DELETE FROM partman.part_config WHERE parent_table = 'audit.events';
-- +goose StatementEnd
```

The migration owner `yugabyte` owns the wrapper and has CREATE on `audit`.
If QA reports missing partman privileges, grant
`GRANT USAGE ON SCHEMA partman TO <owner>` and
`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA partman TO <owner>` before the wrapper.

- [ ] Apply 004 and repeat the exact adoption test command. Require success.
  If 4.7.4 rejects foreign-named children despite a later start partition,
  inspect `partman.show_partitions('audit.events')` on fresh registration and
  rename existing children with
  `ALTER TABLE audit.events_YYYY_MM_DD RENAME TO <partman_name>` before
  registration. Preserve the test assertions.
- [ ] Commit:

```bash
git add migrations/004_audit_partman.sql internal/audit/partition_adopt_integration_test.go
git commit -S -m "Register audit.events with pg_partman and add maintenance wrapper (TACK-333)"
```

## Task 2: Publish headroom and maintenance results

Modify `internal/telemetry/metrics.go` and add or extend `metrics_test.go`.

- [ ] Add the failing metrics test:

```go
package telemetry

import "testing"

func TestAuditPartitionMetrics(t *testing.T) {
	SetAuditPartitionHeadroomWeeks(9)
	if got := auditPartitionHeadroomWeeks.Value(); got != 9 {
		t.Fatalf("headroom gauge = %d, want 9", got)
	}
	SetAuditPartitionHeadroomWeeks(3)
	if got := auditPartitionHeadroomWeeks.Value(); got != 3 {
		t.Fatalf("headroom gauge after update = %d, want 3", got)
	}
	IncAuditPartitionMaintenance("ok")
	IncAuditPartitionMaintenance("ok")
	IncAuditPartitionMaintenance("error")
	if got := auditPartitionMaintenanceTotal.Get("ok").String(); got != "2" {
		t.Fatalf("maintenance ok count = %s, want 2", got)
	}
	if got := auditPartitionMaintenanceTotal.Get("error").String(); got != "1" {
		t.Fatalf("maintenance error count = %s, want 1", got)
	}
}
```

- [ ] Run `go test ./internal/telemetry/ -run TestAuditPartitionMetrics -v`
  and require undefined-metric failure. Add these declarations to the audit
  metrics block and these functions:

```go
	// auditPartitionHeadroomWeeks counts future partitions beyond the current week.
	auditPartitionHeadroomWeeks = expvar.NewInt("tack_audit_partition_headroom_weeks")
	// auditPartitionMaintenanceTotal counts runs by ok or error result.
	auditPartitionMaintenanceTotal = expvar.NewMap("tack_audit_partition_maintenance_total")
```

```go
// SetAuditPartitionHeadroomWeeks publishes the future weekly partition count.
func SetAuditPartitionHeadroomWeeks(weeks int64) { auditPartitionHeadroomWeeks.Set(weeks) }

// IncAuditPartitionMaintenance records one ok or error maintenance result.
func IncAuditPartitionMaintenance(result string) { auditPartitionMaintenanceTotal.Add(result, 1) }
```

- [ ] Repeat the test and require success. Commit:

```bash
git add internal/telemetry/metrics.go internal/telemetry/metrics_test.go
git commit -S -m "Add audit partition headroom gauge and maintenance counter (TACK-333)"
```

## Task 3: Implement the worker

Create `internal/audit/partition_manager.go` and its unit test. Use
`partitionStore` for injected database-free tests and pgx in production.
Reuse the consumer pool without closing it from the worker.

- [ ] Add the failing lifecycle tests:

```go
package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu       sync.Mutex
	runs     int
	runErr   error
	headroom int
	headErr  error
}

func (f *fakeStore) RunMaintenance(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	return f.runErr
}

func (f *fakeStore) HeadroomWeeks(context.Context, time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headroom, f.headErr
}

func (f *fakeStore) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// TestPartitionManagerRunsOnceOnStart verifies maintenance before the first tick.
func TestPartitionManagerRunsOnceOnStart(t *testing.T) {
	store := &fakeStore{headroom: 12}
	pm := NewPartitionManager(store, time.Hour)
	ctx := context.Background()
	pm.Start(ctx)
	deadline := time.After(2 * time.Second)
	for store.runCount() < 1 {
		select {
		case <-deadline:
			t.Fatal("maintenance did not run on start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestPartitionManagerCloseIdempotent verifies repeated Close calls.
func TestPartitionManagerCloseIdempotent(t *testing.T) {
	pm := NewPartitionManager(&fakeStore{headroom: 12}, time.Hour)
	pm.Start(context.Background())
	if err := pm.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestPartitionManagerRunErrorDoesNotPanic verifies survival after failed maintenance.
func TestPartitionManagerRunErrorDoesNotPanic(t *testing.T) {
	store := &fakeStore{runErr: errors.New("boom"), headroom: 0}
	pm := NewPartitionManager(store, time.Hour)
	pm.Start(context.Background())
	deadline := time.After(2 * time.Second)
	for store.runCount() < 1 {
		select {
		case <-deadline:
			t.Fatal("maintenance did not run")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := pm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
```

- [ ] Run `go test ./internal/audit/ -run TestPartitionManager -v` and require
  undefined `NewPartitionManager`. Implement the worker:

```go
package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// partitionHeadroomAlertFloor requires an alert below four future weeks.
// Maintenance normally creates twelve future weekly partitions.
const partitionHeadroomAlertFloor = 4

// partitionStore defines maintenance and headroom queries for the worker.
type partitionStore interface {
	// RunMaintenance creates future audit.events partitions.
	RunMaintenance(ctx context.Context) error
	// HeadroomWeeks counts weekly partitions that start after now.
	HeadroomWeeks(ctx context.Context, now time.Time) (int, error)
}

// PartitionManager maintains future partitions at startup and each period.
// It shares the consumer pool and does not close that pool.
type PartitionManager struct {
	store  partitionStore
	period time.Duration

	stop    chan struct{}
	stopped chan struct{}
}

// NewPartitionManager uses 24h when period is non-positive.
func NewPartitionManager(store partitionStore, period time.Duration) *PartitionManager {
	if period <= 0 {
		period = 24 * time.Hour
	}
	return &PartitionManager{
		store:   store,
		period:  period,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start runs the maintenance loop with goroutine panic recovery.
func (m *PartitionManager) Start(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "audit.partition.panic", slog.Any("err", r))
			}
		}()
		m.loop(ctx)
	}()
}

func (m *PartitionManager) loop(ctx context.Context) {
	ctx = telemetry.WithTraceLogger(ctx, slog.String("worker", "audit.partition"))
	defer close(m.stopped)
	t := time.NewTicker(m.period)
	defer t.Stop()
	/* Restore future coverage immediately before waiting for the first tick. */
	m.runOnce(ctx)
	for {
		select {
		case <-m.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			m.runOnce(ctx)
		}
	}
}

func (m *PartitionManager) runOnce(ctx context.Context) {
	if err := m.store.RunMaintenance(ctx); err != nil {
		telemetry.IncAuditPartitionMaintenance("error")
		telemetry.L(ctx).Error("audit.partition.maintenance_failed", slog.String("err", err.Error()))
		return
	}
	telemetry.IncAuditPartitionMaintenance("ok")

	headroom, err := m.store.HeadroomWeeks(ctx, clock.Now().UTC())
	if err != nil {
		telemetry.L(ctx).Error("audit.partition.headroom_query_failed", slog.String("err", err.Error()))
		return
	}
	telemetry.SetAuditPartitionHeadroomWeeks(int64(headroom))
	if headroom < partitionHeadroomAlertFloor {
		telemetry.L(ctx).Error("audit.partition.headroom_low",
			slog.Int("headroom_weeks", headroom),
			slog.Int("floor_weeks", partitionHeadroomAlertFloor),
		)
		return
	}
	telemetry.L(ctx).Debug("audit.partition.maintained", slog.Int("headroom_weeks", headroom))
}

// Close stops the loop, permits repeated calls, and preserves the shared pool.
func (m *PartitionManager) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	<-m.stopped
	return nil
}

// pgPartitionStore uses the consumer's audit_writer pool and migration 004 wrapper.
type pgPartitionStore struct {
	pool *pgxpool.Pool
}

// NewPGPartitionStore constructs the production store.
func NewPGPartitionStore(pool *pgxpool.Pool) partitionStore {
	return &pgPartitionStore{pool: pool}
}

func (s *pgPartitionStore) RunMaintenance(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		return fmt.Errorf("run partition maintenance: %w", err)
	}
	return nil
}

func (s *pgPartitionStore) HeadroomWeeks(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		AND (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz > $1
	`, now).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("headroom query: %w", err)
	}
	return count, nil
}
```

- [ ] Repeat the lifecycle test command and require all three tests to pass.
  Commit:

```bash
git add internal/audit/partition_manager.go internal/audit/partition_manager_test.go
git commit -S -m "Add PartitionManager worker with headroom metric and alert (TACK-333)"
```

## Task 4: Configure and start the consumer worker

Modify `internal/audit/consumer.go`, `cmd/audit-consumer/main.go`, and
`internal/audit/consumer_config_test.go`.

- [ ] Add the failing default/override test:

```go
package audit

import (
	"testing"
	"time"
)

// TestApplyConsumerDefaultsPartitionPeriod checks the default and explicit period.
func TestApplyConsumerDefaultsPartitionPeriod(t *testing.T) {
	got := applyConsumerDefaults(ConsumerConfig{}).PartitionPeriod
	if got != 24*time.Hour {
		t.Fatalf("default PartitionPeriod = %s, want 24h", got)
	}
	got = applyConsumerDefaults(ConsumerConfig{PartitionPeriod: 6 * time.Hour}).PartitionPeriod
	if got != 6*time.Hour {
		t.Fatalf("explicit PartitionPeriod = %s, want 6h", got)
	}
}
```

- [ ] Run `go test ./internal/audit/ -run TestApplyConsumerDefaultsPartitionPeriod -v`
  and require missing-field failure. Add after `SummaryEvery` in `ConsumerConfig`:

```go
	// PartitionPeriod controls maintenance frequency; zero uses 24h.
	PartitionPeriod time.Duration
```

Before `return cfg` in `applyConsumerDefaults`, add:

```go
	if cfg.PartitionPeriod <= 0 {
		cfg.PartitionPeriod = 24 * time.Hour
	}
```

After `reconciler *Reconciler` in `Consumer`, add:

```go
	partitions *PartitionManager
```

After reconciler construction and before `return c, nil` in `NewConsumer`, add:

```go
	c.partitions = NewPartitionManager(NewPGPartitionStore(ybpool), cfg.PartitionPeriod)
```

After reconciler startup in `Consumer.Start`, add:

```go
	if c.partitions != nil {
		c.partitions.Start(ctx)
	}
```

After reconciler shutdown in `Consumer.Close`, add:

```go
	if c.partitions != nil {
		_ = c.partitions.Close()
	}
```

Add the environment declaration to `consumerEnv`:

```go
	PartitionPeriod time.Duration `env:"AUDIT_CONSUMER_PARTITION_PERIOD" envDefault:"24h"`
```

Add the configuration value to `audit.ConsumerConfig{...}` in `run`:

```go
		PartitionPeriod: cfg.PartitionPeriod,
```

- [ ] Repeat the config test and require success. Run `make build` and require
  vet, golangci, staticcheck, and govulncheck success. Commit:

```bash
git add internal/audit/consumer.go cmd/audit-consumer/main.go internal/audit/consumer_config_test.go
git commit -S -m "Wire PartitionManager into the audit-consumer (TACK-333)"
```

## Task 5: Reproduce missing-partition recovery

Create `internal/audit/partition_manager_integration_test.go` using Task 1's
pool and Task 3's production store. A manager run must recreate coverage for a
future insert when its partition does not exist.

- [ ] Add the regression:

```go
//go:build integration

package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPartitionManagerRestoresMissingFuturePartition verifies a future insert after maintenance.
// It reproduces the 2026-07-06 exhaustion incident tracked by TACK-333.
func TestPartitionManagerRestoresMissingFuturePartition(t *testing.T) {
	pool := partmanTestPool(t)
	ctx := context.Background()

	target := time.Now().UTC().AddDate(0, 0, 7*20) // Select twenty weeks ahead.

	/* Remove the target week's partition to reproduce missing coverage. */
	if _, err := pool.Exec(ctx, `
		DO $$
		DECLARE r record;
		BEGIN
			FOR r IN
				SELECT c.oid::regclass AS child
				FROM pg_inherits i
				JOIN pg_class c ON c.oid = i.inhrelid
				JOIN pg_class p ON p.oid = i.inhparent
				JOIN pg_namespace n ON n.oid = p.relnamespace
				WHERE n.nspname = 'audit' AND p.relname = 'events'
				AND $1::timestamptz >= (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([0-9 :.+-]+)'''))[1]::timestamptz
				AND $1::timestamptz <  (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([0-9 :.+-]+)'''))[1]::timestamptz
			LOOP
				EXECUTE 'DROP TABLE ' || r.child;
			END LOOP;
		END$$;
	`, target); err != nil {
		t.Fatalf("prune target partition: %v", err)
	}

	pm := NewPartitionManager(NewPGPartitionStore(pool), time.Hour)
	pm.Start(ctx)
	t.Cleanup(func() { _ = pm.Close() })

	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM audit.events WHERE org_id = $1`, orgID) })
	deadline := time.After(15 * time.Second)
	for {
		_, err := pool.Exec(ctx, `
			INSERT INTO audit.events (
				org_id, shard, event_time, event_id, seq, actor_id, actor_kind,
				action, entity_kind, entity_id, context, delta, pii_ref,
				prev_hash, row_hash, idempotency_key
			) VALUES ($1, 0, $2, $3, 1, $4, 0, 'test.write', 'node', $5,
			         '{}', 'null', NULL, ''::bytea, '\x00'::bytea, $6)
			ON CONFLICT DO NOTHING
		`, orgID, target, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
			uuid.Must(uuid.NewV7()), uuid.NewString())
		if err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("insert at future week still failing after maintenance: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
```

- [ ] Against a post-004 test database, require this command to pass:

```bash
AUDIT_CHAIN_TEST_DSN="postgres://yugabyte@127.0.0.1:5433/tack?sslmode=disable" \
  go test -tags integration ./internal/audit/ -run TestPartitionManagerRestoresMissingFuturePartition -v
```

Temporarily omit `pm.Start` and require failure from continued insert errors.
Restore the invocation before committing.

- [ ] Commit:

```bash
git add internal/audit/partition_manager_integration_test.go
git commit -S -m "Add partition-manager heal-future-week integration regression (TACK-333)"
```

## Task 6: Validate QA and release

- [ ] Run `make test-unit`, then `make test-integration`, including both real
  adoption and future-week regressions.
- [ ] Require gpt-subagent, review-bugbot, and code-review approval and passing
  CI before merging to main.
- [ ] Deploy the merged ref to `tack_qa`, applying 004 through `./server migrate`.
  Require `SELECT count(*) FROM partman.part_config WHERE parent_table='audit.events'`
  to return 1 and `SELECT retention FROM partman.part_config WHERE parent_table='audit.events'`
  to return NULL. Require startup `audit.partition.maintained` and at least 12
  weeks in `/debug/vars` gauge `tack_audit_partition_headroom_weeks`.
  Recreate QA and repeat the partition and gauge checks.
- [ ] Deploy to `tack_servers` and verify CT 117 preserves stopgap partitions
  through 2026-09-14, registers pg_partman with NULL retention, runs startup
  maintenance, reports at least 12 weeks, and creates a partition beyond that
  date. Require the absence of `no partition of relation "events"` errors.
- [ ] Attach QA and production part_config state, gauge values, and new partition
  evidence to TACK-333 before closing it. Automated maintenance replaces the
  temporary partition extension.

Task 1's adoption test resolves pg_partman 4.7.4 compatibility with an explicit
rename contingency. Tasks 3 and 5 share the same production store, worker,
wrapper, and test pool. Task 4 uses identical `PartitionPeriod` declarations
in defaults, environment parsing, tests, and construction. Do not add pg_cron,
automatic retention, or the separate dead-letter queue in this work.
