# Audit events partition-manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Guarantee `audit.events` always has a weekly partition covering `now()` plus a forward buffer, maintained by an owned, self-healing in-process worker, so audit writes never fail from partition exhaustion again.

**Architecture:** pg_partman is the partition engine, registered on `audit.events` via `create_parent` with a 12-week premake and retention off. A `PartitionManager` goroutine in the audit-consumer process calls a `SECURITY DEFINER` maintenance wrapper on boot and on a daily tick, mirroring the existing `Notarizer` and `Reconciler` workers. Boot catch-up re-establishes headroom on every deploy.

**Tech Stack:** Go, YugabyteDB (PostgreSQL 11.2-based, build 2024.2.8.0), pg_partman 4.7.4, goose SQL migrations, pgx v5, expvar telemetry.

## Global Constraints

- Build with `make build` (runs vet, golangci, staticcheck-extra, govulncheck baseline-gated). Never call `go build` directly.
- Migrations run via `./server migrate` only, never on HTTP startup.
- No shell-outs in tack Go: no `os/exec` of CLIs. Engine work happens in SQL through pgx.
- The audit ledger is append-only and compliance-critical: never auto-drop audit data. pg_partman retention stays off.
- The audit-consumer connects as `audit_writer` (INSERT/SELECT/UPDATE only, no CREATE on schema `audit`). DDL runs through a `SECURITY DEFINER` wrapper with a pinned `search_path`.
- No file exceeds 200 lines; split by concern.
- Use `log/slog` with named attributes; message names use `noun.verb`. Levels: Info normal, Debug trace, Error failure.
- Every error wraps context: `fmt.Errorf("...%s: %w", id, err)`.
- Integration tests against real YugabyteDB are gated by an env DSN and skip when unset, matching `chain_append_integration_test.go` (`AUDIT_CHAIN_TEST_DSN`).
- Each implementation PR passes gpt-subagent, review-bugbot, and code-review, iterating until approved, then CI green, then merge. Validate prod-affecting steps on QA first.

---

### Task 1: Migration 004, install pg_partman, adopt audit.events, add the maintenance wrapper

**Files:**
- Create: `migrations/004_audit_partman.sql`
- Test: `internal/audit/partition_adopt_integration_test.go`

**Interfaces:**
- Consumes: existing `audit.events` (`PARTITION BY RANGE (event_time)`, weekly children named `events_YYYY_MM_DD`), role `audit_writer`.
- Produces: SQL function `audit.run_partition_maintenance()` (returns void, `SECURITY DEFINER`, EXECUTE granted to `audit_writer`) that runs pg_partman maintenance for `audit.events`; pg_partman registration of `audit.events` in `partman.part_config` with `premake = 12` and retention off.

The migration is adopted onto a table that already has children. pg_partman must not premake a week an existing child already covers, or the overlapping-range operation fails. The migration sets `p_start_partition` to the week after the latest existing child, so pg_partman only creates partitions beyond existing coverage. This needs no knowledge of pg_partman's internal child-naming and does not rename existing children. The existing children stay attached under their names, and pg_partman manages future weeks under its own names. Both are valid partitions; queries route by range.

- [ ] **Step 1: Write the failing integration test**

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

// partmanTestDSN names a migrated audit database (post-004) reachable for the
// adoption test. Skips when unset, mirroring chainTestDSNEnv.
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

// TestPartmanAdoptionCreatesFutureWeek verifies migration 004 registered
// audit.events with pg_partman, that the maintenance wrapper is callable, and
// that after maintenance a partition exists whose range covers a future week,
// with no overlap error. This is the adoption regression.
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

- [ ] **Step 2: Run the test to verify it fails**

Bring up the test database and apply migrations through 003 only (004 not yet written):

Run:
```bash
make test-fdb-down 2>/dev/null || true
docker compose -f docker-compose.test.yml up -d yugabyte
# wait for health, then migrate to 003, then:
AUDIT_CHAIN_TEST_DSN="postgres://yugabyte@127.0.0.1:5433/tack?sslmode=disable" \
  go test -tags integration ./internal/audit/ -run TestPartmanAdoptionCreatesFutureWeek -v
```
Expected: FAIL (part_config has no `audit.events` row; `audit.run_partition_maintenance()` does not exist).

- [ ] **Step 3: Write migration 004**

Explanatory comments use block-comment form. The `-- +goose` lines are goose directives and must stay exactly as shown.

```sql
-- +goose Up
-- +goose StatementBegin
/* Install pg_partman and register audit.events for native weekly partition
   management. Existing hand-named children (events_YYYY_MM_DD) remain attached.
   p_start_partition is set to the week after the latest existing child so
   pg_partman only creates partitions beyond existing coverage and never builds
   an overlapping range. Retention stays off: the audit ledger never auto-drops. */
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
    /* Highest existing partition upper bound, parsed from the partition bound. */
    SELECT max((regexp_match(pg_get_expr(c.relpartbound, c.oid),
                             'TO \(''([0-9-]+)'''))[1]::date)
      INTO max_upper
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_namespace n ON n.oid = p.relnamespace
     WHERE n.nspname = 'audit' AND p.relname = 'events';

    /* Start pg_partman at the later of the week after existing coverage and the
       current week, so a fresh database with no children also works. */
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
/* SECURITY DEFINER wrapper so the audit_writer role (INSERT only) can drive
   partition maintenance without holding CREATE on schema audit. The pinned
   search_path closes the definer-escalation risk. The wrapper targets only
   audit.events. */
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
/* Unregister audit.events from pg_partman. Leaves existing partitions in place. */
DELETE FROM partman.part_config WHERE parent_table = 'audit.events';
-- +goose StatementEnd
```

Note on the function owner: the migration runs as the database owner (`yugabyte`), which has CREATE on schema `audit` and owns the wrapper, so `SECURITY DEFINER` executes with sufficient privilege. If a QA run shows `run_maintenance` needs schema `partman` USAGE or EXECUTE that the owner lacks, add `GRANT USAGE ON SCHEMA partman TO <owner>` and `GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA partman TO <owner>` before the wrapper.

- [ ] **Step 4: Run the test to verify it passes**

Apply migration 004, then:

Run:
```bash
AUDIT_CHAIN_TEST_DSN="postgres://yugabyte@127.0.0.1:5433/tack?sslmode=disable" \
  go test -tags integration ./internal/audit/ -run TestPartmanAdoptionCreatesFutureWeek -v
```
Expected: PASS.

Contingency (only if the test shows pg_partman 4.7.4 rejects a parent with foreign-named children even with `p_start_partition` ahead): rename existing children to pg_partman's native weekly name before `create_parent`. Determine the exact name by inspecting `partman.show_partitions('audit.events')` on a fresh registration, then add `ALTER TABLE audit.events_YYYY_MM_DD RENAME TO <partman_name>` statements. Keep the behavior assertion above unchanged.

- [ ] **Step 5: Commit**

```bash
git add migrations/004_audit_partman.sql internal/audit/partition_adopt_integration_test.go
git commit -S -m "Register audit.events with pg_partman and add maintenance wrapper (TACK-333)"
```

---

### Task 2: Telemetry, partition headroom gauge and maintenance counter

**Files:**
- Modify: `internal/telemetry/metrics.go`
- Test: `internal/telemetry/metrics_test.go` (create if absent)

**Interfaces:**
- Produces: `telemetry.SetAuditPartitionHeadroomWeeks(weeks int64)` and `telemetry.IncAuditPartitionMaintenance(result string)` where result is `"ok"` or `"error"`.

- [ ] **Step 1: Write the failing test**

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

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/telemetry/ -run TestAuditPartitionMetrics -v`
Expected: FAIL (`auditPartitionHeadroomWeeks` undefined).

- [ ] **Step 3: Add the metrics**

In `internal/telemetry/metrics.go`, add to the audit metrics `var (...)` block:

```go
	// audit_partition_headroom_weeks gauge: count of future weekly partitions
	// beyond the one covering now(). A stalled partition-manager shows up here
	// before audit.events runs out of partitions.
	auditPartitionHeadroomWeeks = expvar.NewInt("tack_audit_partition_headroom_weeks")
	// audit_partition_maintenance_total{result="ok|error"}.
	auditPartitionMaintenanceTotal = expvar.NewMap("tack_audit_partition_maintenance_total")
```

And the helpers:

```go
// SetAuditPartitionHeadroomWeeks publishes the current count of future weekly
// partitions available beyond now().
func SetAuditPartitionHeadroomWeeks(weeks int64) { auditPartitionHeadroomWeeks.Set(weeks) }

// IncAuditPartitionMaintenance records one maintenance run outcome (result is
// "ok" or "error").
func IncAuditPartitionMaintenance(result string) { auditPartitionMaintenanceTotal.Add(result, 1) }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/telemetry/ -run TestAuditPartitionMetrics -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/telemetry/metrics.go internal/telemetry/metrics_test.go
git commit -S -m "Add audit partition headroom gauge and maintenance counter (TACK-333)"
```

---

### Task 3: PartitionManager worker

**Files:**
- Create: `internal/audit/partition_manager.go`
- Test: `internal/audit/partition_manager_test.go`

**Interfaces:**
- Consumes: `telemetry.SetAuditPartitionHeadroomWeeks`, `telemetry.IncAuditPartitionMaintenance` (Task 2).
- Produces: `partitionStore` interface (`RunMaintenance(ctx) error`, `HeadroomWeeks(ctx, now time.Time) (int, error)`); `NewPartitionManager(store partitionStore, period time.Duration) *PartitionManager`; `NewPGPartitionStore(pool *pgxpool.Pool) partitionStore`; methods `Start(ctx)`, `Close() error`. Constant `partitionHeadroomAlertFloor = 4`.

The manager takes a `partitionStore` interface so unit tests inject a fake with no database. The production store is pgx-backed. The manager reuses the consumer's pool through the store, and its `Close` does not close the pool, mirroring `Reconciler`.

- [ ] **Step 1: Write the failing test**

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

// TestPartitionManagerRunsOnceOnStart verifies boot catch-up: Start triggers one
// maintenance run before any tick fires.
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

// TestPartitionManagerCloseIdempotent verifies Close can be called twice safely.
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

// TestPartitionManagerRunErrorDoesNotPanic verifies a maintenance failure is
// swallowed (logged) and the worker keeps running.
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

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/audit/ -run TestPartitionManager -v`
Expected: FAIL (`NewPartitionManager` undefined).

- [ ] **Step 3: Write the worker**

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

// partitionHeadroomAlertFloor is the number of future weekly partitions below
// which the manager logs an alert. Twelve are premade; four weeks of warning is
// ample lead time to react before audit.events runs out.
const partitionHeadroomAlertFloor = 4

// partitionStore is the data-access seam for the partition-manager, so the loop
// is unit-testable without a database.
type partitionStore interface {
	// RunMaintenance premakes forward partitions for audit.events.
	RunMaintenance(ctx context.Context) error
	// HeadroomWeeks returns the count of weekly partitions whose range starts
	// after now, i.e. the forward buffer remaining.
	HeadroomWeeks(ctx context.Context, now time.Time) (int, error)
}

// PartitionManager keeps audit.events supplied with future weekly partitions.
// It mirrors Notarizer and Reconciler: a recovered goroutine that runs once at
// boot (catch-up) and then on a fixed period. It reuses the consumer's pool
// through the store, so Close does not close the pool.
type PartitionManager struct {
	store  partitionStore
	period time.Duration

	stop    chan struct{}
	stopped chan struct{}
}

// NewPartitionManager builds a manager over the given store. A non-positive
// period falls back to 24h.
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

// Start launches the maintenance loop in a recovered goroutine.
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
	/* Run once at boot so a freshly deployed or restarted consumer establishes
	   headroom and catches up missed weeks before the first tick. */
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

// Close stops the loop. Idempotent. Does not close the shared pool.
func (m *PartitionManager) Close() error {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	<-m.stopped
	return nil
}

// pgPartitionStore is the production partitionStore backed by the consumer's
// Yugabyte pool (audit_writer role). Maintenance goes through the SECURITY
// DEFINER wrapper installed by migration 004.
type pgPartitionStore struct {
	pool *pgxpool.Pool
}

// NewPGPartitionStore builds the production store.
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

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/audit/ -run TestPartitionManager -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/audit/partition_manager.go internal/audit/partition_manager_test.go
git commit -S -m "Add PartitionManager worker with headroom metric and alert (TACK-333)"
```

---

### Task 4: Config and consumer wiring

**Files:**
- Modify: `internal/audit/consumer.go` (`ConsumerConfig`, `applyConsumerDefaults`, `Consumer` struct, `NewConsumer`, `Start`, `Close`)
- Modify: `cmd/audit-consumer/main.go` (`consumerEnv`, `NewConsumer` call)
- Test: `internal/audit/consumer_config_test.go`

**Interfaces:**
- Consumes: `NewPartitionManager`, `NewPGPartitionStore` (Task 3).
- Produces: `ConsumerConfig.PartitionPeriod time.Duration`; env `AUDIT_CONSUMER_PARTITION_PERIOD` (default `24h`).

- [ ] **Step 1: Write the failing test**

```go
package audit

import (
	"testing"
	"time"
)

// TestApplyConsumerDefaultsPartitionPeriod pins the 24h default and that an
// explicit value is preserved.
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

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/audit/ -run TestApplyConsumerDefaultsPartitionPeriod -v`
Expected: FAIL (`PartitionPeriod` field does not exist).

- [ ] **Step 3: Wire the config and the worker**

In `internal/audit/consumer.go`:

Add to `ConsumerConfig` (after `SummaryEvery`):

```go
	// PartitionPeriod is how often the partition-manager runs pg_partman
	// maintenance for audit.events. Zero falls back to 24h.
	PartitionPeriod time.Duration
```

Add to `applyConsumerDefaults` (before `return cfg`):

```go
	if cfg.PartitionPeriod <= 0 {
		cfg.PartitionPeriod = 24 * time.Hour
	}
```

Add to the `Consumer` struct (after `reconciler *Reconciler`):

```go
	partitions *PartitionManager
```

In `NewConsumer`, after the reconciler is constructed and before `return c, nil`:

```go
	c.partitions = NewPartitionManager(NewPGPartitionStore(ybpool), cfg.PartitionPeriod)
```

In `Consumer.Start`, after the reconciler start block:

```go
	if c.partitions != nil {
		c.partitions.Start(ctx)
	}
```

In `Consumer.Close`, after the reconciler close block:

```go
	if c.partitions != nil {
		_ = c.partitions.Close()
	}
```

In `cmd/audit-consumer/main.go`, add to `consumerEnv`:

```go
	PartitionPeriod time.Duration `env:"AUDIT_CONSUMER_PARTITION_PERIOD" envDefault:"24h"`
```

And to the `audit.ConsumerConfig{...}` literal in `run`:

```go
		PartitionPeriod: cfg.PartitionPeriod,
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/audit/ -run TestApplyConsumerDefaultsPartitionPeriod -v`
Expected: PASS.

- [ ] **Step 5: Build to confirm wiring compiles**

Run: `make build`
Expected: success (vet, golangci, staticcheck, govulncheck clean).

- [ ] **Step 6: Commit**

```bash
git add internal/audit/consumer.go cmd/audit-consumer/main.go internal/audit/consumer_config_test.go
git commit -S -m "Wire PartitionManager into the audit-consumer (TACK-333)"
```

---

### Task 5: Integration regression, insert at now() succeeds after boot

**Files:**
- Test: `internal/audit/partition_manager_integration_test.go`

**Interfaces:**
- Consumes: `NewPGPartitionStore`, `NewPartitionManager` (Task 3); `audit.run_partition_maintenance()` (Task 1); `partmanTestPool` (Task 1).

This is the direct regression for the incident: with no future partition, one manager run must create one, and an insert dated in that future week must succeed.

- [ ] **Step 1: Write the failing test**

```go
//go:build integration

package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPartitionManagerHealsFutureWeek drops future partitions, runs the manager
// once, and asserts an insert at a future week succeeds. Regression for the
// 2026-07-06 partition-exhaustion incident (TACK-333).
func TestPartitionManagerHealsFutureWeek(t *testing.T) {
	pool := partmanTestPool(t)
	ctx := context.Background()

	target := time.Now().UTC().AddDate(0, 0, 7*20) // 20 weeks out

	/* Drop any partition covering the target week to create the failure state. */
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

- [ ] **Step 2: Run the test to verify it passes and is meaningful**

Against a database migrated through 004:

Run:
```bash
AUDIT_CHAIN_TEST_DSN="postgres://yugabyte@127.0.0.1:5433/tack?sslmode=disable" \
  go test -tags integration ./internal/audit/ -run TestPartitionManagerHealsFutureWeek -v
```
Expected: PASS. To confirm the test is meaningful, temporarily comment out the `pm.Start` line and re-run; expected FAIL (the insert keeps erroring because nothing creates the partition). Restore the line.

- [ ] **Step 3: Commit**

```bash
git add internal/audit/partition_manager_integration_test.go
git commit -S -m "Add partition-manager heal-future-week integration regression (TACK-333)"
```

---

### Task 6: QA validation, then prod rollout

**Files:** none (operational).

This task validates the change on QA (disposable) before prod, per the production change protocol.

- [ ] **Step 1: Run the full test suite locally**

Run: `make test-unit` then `make test-integration`
Expected: all pass, including the new adoption and heal-future-week integration tests.

- [ ] **Step 2: Merge after review gates**

Confirm gpt-subagent, review-bugbot, and code-review approve, and CI is green, then merge the PR to `main`.

- [ ] **Step 3: Deploy to QA and validate adoption and boot heal**

Deploy the tack stack to `tack_qa` at the merged ref (this runs `./server migrate`, applying 004). Then verify on the QA database:
- `SELECT count(*) FROM partman.part_config WHERE parent_table='audit.events'` returns 1.
- `SELECT retention FROM partman.part_config WHERE parent_table='audit.events'` is NULL.
- The audit-consumer log shows the partition worker ran on boot (`audit.partition.maintained`).
- `tack_audit_partition_headroom_weeks` in `/debug/vars` is at least 12.
- Recreate QA once and confirm a fresh environment self-heals (partitions present, headroom gauge populated after boot).

- [ ] **Step 4: Deploy to prod and verify**

Deploy the tack stack to `tack_servers`. On prod (CT 117), verify:
- pg_partman adopted `audit.events` (part_config row present, retention NULL) without disturbing the stopgap partitions through 2026-09-14.
- The audit-consumer boot run logged `audit.partition.maintained` and `tack_audit_partition_headroom_weeks` is at least 12.
- A future-week partition beyond 2026-09-14 now exists (pg_partman premade forward).
- No `no partition of relation "events"` errors appear after the deploy.

- [ ] **Step 5: Close the ticket**

Update TACK-333 with the QA and prod validation evidence (part_config state, headroom gauge value, the premade future partitions), and note that the stopgap is now superseded by automated maintenance.

---

## Self-review

**Spec coverage:**
- pg_partman engine, create_parent, 12-week premake, retention off: Task 1.
- SECURITY DEFINER wrapper and audit_writer EXECUTE grant: Task 1.
- Adoption of existing hand-named partitions without overlap: Task 1 (p_start_partition) plus adoption integration test, with a rename contingency.
- In-process scheduler goroutine (boot plus daily tick), mirrors Notarizer and Reconciler: Task 3.
- Reuses consumer pool, Close does not close pool: Task 3 (store holds pool) plus Task 4 (wiring).
- Headroom gauge, low-headroom alert, maintenance failure counter: Task 2 (metrics) plus Task 3 (usage).
- Config `AUDIT_CONSUMER_PARTITION_PERIOD` default 24h: Task 4.
- Unit tests (loop runs on boot, ticks, idempotent Close, error path): Task 3.
- Integration test (insert at now() succeeds after boot on a DB with no future partition): Task 5.
- QA-first rollout: Task 6.
- Non-goals (no pg_cron, no retention, no DLQ) respected across all tasks.

**Placeholder scan:** No TBD or TODO. The one version-specific unknown (pg_partman 4.7.4 adoption behavior) is resolved by the Task 1 integration test with a concrete contingency, not deferred.

**Type consistency:** `partitionStore` (`RunMaintenance`, `HeadroomWeeks`) is defined in Task 3 and consumed by Tasks 3 and 5. `NewPartitionManager`, `NewPGPartitionStore`, `Start`, `Close` names match across Tasks 3, 4, 5. `SetAuditPartitionHeadroomWeeks` and `IncAuditPartitionMaintenance` names match between Task 2 and Task 3. `ConsumerConfig.PartitionPeriod` and env `AUDIT_CONSUMER_PARTITION_PERIOD` match between Task 4's config test, consumer, and main. `partmanTestPool` is defined in Task 1 and reused in Task 5.
