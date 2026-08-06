# Backup rearchitecture: standby-first, no work on the serving host

## Contract

The backup system meets five properties: durable (every artifact survives loss
of the production machine), scalable (no step reads the dataset row by row),
continuous (protection advances on its own, with no operator action), distributed
(engine-native mechanisms that spread work across nodes), and recoverable
(rehearsed restores, not assumed ones). Two numbers bind the design: a disaster
may lose at most the last few seconds of writes, and the service may be down for
at most minutes.

One structural rule binds every mechanism: backup work never runs on the
production guest or inside its containers. Snapshot creation by the database
itself (hard links, near-instant) is the only permitted on-cluster step.

## Target architecture

### Product data (FoundationDB)

Unchanged. The engine streams every change continuously to the S3 object store
and is restorable to any point in its window. One addition: a freshness alarm on
the stream (see Alarms).

### Auth and audit ledger (YugabyteDB)

A standby copy of the database runs on its own guest and receives every change
as an asynchronous stream (the engine's xCluster replication). The standby
trails production by seconds.

- All backup work runs against the standby: the scheduled distributed-snapshot
  export to the object store, the pack and upload, and every restore rehearsal.
- The standby is promotable. Disaster recovery is flipping traffic to it, which
  bounds downtime to minutes. Rebuilding from the object store remains the slow
  fallback for corruption that replicated.
- The in-cluster point-in-time rewind schedule stays on production as the
  corruption layer (it is near-free; it is not backup work).

### Derived stores (Meilisearch, ClickHouse)

Never backed up. Both rebuild from their sources of record. Their runbooks are
the recovery path.

### Temporal workflow state (temporal-db)

No backup, matching the recovery runbook. The dump step is deleted.

## Deletions

The bare `ops backup` command stops running a full snapshot; it prints its
subcommands and exits nonzero. Deleted with it: the `ysql_dump` full row dump
(`backup_yugabyte.go`), the temporal-db dump (`backup_temporal.go`), the
Meilisearch volume tar (`backup_meilisearch.go`), the local manifest and
`.latest` pointer machinery for those artifacts, their verify categories, and
the unused `tack-audit-archive` bucket creation. The recovery runbook consumes
none of these artifacts.

## Alarms

One metric per mechanism: seconds since last success. Alert on staleness, not on
failure, because silent failures do not fail. Covered: the FoundationDB stream's
restorable point, the standby's replication lag, the standby's last completed
export, and the last passing restore rehearsal.

## Rehearsals

The existing restore drill runs on a schedule against the standby's exports,
never on production. A promotion rehearsal (flip to standby, verify, flip back)
runs on the QA pair before the standby enters service and periodically after.

## Rollout

Every phase lands on QA (guest pair) first, then production, per the standing
iterative-testing rule.

1. Deletions plus the bare-command guard.
2. Standby guest provisioned (OpenTofu plus Ansible in the configs repo),
   replication established, lag alarm live.
3. Export scheduling moves to the standby; production export path retired.
4. Scheduled rehearsals plus the staleness alarms.
5. Promotion rehearsal on QA; document the promotion runbook.

## Interactions

- TACK-336 (durable dead-letter queue plus Kafka retention) proceeds
  independently; it protects the ledger's write path, not its storage.
- The audit cold archive (Iceberg on SeaweedFS, per the horizontal design doc)
  is out of scope here and unblocked by nothing in this spec.
- The 2026-08-05 incident's prevention items (bare-command guard, per-service
  memory budgets on the production guest) ship with phase 1.
