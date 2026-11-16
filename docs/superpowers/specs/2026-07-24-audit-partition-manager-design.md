# Maintain audit event partitions (TACK-333)

`audit.events` uses weekly range partitions. The initial migration created the
current week and eight future weeks but application code never called
`audit.ensure_events_partition(DATE)` to create more. Partitions ended on
2026-07-06. Every audit insert then failed for about two and a half weeks with
`no partition of relation "events" found for row (SQLSTATE 23514)`.
The recorder logged failures and continued without paging an operator.
TACK-334 covers the gap; TACK-336 makes unwritable events durable.

This design registers the table with pg_partman, the PostgreSQL partition
management extension, and schedules maintenance in audit-consumer. The worker
runs on startup and daily to restore future partition coverage after downtime.

## Requirements

- Keep a partition for `now()` and a configured forward buffer of 12 weeks.
- Restore coverage on each audit-consumer startup.
- Publish future-partition count and alert below four weeks before exhaustion.
- Preserve existing partitions, `(org, shard)` hash chains, and audit writes.
- Disable automatic retention and pruning of compliance data.
- Preserve weekly granularity and the `(org, shard)` hash sub-layout.
- Keep scheduling in the application. Do not enable pg_cron, change cluster
  gflags, or restart YugabyteDB for this work.
- Implement the durable dead-letter queue separately under TACK-336.

## Verified engine capabilities

The 2026-07-24 production inspection of `tack-yugabyte-1` established these facts:

- YugabyteDB build `2024.2.8.0` uses PostgreSQL 11.2.
- The image supplies pg_partman 4.7.4 and pg_cron 1.6, but neither extension
  has an installation in the database.
- The non-colocated `tack` database supports pg_partman maintenance.
- The database does not contain a `cron` schema.
- Existing partitions use `events_2026_04_27`, `events_2026_05_04`, and similar
  dates rather than pg_partman's `events_p*` convention.

## Register pg_partman

Migration `004_audit_partman.sql` installs and registers native management:

```sql
CREATE SCHEMA IF NOT EXISTS partman;
CREATE EXTENSION IF NOT EXISTS pg_partman WITH SCHEMA partman;

SELECT partman.create_parent(
    p_parent_table => 'audit.events',
    p_control      => 'event_time',
    p_type         => 'native',
    p_interval     => '1 week',
    p_premake      => 12
    -- Set p_start_partition from the adoption prototype below.
);

-- Disable automatic deletion of compliance data.
UPDATE partman.part_config
SET    retention = NULL,
       retention_keep_table = true,
       infinite_time_partitions = true
WHERE  parent_table = 'audit.events';
```

`p_premake => 12` creates twelve future weekly partitions.
`infinite_time_partitions = true` continues creation even when recent partitions
have not received rows.

Prototype adoption on disposable QA with production's partition layout and a
stale final partition. Determine the exact 4.7.4 weekly naming and
`create_parent` behavior. Do not create overlapping weekly ranges. Choose the
strategy that adopts existing data without copying it or overlapping ranges:

1. Rename each existing child with `ALTER TABLE ... RENAME` to pg_partman's
   native weekly convention, then let `create_parent` recognize it.
2. Keep existing attached children and set `p_start_partition` to the first
   uncovered week, limiting new partitions to that week and later.

Record the verified naming string and chosen strategy in migration comments.
Native partition adoption changes metadata rather than rewriting the existing
400k rows. YugabyteDB disables `create_parent`'s ACCESS EXCLUSIVE lock.

## Grant bounded maintenance access

`audit_writer` has INSERT, SELECT, and UPDATE privileges but cannot create audit
schema objects. Add a migration-owner `SECURITY DEFINER` wrapper and grant the
writer execution rights:

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

Pin `search_path` to prevent privilege escalation through object substitution.
The wrapper permits maintenance only on `audit.events`. Grant its owner USAGE
on `partman` and EXECUTE on the maintenance functions.

YugabyteDB disables pg_partman's advisory locks. Run the wrapper serially in
one goroutine to avoid concurrent maintenance.

## Run PartitionManager

Implement `internal/audit/partition_manager.go` with the existing `Notarizer`
and `Reconciler` lifecycle: `New`, `Start`, `loop`, `runOnce`, and idempotent
`Close`. Recover goroutine panics through the existing worker mechanism.

- `NewPartitionManager(pool *pgxpool.Pool, period time.Duration)` reuses the
  consumer's `audit_writer` pool.
- `Start(ctx)` starts the loop and invokes maintenance immediately.
- `runOnce(ctx)` calls `SELECT audit.run_partition_maintenance()`, counts
  partitions after `now()`, and publishes headroom.
- `loop(ctx)` runs every `period` and honors `stop` and `ctx.Done()`.
- Construct the manager in `NewConsumer`, start it in `Consumer.Start`, and
  close it in `Consumer.Close` beside the notarizer and reconciler.

A startup maintenance call creates the configured forward buffer even after
weeks of downtime.

## Record health and configure the interval

Publish an expvar headroom gauge through telemetry after every `runOnce`.
Below four weeks, log ERROR with a distinct message and raise the existing
notarizer/reconciler alert signal. On maintenance failure, log ERROR and
increment a failure counter without crashing the consumer. Retry on the next
tick or startup. Keep missing-partition failures at ERROR in `appendChainRow`.

`AUDIT_CONSUMER_PARTITION_PERIOD` sets the tick interval and defaults to `24h`.
The migration sets the 12-week buffer in `part_config.premake`; application
configuration does not override it. Use a constant for the four-week alert floor.

## Verify and release

Unit tests in `internal/audit/partition_manager_test.go` use a stubbed
maintenance call to check startup execution, periodic execution, headroom
calculation, low-headroom alerts, shutdown, and repeated `Close` calls.

The `AUDIT_CHAIN_TEST_DSN` integration test uses real YugabyteDB. Install and
register pg_partman, drop future partitions, run maintenance once, and require
a future partition and successful insert at `now()`. This reproduces the incident.

1. Complete the QA adoption prototype and prove zero overlapping ranges without
   copying data.
2. Finalize migration `004` from that sequence. Build and test in CI.
3. Deploy on QA. Verify installation, adoption, future creation, startup
   maintenance, and the expected headroom gauge. Recreate QA and verify fresh
   initialization too.
4. Deploy on production. Adopt or create beyond the stopgap partitions through
   2026-09-14. Verify the gauge and startup maintenance.

Require implementation review through `gpt-subagent`, `review-bugbot`, and
`code-review` until approval, followed by passing CI before merge. Validate
all destructive and production steps on QA first.

## Alternatives

The design rejects pg_cron for this ticket. Its `enable_pg_cron` master and
tserver gflags require a database restart and an auth/audit outage on the
single-node production deployment. A database scheduler also needs independent
monitoring of `cron.job_run_details` to detect silent stoppage. Reconsider it
when several jobs require an in-database scheduler.

A Go loop around `ensure_events_partition` would avoid an extension but
reimplement pg_partman's partition logic and omit its standard tooling.
The design uses pg_partman with in-process scheduling instead.
