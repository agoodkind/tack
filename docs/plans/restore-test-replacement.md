# Plan: Replace Restore-Test With Engine-Native Continuous Backup

## Context

The existing `internal/ops/backup_restore_*.go` scratch-cluster path turns out to be a smoke test, not a recovery rehearsal. It validates that a dump file is structurally replayable somewhere; it does not exercise the actual procedure that a real restore would use. The Yugabyte path uses vanilla `psql` against `postgres:16-alpine` while production runs `ysqlsh` against Yugabyte, and those two engines reject each other's syntax. All four scratch paths target fresh containers with default volumes rather than the production volume names that a real restore would land in. Service ordering between FoundationDB, Yugabyte, Temporal-DB, Meilisearch, the app, and the audit-consumer is never exercised. The whole subsystem needs to come out.

The replacement has to align with the project rule that everything is built for horizontal scale from day zero. That rules out single-host-only solutions.

## Decisions Already Settled With The Operator

The operator confirmed the following choices on 2026-05-10 during this planning conversation:

- The work order is continuous backup first, then the recovery drill later. Continuous backup runs against production engines and shrinks the recovery-point window from roughly a day to a few minutes. The drill that exercises the full recovery procedure is deferred until the QA environment exists on suburban (TACK-235).
- The drill itself is out of scope right now because QA does not yet exist, so its blocking semantics do not need a decision today.
- The scratch-restore code gets deleted now in a single commit. The repo does not need to keep broken validation code around while the replacement is built.
- The audit chain concurrency hazard noticed during this investigation gets filed as its own ticket, with concrete evidence from the migration files.

## Findings From Investigation

### Audit chain compatibility with point-in-time restore

The `chain_heads` upsert lives inside the same transaction as the `audit.events` insert (`internal/audit/yugabyte.go:101-214`). A Yugabyte point-in-time restore captures a consistent pair of events and chain-head rows. Notarizations created before the restore-target time stay mathematically valid because their Merkle roots were signed over chain heads that existed at signing time, and that math does not change. Notarizations created after the restore-target time are discarded by the restore, and the next notarizer run after the restore creates fresh ones. The 60-second notarizer cadence means up to a minute of events at any moment may have valid hash chains but no Merkle proof yet.

### Audit chain concurrency hazard (separate bug, real)

`Record()` reads `chain_heads` without a `FOR UPDATE` lock at `internal/audit/yugabyte.go:138-141`, then writes a new chain head via `INSERT ... ON CONFLICT DO UPDATE`. The events PRIMARY KEY in `migrations/002_audit.sql:56` is `((org_id, shard) HASH, event_time ASC, seq ASC, event_id ASC)`, and the UNIQUE constraint added in `migrations/004_audit_events_event_id_uniq.sql` covers `event_id` alone. Neither catches a race because `event_time` is `time.Now()` at insert and `event_id` is a fresh UUIDv7 per call, so they always differ between two concurrent calls. Two concurrent `Record()` calls for the same `(org_id, shard)` can both observe the same `lastSeq` and `lastHash`, both insert at `seq = lastSeq + 1` with the same `prev_hash`, then both upsert `chain_heads` where last-writer-wins. One row's `row_hash` becomes the chain head; the other row exists in `audit.events` with the same `prev_hash` and same `seq` but is never referenced by any future hash. Chain-walk verification skips it; notarization for that window does not cover it. The cleanest fix is a UNIQUE constraint on `(org_id, shard, seq)` so one of the two inserts fails and retries. An alternative is `SELECT ... FOR UPDATE` on the chain_heads row to linearize. Phase 2 wave 2 cutover makes this less urgent because the consumer becomes the single writer of chain_heads, but the bug is real today.

### FoundationDB continuous mode

FDB's continuous backup mode is engine-native and N-node by design ([Apple FDB docs](https://apple.github.io/foundationdb/backups.html)). The current `tack-backup-agent` sidecar already participates. Adding nodes adds agents. Continuous mode pushes rolling snapshots and log files to a destination URL and is restorable to any moment after the first snapshot. The IPv6 overlay at `fdb-overlay/fdb.bash` is required for any FDB container on an IPv6-only host like CT 117, so the overlay needs to be part of the standard image build rather than a per-restore mount.

### YugabyteDB continuous PITR

Yugabyte 2024.2 ships engine-native PITR via snapshot schedules ([Yugabyte docs](https://docs.yugabyte.com/stable/manage/backup-restore/point-in-time-recovery/)). A schedule defines a retention window and a snapshot interval, and the engine takes copy-on-write snapshots at that cadence. Restores have microsecond precision within the retention window. Snapshots are partition-table-aware, so the weekly partitions on `audit.events` are restored together as one atomic unit.

### Temporal-DB continuous archive

The Temporal database is plain Postgres 16 in the `postgres:16-alpine` image. WAL archiving plus a base backup is the standard Postgres PITR shape. Postgres needs `archive_mode=on` and an `archive_command` that copies finished WAL files to a destination, plus a periodic `pg_basebackup`.

### Meilisearch is snapshot-only

Meilisearch only supports periodic snapshots, not continuous PITR ([Meilisearch docs](https://meilisearch.com/docs/learn/data_backup/snapshots_vs_dumps)). For Tack the search index is derivable from canonical data in FDB and Yugabyte, so a worst-case fallback is to rebuild the index from source rather than to restore it byte-for-byte. Meilisearch RPO is bounded by snapshot interval.

### Service ordering during a real restore

The app `depends_on` FDB, Yugabyte, Meilisearch, and Temporal all healthy (`docker-compose.yml`). Temporal `depends_on` Temporal-DB healthy. Audit-consumer `depends_on` Kafka, Yugabyte, ClickHouse healthy. Compose's `service_healthy` covers process-up, not data-fully-restored. A real restore procedure has to bring data stores up first, wait for the engine-level readiness signal, and then bring up dependent services in order.

## Now-Work (Layer 1, Continuous Backup)

### FoundationDB

Switch the running `tack-backup-agent` sidecar from on-demand mode to continuous mode. The agent already exists; the change is configuration. Required:

- A configuration field for the destination URL where rolling snapshots and logs land. Likely a local on-host path on CT 117 today, with object-storage paths added later when the system goes multi-host.
- A snapshot interval and a log retention period.
- A health check that the destination is producing files at the expected cadence.

### Yugabyte

Create a PITR snapshot schedule on the `tack` database via `yb_admin create_snapshot_schedule`. Required:

- Initial proposal: 1-hour interval, 7-day retention, applied to schemas `public` and `audit`.
- Schedule creation as a one-shot ops subcommand (`./server ops backup yb-pitr-init`) so the action is checked into Tack rather than living as a host-level imperative.
- A health check that snapshots are being taken at the expected cadence.

### Temporal-DB

Enable WAL archiving on the `tack-temporal-db-1` Postgres container. Required:

- Update the Postgres command line or the Compose service env to set `archive_mode=on` and `archive_command='cp %p /var/lib/postgresql/wal-archive/%f'` (or equivalent to a host-mounted path).
- Add a `wal-archive` volume to the Compose service.
- Schedule `pg_basebackup` runs at a documented cadence.

### Meilisearch

No continuous mode. Tighten the existing snapshot cadence if needed. Document the rebuild-from-canonical fallback.

## What Gets Deleted In A Standalone Commit

- `internal/ops/backup_restore.go` (the orchestrator)
- `internal/ops/backup_restore_fdb.go` (scratch FDB)
- `internal/ops/backup_restore_pg.go` (scratch Postgres path used for Yugabyte and Temporal)
- `internal/ops/backup_restore_meili.go` (scratch Meilisearch)
- `Makefile` target `backup-restore-test`
- The historic shell-script equivalent at `scripts/backup-restore-test.sh` if it still exists in the tree
- Any helper functions (e.g., `runOneShot` callers from these files; check for other callers before removing the helper itself)
- Any references in HANDOFF.md, retro_log, or other docs that point at the scratch path

The `make backup-verify` command stays in the tree. It is worth being clear about what it actually does and does not prove, because earlier framing in this plan made it sound stronger than it is.

What `backup-verify` checks today, against any backup directory it is pointed at: every file's SHA256 matches the value the manifest recorded at backup time, and every file matches a small per-category shape pattern. The shape patterns are real for FoundationDB archives and for the Yugabyte and Meilisearch volume tars, because those check for engine-specific markers that catch the 2026-04-25 empty-backup signature. The shape pattern for the SQL dumps from Yugabyte and Temporal-DB is much weaker: it reads the first sixteen megabytes of the dump and asserts the substring `CREATE TABLE` appears somewhere in it. Anything that vaguely looks like SQL passes that check. A dump with the right schema but content that would never actually restore would still pass.

What `backup-verify` does not check: it never opens any database engine, so it cannot tell whether a dump file would actually load into the engine it came from. The hash check compares each file to its own manifest entry, so it catches a file that got corrupted or modified after the backup was written, but it cannot catch a backup that was bad from the start.

Verify keeps running on every backup because it does catch a real class of failure (corrupted file on disk, empty backup, or the wrong file type sitting under the right name). The stronger gate of "the engine can actually consume this" only arrives once the engine-native continuous-restore path lands.

## Critical Files

- `internal/config/config.go` — add continuous-backup configuration fields under the existing `Backup*` block. New fields likely include `BackupFDBContinuous bool`, `BackupFDBDestination string`, `BackupFDBSnapshotInterval time.Duration`, plus equivalents for Yugabyte and Temporal.
- `internal/ops/backup_run.go` — drop scratch-restore orchestration; keep snapshot-export path; possibly add new subcommand wrappers for `yb-pitr-init` and `pg-archive-init`.
- `internal/ops/backup_restore*.go` — delete in the standalone commit.
- `Makefile` — drop `backup-restore-test`; possibly add per-engine PITR-init targets that wrap the new ops subcommands.
- `docker-compose.yml` (root) — add `wal-archive` volume on `temporal-db`, archive-mode environment; the FDB backup-agent already exists and gains continuous-mode configuration via env or command args.
- `docs/runbooks/recovery.md` — does not exist; needs creation as part of this work, capturing the documented recovery procedure that the future drill will execute.

## Tickets To File

- **Audit chain race in `Record()` at `yugabyte.go:138-141`**. Concurrent writes for the same `(org_id, shard)` can produce orphan events that break chain integrity. Cleanest fix is a UNIQUE constraint on `(org_id, shard, seq)`. File now.
- **FDB continuous mode rollout**. The actual implementation under Layer 1 above.
- **Yugabyte PITR schedule rollout**. Same.
- **Temporal-DB WAL archive rollout**. Same.
- **Meilisearch fallback documentation**. Document the rebuild-from-canonical procedure for the search index.
- **Drill subcommand and runbook (deferred behind QA)**. Tracks the Layer-2 work that comes after TACK-235 stands up.

## Verification Plan

For the deletion commit:

- The repo no longer references scratch-restore code anywhere.
- `make build` and `make test` pass.
- `make backup` and `make backup-verify` still work end-to-end against CT 117 against the existing artifact at `tack-20260510T192826Z`.

For each Layer 1 engine rollout:

- The destination is producing the expected artifacts at the expected cadence, observable via filesystem listing or the engine's own status command.
- A spot-check restore of a recent point-in-time into a manually-created scratch instance succeeds and shows expected data.
- The configuration survives a restart of the production stack without manual intervention.

The Layer-2 drill that walks through the full procedure end-to-end is deferred until QA exists.

## Handoff Doc Rewrite (Deferred)

Once Layer 1 is in place and at least one of the engine rollouts has produced verifiable continuous-backup artifacts, rewrite `docs/incidents/2026-05-09-seed-parallel-org/HANDOFF.md`. Constraints already settled:

- Cruft removed: drop transient hypotheses, abandoned alternatives, already-shipped detail.
- Important-only history: keep load-bearing facts about how the parallel-org outage happened, what fixed it, what permanently changed in code or process, and what is left.
- What's ahead: open work in priority order.
- Every reference inline-summarized: every TACK-N number, file path, image SHA, branch name, and worktree path carries a short gloss in the same line so a reader can understand the line cold.

Process: read the current HANDOFF, list every reference, write a one-phrase gloss for each, draft new sections, delete the old, write the replacement. Cross-check that no orphan references remain. Other docs in the same directory (`retro_log.md`, `post_incident_roadmap.md`, the various `*_plan.md` and `*_report.md` siblings) get evaluated for fold-in or delete during the same pass.

## Sequencing

1. File the audit chain race ticket with the migration evidence.
2. Delete the scratch-restore code in a single standalone commit. Ship.
3. Build the FoundationDB continuous-mode rollout. Ship.
4. Build the Yugabyte PITR schedule rollout. Ship.
5. Build the Temporal-DB WAL archive rollout. Ship.
6. Document the recovery runbook based on the rollouts that landed.
7. After QA stands up under TACK-235, build the drill subcommand against the continuous artifacts.
8. Once the drill produces a green result, rewrite HANDOFF.md.
