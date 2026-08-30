# Tack recovery

This runbook restores Tack's data stores from backup after data loss or
corruption.

Restore from backup is the slowest recovery tier. The fast, no-data-loss
mechanism is quorum replication across fault domains, which is the disaster
recovery plan for availability and node or zone failure. Backups exist for the
failure class replication cannot help with: corruption or an accidental delete
that replication copies to every replica. Use this runbook for that case, or
when a store must be rebuilt from off-host artifacts.

## What is backed up

- FoundationDB holds all product data (orgs, workspaces, projects, issues, every
  node and relationship). Its backup is a continuous stream to the SeaweedFS
  object store, restorable to any point in its window.
- YugabyteDB holds auth (`users`, `api_tokens`, `org_members`) and the audit
  ledger (`audit.events`, `audit.chain_heads`, `audit.notarizations`,
  `audit.pii`). Its backup is an engine-native distributed snapshot exported
  off-host to the object store.
- Meilisearch holds the search index and is rebuilt from FoundationDB, not
  restored. See `meilisearch-recovery.md`.
- Temporal holds no Tack data and has no backup.

## Confirming the backups restore

`./server ops backup restore-drill` restores each store into throwaway
containers, confirms the data is present, and removes those containers and
volumes afterward, including after an interrupt. It never connects to the live
cluster, so it is safe to run at any time. It performs the same restore steps
described below, so a passing drill is direct evidence that the procedures here
work.

Run it from the install directory with the `backup` profile available:

```
TACK_IMAGE_TAG=<image-sha> docker compose run --rm \
  -e TACK_BACKUP_FDB_CONTINUOUS=true \
  -e TACK_BACKUP_FDB_OVERLAY_PATH=/root/tack/fdb-overlay/fdb.bash \
  -e TACK_BACKUP_YB_ROCKSDB_DIR=/home/yugabyte/var/data/yb-data/tserver/data/rocksdb \
  -e TACK_BACKUP_YB_OVERLAY_PATH=/root/tack/yugabyte-overlay/yugabyted \
  tack-ops ops backup restore-drill
```

The drill exits zero when both legs assert data is present and the rehearsal it
records lands in the object store. The FoundationDB leg runs only when
`TACK_BACKUP_FDB_CONTINUOUS` is true and is otherwise skipped with a warning.

Add `--fdb-target-time` to restore FoundationDB to one moment instead of the
latest restorable point. Write the moment as RFC 3339 (`2026-08-30T01:07:23Z`)
or in FoundationDB's own form (`2026/08/30.01:07:23+0000`); both carry an
explicit offset, so a target time means the same instant wherever it is typed.
The drill reads the backup's restorable window before it restores and stops
when the target falls outside it, naming the window. It never substitutes the
latest for a moment it cannot reach.

## Confirming the backups are still current

`./server ops backup staleness-check --execute` reports how long ago each backup
mechanism last succeeded and exits nonzero once an age passes its threshold, so a
mechanism that quietly stopped producing artifacts surfaces as an alert instead
of a discovery during a recovery. It prints one line per mechanism:

```
ledger-export age=11520s threshold=129600s FRESH newest complete run 20260829T010000Z
rehearsal     age=7200s threshold=691200s FRESH restore drill rt20260829T060000Z-42 passed: fdb, yugabyte
replication   age=0s threshold=1800s FRESH 0 dead nodes, 0 under-replicated tablets
```

The ledger export is dated from the newest complete export run, the rehearsal
from the last passing restore drill, and replication from the last time this
check saw every node alive and every tablet fully replicated, an observation the
check makes and records itself. A mechanism with no recorded success reads
`age=unknown` and counts as stale, because a backup that has never demonstrably
succeeded is the failure this check exists to catch. The FoundationDB restorable
point joins the report only when `TACK_BACKUP_FDB_CONTINUOUS` is true. The
windows are the `TACK_BACKUP_STALENESS_*` settings, in seconds.

## Prerequisites

- The object store is reachable. Its endpoint, host, credentials, and buckets
  come from the configs-rendered `.env`
  ([`tack.env.j2`](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2)),
  never hand-set on the host.
- The FoundationDB overlay `fdb-overlay/fdb.bash` and the YugabyteDB overlay
  `yugabyte-overlay/yugabyted` are present in the install directory.
- A `backup_agent` is running. In normal operation it is the `fdb-backup-agent`
  Compose service (profile `backup`); it drains both backups and restores.

## FoundationDB recovery

The continuous backup is stored in the object store under a marker object at
`backups/<run-id>`. A host that streams starts its session during
`./server ops provision`, which every deploy runs, so no operator has to arm it.
Each start records one such marker; the latest is the live backup.
`./server ops backup fdb-continuous-init` performs the same start out of band and
is idempotent, so it leaves a running session alone.

1. Find the backup name. List the marker objects under `backups/` in the
   `tack-backups` bucket and take the latest run id. The restore drill does this
   with `listImmediateObjects`; by hand, an S3 client listing `backups/` with the
   `/` delimiter returns the marker keys `backups/<run-id>`.

2. Bring up the recovery FoundationDB cluster. For a clean recovery this is a
   fresh, empty single-node cluster on a new data volume, configured with
   `fdbcli --exec 'configure new single ssd'`. Restoring into a cluster that
   already holds data mixes the two; restore into an empty target.

3. Restore. With a `backup_agent` running and the blobstore host reachable, run
   `fdbrestore` against the recovery cluster, addressing the backup by its bare
   run id (fdbbackup re-adds the `backups/` folder):

   ```
   fdbrestore start \
     --dest-cluster-file <recovery-cluster-file> \
     -r 'blobstore://<key>:<secret>@fdb-blobstore-host:<port>/<run-id>?bucket=tack-backups&region=<region>&secure_connection=0' \
     --waitfordone
   ```

   `fdb-blobstore-host` must resolve to the object store's IPv6 address, the same
   `--add-host fdb-blobstore-host:<ipv6>` mapping the backup uses, because
   fdbbackup cannot resolve an IPv6 literal.

   That form restores the latest restorable point. To restore to a chosen
   moment, name the moment and a cluster file for the source cluster.
   fdbrestore converts a wall-clock time into a version using the source's
   version metadata, so the source has to be reachable and its cluster file
   readable during the restore:

   ```
   fdbrestore start \
     --dest-cluster-file <recovery-cluster-file> \
     --orig-cluster-file <source-cluster-file> \
     --timestamp '2026/08/30.01:07:23+0000' \
     -r '<the same backup URL>' \
     --waitfordone
   ```

   The two cluster files must name different clusters. Every write goes to the
   destination; the source is only read for the time-to-version lookup. Naming
   the live cluster as the destination overwrites production, so mount the
   source read-only and keep it off FoundationDB's default client path
   (`/etc/foundationdb/fdb.cluster`), where a command that omitted its
   cluster-file flag would otherwise pick it up.

   Check the moment against the backup's restorable window before restoring.
   `fdbbackup describe -d '<backup URL>' -C <source-cluster-file>
   --version-timestamps` prints `MinRestorableVersion` and
   `MaxRestorableVersion` with wall-clock times; a moment outside that span
   cannot be reached, and fdbrestore given one does not restore that moment.

4. Verify. The product-data keys are tuple-encoded, so confirm the keyspace is
   non-empty with a range read (`fdbcli --exec 'getrange "" \xff 5'`) rather than
   a literal-prefix scan. The drill asserts this automatically.

## YugabyteDB recovery

The export is stored under `yugabyte-snapshot/<run-id>/` in the object store.
The orchestrator (`ops backup yb-snapshot-export`) uploads `manifest.json`,
`metadata.snapshot`, and `schema.sql` at the run root; each tablet-server guest
then uploads its own `nodes/<node>/tablets.tar.gz` via
`ops backup yb-archive-node`. The manifest lists every node prefix the run
needs, and a run is restorable only when every listed prefix holds its archive.
The restore drill enforces this and refuses an incomplete run, naming the
missing nodes. The latest prefix is the most recent export.

The restore drill performs these steps into a throwaway yugabyted and is the
verified procedure. A real recovery applies the same steps to the recovery
YugabyteDB:

1. Stage `manifest.json`, `metadata.snapshot`, and `schema.sql` from
   `yugabyte-snapshot/<run-id>/`, then every node archive the manifest lists
   from `yugabyte-snapshot/<run-id>/nodes/<node>/tablets.tar.gz`. If any listed
   archive is missing, stop and pick a complete run.
2. Bring up the recovery yugabyted with the `yugabyte-overlay/yugabyted` overlay,
   advertising on its own hostname.
3. Create the audit roles the schema's row-level-security policies name (for
   example `audit_reader`), then create the `pgcrypto` extension, then apply
   `schema.sql`. The schema fails to apply if the audit roles do not exist first.
4. Run `yb-admin import_snapshot metadata.snapshot <database>`. It preserves
   table ids and assigns new tablet ids, printing the old-to-new tablet mapping.
5. Extract each node archive into its own directory, then copy each tablet's
   files from any one node's copy (replicas of the same tablet exist on several
   nodes; use one copy, never a mix) into the new tablet's snapshot directory
   under the rocksdb root, following the mapping from step 4, then run
   `yb-admin restore_snapshot <new-snapshot-id>`.
6. Verify the auth tables `users`, `api_tokens`, and `org_members` hold rows.

### Audit ledger caveat

The audit ledger is a hash chain. `audit.chain_heads` holds the current per
`(org, shard)` chain head, and future audit writes continue from it. When
recovering audit data, preserve or merge `audit.chain_heads` rather than
overwriting it; restoring auth alone, or resetting the chain heads, breaks the
chain and loses the ledger's continuity. The recovery target must never write to
the live cluster's `audit.chain_heads`.

## Recovery objectives

For FoundationDB, the recovery point is any version within the continuous
backup's restore window, which advances while the backup runs. For YugabyteDB,
it is the most recent snapshot export. Recovery time for both is bounded by the
restore and its verification, which is the slowest tier; replication, not this
runbook, is the fast path.
