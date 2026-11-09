# Prepare a data restore

Restoration replaces data in a recovery target. The backup CLI implements
temporary restore drills and a FoundationDB version lookup. It does not
implement an operator command that restores an arbitrary persistent target.
Do not substitute direct engine commands for the required audited operation.

Prepare and validate recovery on QA before production. Production tests require
explicit operator authorization. Preserve the audit ledger and its chain heads
through every recovery. A recovery target must never write the live cluster's
`audit.chain_heads`.

## Establish the recovery target

1. Record the loss or corruption, the required recovery time, and the newest
   acceptable source write. Read the observed backup point before estimating
   loss. Use the [backup tiers](../../explanation/backup-tiers.md) to distinguish
   a replica failure from data corruption that requires an earlier copy.
2. Identify the source cluster and separate recovery target. Require fresh data
   storage for the target. Restore FoundationDB into an empty cluster; existing
   target keys can mix with restored keys. Never configure a new database over
   an existing cluster.
3. Obtain object-store settings, credentials, engine images, overlays, network
   settings, and storage paths through Configs. Do not hand-edit live
   configuration. Keep the IPv6-only service bridge and permanent YugabyteDB
   node identities. Container addresses must not become persistent engine
   identities.
4. Record an operator identity and the required audit evidence for intent,
   actual targets, outcomes, interruptions, and recovery. Require an audited
   operation before effects. The existing drill validates a temporary target;
   its success does not implement or authorize persistent-target restoration.
5. Rehearse the selected backup and time using
   [the restore drill](verify-restores.md). Record the actual legs, artifact
   identities, verification results, and observed backup age.

## Select FoundationDB artifacts

1. Identify backup marker objects under `backups/` in the configured backup
   bucket. A marker key has the form `backups/<run-id>`; the native backup URL
   addresses the bare run identifier because the engine adds the folder.
   An S3 listing with `/` as its delimiter distinguishes these marker objects
   from child prefixes.
2. For the latest point, identify the newest session. For a selected moment,
   require a session with a restorable window containing that moment. The drill
   checks sessions from newest to oldest. Its `fdbbackup describe` call uses
   `--version-timestamps` to read the earliest and latest restorable times.
   If every candidate fails, report each window or read error and refuse the target.
3. Use a whole-second target with an explicit UTC offset. The accepted forms
   include `2026-08-30T01:07:23Z` and `2026/08/30.01:07:23+0000`.
   FoundationDB's timestamp argument cannot express fractional seconds. A
   fractional target must fail rather than round down.
4. Establish how the recovery will convert time to a version. A timestamp-based
   drill reads the source cluster and needs its cluster file. Keep that file
   read-only and outside the default client path, `/etc/foundationdb/fdb.cluster`. The destination file must
   identify a different cluster. After total source loss, `restore-version`
   reads recorded version-to-time pairs from object storage and reports the
   reading at or before the requested moment, including its timestamp. It
   cannot recover a moment earlier than its oldest reading or perform a restore.
5. Require the recovery operation to use the correct cluster explicitly for
   every engine call, run a backup agent against the empty target, and preserve
   the `fdb-blobstore-host` mapping to the object store's IPv6 address. The native blobstore client
   cannot use an IPv6 literal as that hostname. Keep credential-bearing URLs
   out of output and logs.
6. Require a range read across the restored keyspace. Product keys use tuple
   encoding; a literal product-prefix scan does not establish that data exists.

## Select YugabyteDB artifacts

1. Select a complete export under `yugabyte-snapshot/<run-id>/`. Read
   `manifest.json`, every declared root artifact, and each declared node's
   `nodes/<node>/tablets.tar.gz` and `nodes/<node>/tablets.inventory`.
   Require `metadata.snapshot`, `schema.sql`, and `roles.sql` in the manifest
   and in object storage. The error `manifest does not declare required artifacts`
   requires a new export. When the error identifies declared artifacts as absent,
   complete their uploads or choose another complete run. The newest prefix
   alone cannot establish completeness.
2. Require the recovery engine and dump tools to match the ledger image. Use
   the patched YugabyteDB overlay and the target's own permanent hostname for
   both listening and advertising. A replacement hostname must resolve inside
   the container to an address that the container can bind.
3. Require the operation to apply roles before schema privileges. The roles
   dump preserves source roles and memberships without passwords. The schema
   dump preserves grants and revokes. The operation must create `pgcrypto`
   before schema application.
4. Require role application with verbose diagnostics and fixed server and
   client message locales. The existing drill sets server `lc_messages=C`,
   clears `LC_ALL`, and sets `LC_MESSAGES=C` through a superuser connection.
   The server locale controls SQL diagnostics. The client locale controls the
   `LOCATION` and `DETAIL` labels. The drill continues after duplicate-role
   creation to apply each subsequent role
   alteration. It inspects every diagnostic afterward; an exit status alone
   cannot establish successful application.
5. Reject role-application failures other than SQLSTATE `42710` with the exact
   message `role "..." already exists`. Permit that diagnostic only for an
   engine-created role or the target bootstrap user. The engine creates
   `postgres`, `yugabyte`,
   `yb_db_admin`, `yb_extension`, `yb_fdw`, and its bootstrap user before the
   restore. The current classifier accepts duplicate-role diagnostics beyond
   that set and does not enforce this restriction.
   SQLSTATE classes `00`, `01`, and `02` do not indicate failed statements.
   Other diagnostics, including unclassified diagnostics, must stop recovery.
6. Require schema application to stop on its first error. The existing drill
   uses `ON_ERROR_STOP=1` and superuser `session_replication_role=replica` to
   prevent migration 019's schema guard from rejecting the restored schema.
   This scratch restore setting does not authorize live schema changes.
7. Require snapshot import to preserve table identifiers and use its returned
   old-to-new tablet mappings. The drill rejects a table row when its returned
   new identifier differs from its original identifier. Extract each node
   archive separately and compare each file against its node inventory by path
   and size before selecting a replica.
8. Require one complete node copy for every imported tablet. Do not mix files
   from different replicas. A present directory or one nonempty file does not
   establish completeness. Stop before placement if a tablet lacks a complete copy.
   The error reports the total and a sample of tablets with missing files or no archive.
   Place one verified copy in each new tablet's snapshot directory under the configured
   RocksDB root. Restore only after every mapping has a verified copy. A successful
   engine restore alone cannot detect an omitted tablet. Wait for restoration to
   finish before querying rows.
9. Require the recovered database to contain `users`, `api_tokens`, and
   `org_members` rows. Require complete per-organization audit-chain
   verification under a login with only `audit_reader` membership. Reject an
   empty ledger, a stored hash that fails recomputation, a `prev_hash` that does
   not match the preceding row, sequence gaps in a full export,
   or a bundle count below the restored ledger count. A filtered or bounded
   export can create gaps by excluding rows; it cannot replace full-range
   recovery verification.
10. Compare restored counts and known writes with source evidence separately.
    A consistent chain cannot detect missing newest rows. Preserve or merge
    the recovered per-organization and per-shard chain heads rather than reset
    them. Restore authentication and audit data together.
11. Require the audited `ops audit seed-roles` operation to set recovered login passwords
    before any application connection uses the recovered database. The export
    intentionally omits passwords.

Stop preparation if the selected artifacts, target isolation, required audit
operation, or verification cannot satisfy these conditions. A passing drill
proves its own temporary restore and checks; it does not prove production
completeness or provide a persistent recovery operation.
