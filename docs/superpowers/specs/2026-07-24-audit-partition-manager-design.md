# Audit events partition-manager (TACK-333)

## Summary

`audit.events` is range-partitioned by week. Nothing creates new weekly
partitions, so they ran out on 2026-07-06 and every audit insert failed silently
for about two and a half weeks. This design adds an owned, self-healing
partition-manager so `audit.events` always has a partition covering the current
week plus a forward buffer, and never runs out again.

The partition engine is pg_partman (the standard PostgreSQL partition-management
extension, available on our YugabyteDB build). The scheduler is a goroutine in
the audit-consumer process that calls pg_partman maintenance on boot and on a
daily tick. This keeps partition maintenance in the process we already run, log,
and alert on, with boot catch-up as a structural safety net that re-verifies
headroom on every deploy.

## Background and root cause

`audit.events` is declared `PARTITION BY RANGE (event_time)` with one child
partition per week, named `events_YYYY_MM_DD` (migration `002_audit.sql`). That
migration seeded the current week plus eight ahead and left a helper
`audit.ensure_events_partition(DATE)`, with a comment claiming a goroutine in the
audit package would create each new weekly partition. That goroutine was never
built: no Go code ever called the helper. The nine seeded weeks were consumed by
2026-07-06, after which inserts failed with
`no partition of relation "events" found for row (SQLSTATE 23514)`.

The failure was silent because the recording layer logged and continued; the
missing partition was not surfaced as a page. The incident is recorded in
TACK-334 (the resulting audit gap) and TACK-336 (durable dead-letter so unwritable
events are never lost). This ticket removes the recurring cause.

## Goals

- `audit.events` always has a partition covering `now()` plus at least a
  configured forward buffer (12 weeks).
- Restarting the audit-consumer re-establishes headroom on boot, catching up any
  weeks missed while it was down.
- A headroom metric exists, and headroom falling below a floor raises an alert
  before partitions are exhausted.
- Adopting pg_partman does not disturb the existing partitions, the per-`(org,
  shard)` hash chain, or the running audit write path.

## Non-goals

- Retention or pruning of old partitions. The audit ledger is a compliance record
  and must never auto-drop data. pg_partman retention stays off.
- Changing the weekly granularity or the `(org, shard)` hash sub-layout.
- The durable dead-letter queue for unwritable events (TACK-336).
- Enabling pg_cron. Scheduling stays in the application process, so no cluster
  gflag change and no YugabyteDB restart.

## Verified environment facts

Checked on prod (`tack-yugabyte-1`) 2026-07-24:

- YugabyteDB is PostgreSQL 11.2-based, build `2024.2.8.0`.
- `pg_partman` 4.7.4 and `pg_cron` 1.6 are available (not installed).
- The `tack` database is not colocated, so pg_partman maintenance is supported.
- `pg_cron` is not enabled (no `cron` schema); this design does not need it.
- Existing partitions are named `events_2026_04_27`, `events_2026_05_04`, and so
  on (hand-rolled scheme, not pg_partman's `events_p*`).

## Design

### Component: pg_partman registration (migration `004_audit_partman.sql`)

Install pg_partman and register `audit.events` for native partition management.

```sql
CREATE SCHEMA IF NOT EXISTS partman;
CREATE EXTENSION IF NOT EXISTS pg_partman WITH SCHEMA partman;

SELECT partman.create_parent(
    p_parent_table => 'audit.events',
    p_control      => 'event_time',
    p_type         => 'native',
    p_interval     => '1 week',
    p_premake      => 12
    -- p_start_partition set from the adoption prototype (see below)
);

-- Compliance: never auto-drop audit data.
UPDATE partman.part_config
SET    retention = NULL,
       retention_keep_table = true,
       infinite_time_partitions = true
WHERE  parent_table = 'audit.events';
```

`p_premake => 12` keeps twelve future weekly partitions ahead of the current
week. `infinite_time_partitions = true` keeps maintenance creating future
partitions regardless of whether recent ones received rows.

### Adoption of existing partitions (prototype on QA first)

The existing children are named `events_YYYY_MM_DD`; pg_partman 4.7.4 uses its own
`events_p*` scheme. Two partitions cannot cover the same week, so
`create_parent` must not premake a week an existing child already covers, or the
overlapping-range insert fails.

The exact 4.7.4 weekly child-naming and the exact behavior of `create_parent`
against pre-existing children are version-specific. Determine them empirically on
QA (disposable, recreated to mirror prod's partition layout), then codify the
verified sequence in the migration. The two candidate strategies:

1. **Rename to match.** Rename each existing `events_YYYY_MM_DD` child to
   pg_partman's 4.7.4 native weekly name (an `ALTER TABLE ... RENAME`,
   metadata-only), then `create_parent` recognizes them and premakes forward with
   no overlap.
2. **Start ahead.** Leave the existing children under their current names as
   valid attached partitions, and set `p_start_partition` to the first week not
   already covered, so pg_partman premakes only beyond the existing coverage.

Prefer whichever the QA prototype shows adopts cleanly with zero range overlap and
no data movement. Record the verified naming string and the chosen strategy in the
migration comments.

Adoption is metadata-only: native partitioning does not move data, the child
tables already exist, and YugabyteDB disables the `create_parent` ACCESS
EXCLUSIVE lock, so this does not rewrite the 400k-row table.

### Component: privilege wrapper (same migration)

The audit-consumer connects as `audit_writer`, which has INSERT/SELECT/UPDATE
only and no CREATE on schema `audit`. pg_partman `run_maintenance` performs DDL,
so the writer role cannot call it directly. Add a `SECURITY DEFINER` wrapper
owned by a role that has CREATE on schema `audit` (the migration owner) and grant
EXECUTE to `audit_writer`:

```sql
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

REVOKE ALL ON FUNCTION audit.run_partition_maintenance() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.run_partition_maintenance() TO audit_writer;
```

The pinned `search_path` closes the SECURITY DEFINER escalation risk. The wrapper
targets only `audit.events`, so the writer cannot drive maintenance on any other
table. The wrapper's owner must also hold USAGE on schema `partman` and EXECUTE on
the pg_partman maintenance functions; grant these in the migration.

YugabyteDB disables the advisory locks pg_partman would use to serialize
concurrent maintenance. A single goroutine calls the wrapper serially, so
concurrency is not a concern.

### Component: PartitionManager goroutine (`internal/audit/partition_manager.go`)

A background worker that mirrors the existing `Notarizer` and `Reconciler`:
`New`, `Start`, `loop`, `runOnce`, `Close`, with a recovered goroutine and an
idempotent `Close`.

- `NewPartitionManager(pool *pgxpool.Pool, period time.Duration)` reuses the
  consumer's Yugabyte pool (as `audit_writer`).
- `Start(ctx)` launches the loop and runs once immediately, so a fresh or
  restarted consumer establishes headroom before the first tick.
- `runOnce(ctx)` calls `SELECT audit.run_partition_maintenance()`, then reads the
  current headroom (count of future partitions beyond `now()`) and publishes it.
- `loop(ctx)` ticks every `period` (default 24h) and calls `runOnce`, honoring
  `stop` and `ctx.Done()`.
- Wire it into the consumer next to the notarizer and reconciler: construct in
  `NewConsumer`, `Start` in `Consumer.Start`, `Close` in `Consumer.Close`.

Because `run_maintenance` premakes forward to `p_premake` weeks, one call on boot
catches up every week missed during downtime.

### Observability

- **Headroom gauge.** After each `runOnce`, count future partitions beyond
  `now()` and publish an expvar gauge through the existing `telemetry` package,
  consistent with the other audit workers' metrics.
- **Low-headroom alert.** When headroom falls below a floor (4 weeks), log at
  ERROR with a distinct message and raise the same alert signal the notarizer and
  reconciler use, so a stalled manager is caught before exhaustion.
- **Maintenance failure.** A failed `runOnce` logs ERROR and increments a failure
  counter. The failure does not crash the consumer; the next tick and the next
  boot retry.

The missing-partition insert failure itself (`SQLSTATE 23514`) remains ERROR in
`appendChainRow`; making unwritable events durable is TACK-336, kept separate.

### Configuration

- `AUDIT_CONSUMER_PARTITION_PERIOD` (default `24h`): maintenance tick interval.

The forward buffer (12 weeks) lives in `part_config.premake`, set by the
migration, not in application config. The low-headroom alert floor (4 weeks) is a
constant.

## Testing

- **Unit (`internal/audit/partition_manager_test.go`).** With a stubbed
  maintenance call, verify the loop runs once on start, ticks on the period,
  computes headroom from a partition list, raises the alert below the floor, and
  that `Close` is idempotent and stops the loop.
- **Integration (`AUDIT_CHAIN_TEST_DSN`, real YugabyteDB).** After installing
  pg_partman and registering `audit.events`, drop all future partitions, run the
  manager once, and assert a future partition now exists and an insert at `now()`
  succeeds. This is the direct regression for the incident.

## Rollout

1. Prototype the pg_partman adoption on QA (recreate QA to mirror prod's partition
   layout, including a stale last partition), determine the exact 4.7.4 weekly
   naming and the clean adoption strategy, and confirm zero range overlap and no
   data movement.
2. Finalize migration `004` from the verified sequence. Build and test in CI.
3. Deploy to QA: confirm the extension installs, existing partitions adopt,
   `run_maintenance` premakes forward, the manager runs on boot, and the headroom
   gauge reports the expected value. Recreate QA once more to confirm a fresh
   environment self-heals.
4. Deploy to prod. The stopgap partitions through 2026-09-14 already exist;
   pg_partman adopts them (or premakes beyond them) and the manager takes over
   maintenance. Verify the headroom gauge and that boot catch-up ran.

## Review gates

Per the working agreement, the implementation PR passes gpt-subagent,
review-bugbot, and code-review, iterating until approved, then CI green, then
merge. Destructive and prod steps validate on QA first.

## Alternatives considered

- **pg_partman + pg_cron (fully canonical).** Schedules `run_maintenance` inside
  the database. Rejected for this ticket because enabling pg_cron needs the
  `enable_pg_cron` gflag on master and tserver, which requires a YugabyteDB
  restart (prod is single-node, so a brief auth and audit outage), and because a
  DB-side cron job that silently stops firing reproduces the exact unmonitored-
  scheduler failure that caused this incident, unless separately alerted on
  `cron.job_run_details`. Revisit if a general in-database scheduler is wanted for
  many future jobs.
- **Pure Go loop over the hand-rolled `ensure_events_partition`.** No extension,
  leanest, but reimplements the partition logic pg_partman already provides and
  gives up the standard tooling. Rejected in favor of adopting the ecosystem
  standard while keeping scheduling in-process.
