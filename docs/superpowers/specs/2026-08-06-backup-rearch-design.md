# Backup rearchitecture: standby-first, no work on the serving host

## Contract

The backup system meets five properties: durable (every artifact survives loss
of the production machine), scalable (no step reads the dataset row by row),
continuous (protection advances on its own, with no operator action), distributed
(engine-native mechanisms that spread work across nodes), and recoverable
(rehearsed restores, not assumed ones). Two numbers bind the design: a disaster
may lose at most the last few seconds of writes, and loss of a database node
must heal automatically in seconds, with no operator action.

One structural rule binds every mechanism: backup work never runs on the
production guest or inside its containers. Snapshot creation by the database
itself (hard links, near-instant) is the only permitted on-cluster step.

## Target architecture

### Product data (FoundationDB)

Unchanged. The engine streams every change continuously to the S3 object store
and is restorable to any point in its window. One addition: a freshness alarm on
the stream (see Alarms).

### Auth and audit ledger (YugabyteDB)

The database runs as a three-node cluster, each node on its own guest, keeping
three live copies of every row (replication factor 3). When the leader node
dies, the survivors elect a new leader in seconds, automatically. Failover is
convergence, not an operator event.

- Backup work pins to a follower node: the scheduled distributed-snapshot
  export to the object store, the pack and upload, and every restore rehearsal.
  The leader only ever serves traffic.
- Rebuilding from the object store remains the slow fallback for corruption
  that replicated to all copies.
- The in-cluster point-in-time rewind schedule stays as the corruption layer
  (it is near-free; it is not backup work).

Scope boundary: the app stays on its current guest, and traefik (which routes
tack.home.goodkind.io to the app) is untouched. The app's database connection
string lists all three node addresses, so the driver follows the cluster
without any proxy change. Seconds-fast convergence therefore covers the
database tier only; the app guest remains a single point, and app-tier
failover is a separate future change that would touch traefik.

Placement: all three nodes run on the vault hypervisor, one per guest, over
the existing routed IPv6 network. This converges automatically for guest and
process failures and requires no cross-hypervisor networking. Loss of vault
itself is covered by the off-machine export until one node migrates to a
second hypervisor; that migration is a routine add-node, remove-node
operation with automatic rebalancing and discards nothing built here.

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
restorable point, the cluster's under-replication state (any tablet below three
live copies), the follower's last completed export, and the last passing
restore rehearsal.

## Rehearsals

The existing restore drill runs on a schedule against the follower's exports,
never against the leader. A failover rehearsal (kill a node, verify the cluster
converges and the app never errors, restore the node) runs on the QA cluster
before rollout and periodically after.

## Rollout

Every phase lands on QA (guest pair) first, then production, per the standing
iterative-testing rule.

1. Deletions plus the bare-command guard.
2. Two additional database guests provisioned (OpenTofu plus Ansible in the
   configs repo); the cluster expands to three nodes; under-replication alarm
   live.
3. Export scheduling pins to a follower; the on-leader export path retires.
4. Scheduled rehearsals plus the staleness alarms.
5. Failover rehearsal on QA; document the failover runbook.

## Interactions

- TACK-336 (durable dead-letter queue plus Kafka retention) proceeds
  independently; it protects the ledger's write path, not its storage.
- The audit cold archive (Iceberg on SeaweedFS, per the horizontal design doc)
  is out of scope here and unblocked by nothing in this spec.
- The 2026-08-05 incident's prevention items (bare-command guard, per-service
  memory budgets on the production guest) ship with phase 1.
