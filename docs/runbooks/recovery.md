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

## Verify recoverability without touching live data

`./server ops backup restore-drill` restores each store into throwaway
containers, asserts the data is present, and removes the throwaway containers and
volumes, including after an interrupt. It never connects to the live cluster, so
it is safe to run anytime to confirm the backups are restorable. The drill is the
executable, verified form of the procedures below.

Run it from the install directory with the `backup` profile available:

```
TACK_IMAGE_TAG=<image-sha> docker compose run --rm \
  -e TACK_BACKUP_FDB_CONTINUOUS=true \
  -e TACK_BACKUP_FDB_OVERLAY_PATH=/root/tack/fdb-overlay/fdb.bash \
  -e TACK_BACKUP_YB_ROCKSDB_DIR=/home/yugabyte/var/data/yb-data/tserver/data/rocksdb \
  -e TACK_BACKUP_YB_OVERLAY_PATH=/root/tack/yugabyte-overlay/yugabyted \
  tack-ops ops backup restore-drill
```

The drill exits zero when both legs assert data is present. The FoundationDB leg
runs only when `TACK_BACKUP_FDB_CONTINUOUS` is true and is otherwise skipped with
a warning.

## Prerequisites for any recovery

- The object store is reachable, and `.env` provides `TACK_BACKUP_S3_HOST`,
  `TACK_BACKUP_S3_PORT`, `TACK_BACKUP_S3_ACCESS_KEY_ID`,
  `TACK_BACKUP_S3_SECRET_ACCESS_KEY`, and the bucket names.
- The FoundationDB overlay `fdb-overlay/fdb.bash` and the YugabyteDB overlay
  `yugabyte-overlay/yugabyted` are present in the install directory.
- A `backup_agent` is running. In normal operation it is the `fdb-backup-agent`
  Compose service (profile `backup`); it drains both backups and restores.

## FoundationDB recovery

The continuous backup is stored in the object store under a marker object at
`backups/<run-id>`. Each `./server ops backup fdb-continuous-init` records one
such marker; the latest is the live backup.

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

4. Verify. The product-data keys are tuple-encoded, so confirm the keyspace is
   non-empty with a range read (`fdbcli --exec 'getrange "" \xff 5'`) rather than
   a literal-prefix scan. The drill asserts this automatically.

## YugabyteDB recovery

The export is stored under `yugabyte-snapshot/<run-id>/` in the object store and
holds four files: `manifest.json`, `metadata.snapshot`, `schema.sql`, and
`tablets.tar.gz`. The latest prefix is the most recent export.

The restore drill performs these steps into a throwaway yugabyted and is the
verified procedure. A real recovery applies the same steps to the recovery
YugabyteDB:

1. Stage the four files from `yugabyte-snapshot/<run-id>/`.
2. Bring up the recovery yugabyted with the `yugabyte-overlay/yugabyted` overlay,
   advertising on its own hostname.
3. Create the audit roles the schema's row-level-security policies name (for
   example `audit_reader`), then create the `pgcrypto` extension, then apply
   `schema.sql`. The schema fails to apply if the audit roles do not exist first.
4. Run `yb-admin import_snapshot metadata.snapshot <database>`. It preserves
   table ids and assigns new tablet ids, printing the old-to-new tablet mapping.
5. Copy each tablet's files from the export into the new tablet's snapshot
   directory under the rocksdb root, following the mapping from step 4, then run
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

- FoundationDB: the recovery point is any version within the continuous backup's
  restore window, which advances while the backup runs.
- YugabyteDB: the recovery point is the latest snapshot export.
- Recovery time for both is bounded by restore and verification, the slowest
  tier. Replication, not this runbook, is the fast path.
