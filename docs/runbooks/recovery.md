# Tack recovery

Restore Tack's data stores from backup after data loss or corruption, or when
rebuilding a store from off-host artifacts.

Backup restore is the slowest recovery tier. Quorum replication across fault
domains provides availability without data loss after node or zone failure.
Backups recover from corruption and accidental deletion, which replication copies
to every replica.

## Backup contents

- FoundationDB stores all product data, including orgs, workspaces, projects,
  issues, nodes, and relationships. Its continuous backup to the SeaweedFS
  object store supports restoration to any point in its window.
- YugabyteDB stores auth (`users`, `api_tokens`, `org_members`) and the audit
  ledger (`audit.events`, `audit.chain_heads`, `audit.notarizations`,
  `audit.pii`). Its engine-native distributed snapshot backup exports off-host
  to the object store.

## Compare recovery points

The stores lose different amounts of data during recovery.

FoundationDB supports restoration to any point within its continuous backup
window. A stalled backup agent or an object store refusing writes delays the
latest restorable point even when other services appear healthy. Read the
staleness check's fdb-restorable metric before estimating data loss.

YugabyteDB restores the newest complete scheduled export and loses writes after
that export. Between exports, quorum replication across the three data guests
protects against guest failure, but not corruption or accidental deletion.
The export schedule determines the recovery point for those failures.

The owner guest exports daily at 03:17 UTC with up to five minutes of randomized
delay. Each data guest archives its tablet files every 15 minutes. A run becomes
complete within roughly a quarter hour of the export that started it. The owner
runs the restore rehearsal daily at 05:40 UTC, after the export and archives.
A staleness check runs on timers on the owner and one data guest and mails once
when a mechanism exceeds its age threshold.

Data loss remains within one day plus archive lag only while every scheduled
export succeeds. Each missed export adds another day. The 36-hour export threshold
allows one missed export before alerting. The 8-day rehearsal threshold allows
seven consecutive daily rehearsals to fail before alerting. Compare each
threshold with its schedule rather than assuming every check alerts after one
missed run.

## Confirm the backups restore

`./server ops backup restore-drill` restores each store into throwaway containers,
checks the restored data, and removes the containers and volumes afterward,
including after an interrupt. It performs the recovery steps below.

Run it from the install directory with the `backup` profile available:

```
TACK_IMAGE_TAG=<image-sha> docker compose run --rm \
  -e TACK_BACKUP_FDB_CONTINUOUS=true \
  -e TACK_BACKUP_FDB_OVERLAY_PATH=/root/tack/fdb-overlay/fdb.bash \
  -e TACK_BACKUP_YB_ROCKSDB_DIR=/home/yugabyte/var/data/yb-data/tserver/data/rocksdb \
  -e TACK_BACKUP_YB_OVERLAY_PATH=/root/tack/yugabyte-overlay/yugabyted \
  tack-ops ops backup restore-drill
```

The drill exits zero after every enabled store restore confirms data is present
and the drill writes its rehearsal record to the object store. It restores
FoundationDB only when `TACK_BACKUP_FDB_CONTINUOUS` is true; otherwise, it logs a
warning and skips that restore.

The FoundationDB check monitors progress without a total restore deadline. A
large restore can continue while its counters advance. If progress stops, the
check fails and reports the inactive duration and final counter values.

Add `--fdb-target-time` to restore FoundationDB to one moment instead of the
latest restorable point. Write the moment as RFC 3339 (`2026-08-30T01:07:23Z`)
or in FoundationDB's own form (`2026/08/30.01:07:23+0000`). Both include an
explicit offset and identify the same instant regardless of the local time zone.
Use a whole second. FoundationDB's timestamp-to-version conversion does not
support fractional seconds. The drill rejects fractions instead of restoring
the preceding whole second.

Before restoring, the drill reads the backup's restorable window and rejects
any target outside it. The window lookup has its own time limit. Every explicit
target requires this check; the drill must not substitute the latest restorable
point when it cannot restore the requested target.

## Confirm the backups are current

`./server ops backup staleness-check --execute` prints one line per mechanism
with the age of its last success. It exits nonzero when an age exceeds its
threshold and alerts when a mechanism stops producing current artifacts:

```
ledger-export age=11520s threshold=129600s FRESH newest complete run 20260829T010000Z
rehearsal     age=7200s threshold=691200s FRESH restore drill rt20260829T060000Z-42 passed: fdb, yugabyte
replication   age=0s threshold=1800s FRESH 0 dead nodes, 0 under-replicated tablets
```

The check dates ledger exports from the newest complete export and rehearsals
from the last passing restore drill. It dates replication from its own last
recorded observation that every node was alive and every tablet fully replicated.
A mechanism without a recorded success reports `age=unknown` and counts as stale.
The report includes the FoundationDB restorable point only when
`TACK_BACKUP_FDB_CONTINUOUS` is true. The `TACK_BACKUP_STALENESS_*` settings
specify thresholds in seconds.

The first run that detects a stale mechanism mails `TACK_BACKUP_ALARM_EMAIL`.
The subject identifies the guest and fault. The body explains what stopped,
when it last succeeded, and what to check. The check does not repeat the mail
while the mechanism remains stale or mail on recovery. The guest's check journal
records each reading.

The checkers share sent-alarm state in `backup-status/alarm-state.json` in the
backup bucket. Each guest also saves `staleness-alarm-state.json` under its
backup root (`/root/backups`). This shared state prevents duplicate mail across
the guests. A guest that cannot read the store uses its local copy. The check
retries mail the relay refused on the next run without saving it as sent.
Delete the bucket object and local file on each guest to send mail again on the
next stale run.

## Prerequisites

- The object store must accept connections. Use the endpoint, host, credentials,
  and buckets from the configs-rendered `.env`
  ([tack.env.j2](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2)),
  not manual host settings.
- The install directory must include the FoundationDB overlay
  `fdb-overlay/fdb.bash` and YugabyteDB overlay `yugabyte-overlay/yugabyted`.
- Run a `backup_agent`. Normal operation uses the `fdb-backup-agent` Compose
  service in the `backup` profile to process backups and restores.

## Expire old backups

Retention permanently deletes expired backup copies. It preserves live database
rows, audit ledger rows, backup status objects, and version-to-time records.
The default restore window is 14 days. Ledger cleanup requires a complete export
within that window and preserves exports exactly at the cutoff. FoundationDB
uses its native expiration guard to preserve the snapshot and mutation logs
needed to restore at the cutoff. Those dependencies may include older files.
FoundationDB selection converts the cutoff to a version with the
version-to-time records that the staleness check writes. Retention refuses
FoundationDB cleanup until those records include a reading from before the
cutoff. The preview reports each FoundationDB window as `min_version` and
`max_version`.

Run the commands from the install directory:

1. Preview the selected ledger exports and FoundationDB sessions:

   ```sh
   docker compose run --rm tack-ops ops backup retention
   ```

2. Review the UTC cutoff and selected targets in the output.
3. Delete the selected expired backups:

   ```sh
   docker compose run --rm tack-ops ops backup retention --execute
   ```

Set `--keep-days` to select a different restore window. Execution recomputes the
selection and records audit intent, each cleanup target, and its observed result.
If cleanup fails, inspect its audit result before retrying. An interrupted S3
request can have an uncertain result; the audit evidence distinguishes requested
objects from confirmed deletions. Repeated execution recomputes the remaining
work.

Retention and restore drills acquire the same filesystem lock under the backup
root. Each command waits until the other command finishes reading or changing
backups. Cancellation stops active one-shot containers before releasing the lock.

## FoundationDB recovery

The object store records each continuous backup session with a marker object at
`backups/<run-id>`. Every deploy runs `./server ops provision`, which starts the
session automatically on hosts with continuous backup enabled. The latest marker
identifies the live backup. `./server ops backup fdb-continuous-init` starts a
session outside provisioning without changing an existing running session.

1. Find the backup name. List the marker objects under `backups/` in the
   `tack-backups` bucket and take the latest run id. The restore drill does this
   with `listImmediateObjects`; an S3 client listing `backups/` with the `/`
   delimiter returns marker keys `backups/<run-id>`.

2. Start a fresh, empty single-node recovery cluster on a new data volume,
   configured with `fdbcli --exec 'configure new single ssd'`. Restoring into a
   cluster that already stores data mixes the records. Use an empty target.

3. With a `backup_agent` running and a connection to the blobstore host, run
   `fdbrestore` against the recovery cluster. Use the bare backup run id;
   fdbbackup re-adds the `backups/` folder:

   ```
   fdbrestore start \
     --dest-cluster-file <recovery-cluster-file> \
     -r 'blobstore://<key>:<secret>@fdb-blobstore-host:<port>/<run-id>?bucket=tack-backups&region=<region>&secure_connection=0' \
     --waitfordone
   ```

   `fdb-blobstore-host` must resolve to the object store's IPv6 address. Use the
   backup's `--add-host fdb-blobstore-host:<ipv6>` mapping; fdbbackup cannot resolve
   an IPv6 literal.

   This command restores the latest restorable point. A chosen moment requires
   the source cluster's version metadata for time-to-version conversion. The
   restore must connect to that source and read its cluster file:

   ```
   fdbrestore start \
     --dest-cluster-file <recovery-cluster-file> \
     --orig-cluster-file <source-cluster-file> \
     --timestamp '2026/08/30.01:07:23+0000' \
     -r '<the same backup URL>' \
     --waitfordone
   ```

   The two cluster files must identify different clusters. fdbrestore writes only
   to the destination cluster and reads the source for the time-to-version lookup.
   Selecting the live cluster as the destination overwrites production. Mount the
   source cluster file read-only outside FoundationDB's default client path
   (`/etc/foundationdb/fdb.cluster`). A command without a cluster-file flag would
   otherwise select that default.

   Check the moment against the backup's restorable window before restoring.
   `fdbbackup describe -d '<backup URL>' -C <source-cluster-file>
   --version-timestamps` prints `MinRestorableVersion` and
   `MaxRestorableVersion` with wall-clock times. fdbrestore cannot restore a
   moment outside that span.

4. Confirm the tuple-encoded product keyspace is non-empty with a range read
   (`fdbcli --exec 'getrange "" \xff 5'`), not a literal-prefix scan.
   The drill checks this automatically.

## YugabyteDB recovery

The object store stores each export under `yugabyte-snapshot/<run-id>/`.
`ops backup yb-snapshot-export` uploads `metadata.snapshot`, `schema.sql`,
`roles.sql`, and `manifest.json` at the run root. Each tablet-server guest uses
`ops backup yb-archive-node` to upload `tablets.tar.gz` and `tablets.inventory`
under its `nodes/<node>/` prefix. The inventory lists the path and size of every
archived file. The manifest lists required root artifacts and node prefixes.
A restorable run requires every root artifact and both files for every listed
node. The drill rejects incomplete runs and reports the missing artifacts.
The latest prefix identifies the most recent export.

`roles.sql` defines all source cluster roles and memberships without passwords.
`schema.sql` defines grants and revokes at backup time. Apply roles before the
schema because grants to missing roles fail.

Every manifest must declare `metadata.snapshot`, `schema.sql`, and `roles.sql`.
The restore opens these by name and rejects a manifest that omits any of them,
even if the objects exist. An incomplete manifest either predates the roles file
or suffered truncation and cannot restore both the schema and application access.
The error "manifest does not declare required artifacts" requires a new export.
The error "artifacts ... are absent" instead indicates a declared artifact that
did not upload. The next archive timer or a re-upload can resolve that failure.

The restore drill runs the following steps in a throwaway yugabyted. Apply the
same steps to the recovery YugabyteDB:

1. Stage `manifest.json` from `yugabyte-snapshot/<run-id>/`, then every artifact
   it lists from `yugabyte-snapshot/<run-id>/<artifact>`, then, for every node
   it lists, both `nodes/<node>/tablets.tar.gz` and
   `nodes/<node>/tablets.inventory`. If any listed artifact or either file for
   any listed node is missing, stop and select a complete run.
2. Start the recovery yugabyted with the `yugabyte-overlay/yugabyted` overlay,
   advertising on its own hostname.
3. Apply `roles.sql`, create the `pgcrypto` extension, then apply `schema.sql`.
   Run the roles file with `VERBOSITY=verbose` and without `ON_ERROR_STOP`, then
   inspect the errors:

   ```
   PGOPTIONS="-c lc_messages=C" LC_ALL= LC_MESSAGES=C \
   ysqlsh -h <host> -p 5433 -U <bootstrap-user> -d <database> \
     -v VERBOSITY=verbose -q -f roles.sql
   ```

   Pin the locale to make errors match the checks below. The recovery cluster
   renders severity and message text according to `lc_messages`; the engine
   image includes catalogs for 19 languages. `PGOPTIONS` sets server rendering
   and requires a superuser connection, such as the bootstrap user a fresh
   yugabyted creates. The client variables set libpq's `LOCATION` and `DETAIL`
   labels.

   Accept only errors with SQLSTATE `42710` and message `role "..." already exists`
   for engine-created roles (`postgres`, `yugabyte`, `yb_db_admin`,
   `yb_extension`, `yb_fdw`) or the bootstrap user. The following `ALTER ROLE`
   still applies each role's attributes after `CREATE ROLE` fails for an existing
   role. Read the SQLSTATE regardless of message language. Codes outside classes
   `00`, `01`, and `02` indicate failed statements. Any other error indicates a
   missing role; stop and fix it before applying the schema.

   Apply the schema dump with `ON_ERROR_STOP=1` to fail the restore when a grant
   targets a missing role. Set `session_replication_role=replica` through
   `PGOPTIONS` to prevent the engine from firing the schema guard during schema
   apply. Use a superuser connection because the replica setting requires
   superuser privileges.

   ```sh
   PGOPTIONS="-c session_replication_role=replica" \
   ysqlsh -h <host> -p 5433 -U <bootstrap-user> -d <database> \
     -v ON_ERROR_STOP=1 -q -f schema.sql
   ```

   The export excludes passwords from restored login roles. Set those passwords
   with `ops audit seed-roles` against the recovered database before configuring
   the application to use it.
4. Run `yb-admin import_snapshot metadata.snapshot <database>`. It preserves
   table ids and assigns new tablet ids, printing the old-to-new tablet mapping.
5. Extract each node archive into its own directory. Check every file against
   that node's `tablets.inventory` for presence and exact size. For each tablet,
   select one complete node copy and copy its files into the new tablet's snapshot
   directory under the rocksdb root, using the mapping from step 4. Do not mix
   files from multiple copies of a tablet.

   A directory's existence or a single non-empty file does not prove tablet
   completeness. The inventory records every file this backup captured.
   `restore_snapshot` can succeed with missing tablet data without reporting the
   loss. Stop and select a complete run if every copy of any tablet fails the
   inventory check. Run `yb-admin restore_snapshot <new-snapshot-id>` only after
   every mapped tablet has a complete copy. The drill checks before copying and
   reports each unavailable tablet and its missing archived file count.
6. Verify that `users`, `api_tokens`, and `org_members` contain rows.
7. Verify the restored ledger's hash chain as a login role with only
   `audit_reader` membership, the base role for ledger reads. The bootstrap
   superuser bypasses grants and cannot prove that a restored reader can read
   the ledger. This bypass previously concealed missing grants in backups.
   Export a signed bundle per org over the full time range, verify each bundle,
   and inspect the chain-break count. A break occurs when a row's `prev_hash`
   does not identify the preceding row or its stored hash does not recompute.
   Fail recovery on a break or sequence gap. A full-range export intentionally
   omits neither rows nor sequences; a gap indicates a missing restored row and
   prevents verification of that link. Only filtered or time-bounded exports may
   tolerate gaps caused by their selection. Compare each bundle's row count
   with the restored ledger's count for that org. The drill checks all of these
   conditions and fails on breaks, gaps, missing exported rows, or an empty ledger.

   The export writes and releases each row as it reads it, using about one row's
   memory regardless of ledger size. It does not impose a row cap. Investigate
   any shortfall against the ledger's count as a defect, not a size limit.
   `audit export` uses the same export and includes the full requested range
   unless the command specifies `--limit`.

   Bundle rows do not require a particular order. Verification sorts them into
   per-shard sequence order before checking links. Chain-break and gap counts
   do not depend on export order.

   A verified chain proves consistency between restored rows, not that recovery
   restored every source row. Losing the newest rows can leave a valid chain.
   Compare source and restored row counts separately to check completeness.

### Audit ledger caveat

The audit ledger is a hash chain. `audit.chain_heads` stores the current head for
each `(org, shard)`, which future writes continue. Preserve or merge these heads
during recovery rather than overwriting them. Restoring auth alone or resetting
the heads breaks ledger continuity. The recovery target must never write to the
live cluster's `audit.chain_heads`.

## Recovery objectives

FoundationDB can recover any version within the continuous backup's advancing
restore window. YugabyteDB can recover the most recent snapshot export. Restore
and verification determine recovery time for both stores. Quorum replication
provides faster recovery from node or zone failure.
