# Tack backup runbook

This runbook covers how to take a backup, verify it, restore from it,
respond to alerts, and triage failures of the automated restore-test.

## Why this exists

Between 2026-04-25 and 2026-05-09 the production backup script silently
shipped empty FDB tarballs every day for two weeks. The
`tack_fdb-data.tar.gz` artifact existed and had a non-zero size, but
contained zero `.sqlite` and zero `.fdq` files because the FoundationDB
image declares `VOLUME /var/fdb/data` and the anonymous volume mounted at
that path shadowed the named-volume mount the backup script tarred.
Reference: `incident_2026-05-09_seed_parallel_org/retro_log.md` section 1A.

The defect was not caught because no automated test exercised a restore.
The two scripts in this runbook close that gap. They are deliberately
layered so a defect surfaces at the cheapest layer first.

## Layered verification

| Layer | Script | Cost | Catches |
|-------|--------|------|---------|
| 1 | `scripts/backup-content-check.sh` | seconds | empty tarballs, missing tables, corrupt archives, dump files with no `CREATE TABLE` |
| 2 | `scripts/backup-restore-test.sh` | minutes | restorability defects that look fine structurally (truncated fdbbackup, schema-conflicting pg_dump, Meilisearch volume the daemon refuses) |

Layer 1 runs in CI (`.github/workflows/check.yml`) and on every operator
backup. Layer 2 runs on a daily systemd timer on CT 117 and on demand
before any restore is treated as authoritative.

## Taking a backup

`scripts/backup.sh` runs on CT 117 and writes `/root/backups/tack-<UTC>/`.
The script is idempotent and updates `/root/backups/.latest` on success.
The current implementation has known defects documented in the 2026-05-09
incident report; the rewrite is tracked in a parallel worktree.

```
ssh tack '/root/tack/scripts/backup.sh'
```

After a backup completes, immediately run the content check:

```
ssh tack 'ts=$(cat /root/backups/.latest); /root/tack/scripts/backup-content-check.sh "/root/backups/tack-$ts"'
```

Non-zero exit means the backup is structurally suspect. Do not treat the
backup as a recovery resource until layer 2 also passes.

## Verifying a backup (layer 1, structural)

`backup-content-check.sh <dir>` inspects every recognized artifact:

| Artifact pattern | Verification |
|------------------|--------------|
| `fdb-snapshot*.tar.gz`, `fdbbackup*.tar.gz`, `tack_fdb-data*.tar.gz` | tarball lists OK, contains either fdbbackup `snapshots,` markers with `logs/`, `kvranges/`, `properties/log_begin_version`, OR `.sqlite`/`.fdq` files |
| `audit-snapshot*.tar.gz` | bundle has `MANIFEST.txt` plus `sql/events.csv`, `sql/chain_heads.csv`, `sql/notarizations.csv`, `sql/pii.csv`, and `events.csv` has a recognizable header |
| `yugabyte*.tar.gz`, `tack_yugabyte-data*.tar.gz` | not empty, contains `yb-data`, `pg_data`, `postgresql.conf`, or `tablet-*` markers |
| `*.sql`, `*.sql.gz` | non-empty, first 16 MiB contains a `CREATE TABLE` statement |
| `meili*.tar.gz`, `tack_meili-data*.tar.gz` | not empty, contains `data.ms/` or `instance-uid` |

Exit 0 means every detected artifact passed every check. Exit 1 means at
least one defect; standard-error lists each failure. Exit 2 means
invocation error.

`--strict` flips every "no artifact for this category" warning into a
failure. Use `--strict` from the backup-completion path so a missing
artifact does not pass silently.

## Verifying a backup (layer 2, restore)

`backup-restore-test.sh <dir>` brings up a scratch container per
artifact, restores into it, and runs a sanity query:

| Artifact | Scratch container | Sanity check |
|----------|-------------------|--------------|
| FDB fdbbackup tar | `foundationdb/foundationdb` configured single memory | `fdbbackup describe` parses, `fdbrestore start --waitfordone` succeeds, `fdbcli getrange` returns data |
| audit-snapshot CSV bundle | `postgres:16-alpine` | per-table `\copy` succeeds, `SELECT count(*) FROM audit.events` is greater than zero |
| Yugabyte pg_dump | `postgres:16-alpine` | `psql` accepts the dump, `SELECT count(*) FROM audit.events` is greater than zero |
| Temporal-DB pg_dump | `postgres:16-alpine` | dump loads, at least 5 public tables exist, the `executions` table is present |
| Meilisearch volume tar | `getmeili/meilisearch:v1.12` | `/health` returns OK |

Every scratch resource is created with a unique `RUN_ID` and torn down
in an `EXIT` trap. Pass `--keep-on-failure` to retain failed scratch
containers and volumes for inspection. Successful runs always clean up.

The script defaults to expecting Tack production images. Override via:

| Variable | Default | Purpose |
|----------|---------|---------|
| `TACK_FDB_IMAGE` | `foundationdb/foundationdb:7.4.6` | match the prod image tag |
| `TACK_PG_IMAGE` | `postgres:16-alpine` | scratch Postgres for dumps |
| `TACK_MEILI_IMAGE` | `getmeili/meilisearch:v1.12` | match the prod Meilisearch tag |
| `TACK_FDB_OVERLAY` | autodetected from `<scriptdir>/../fdb-overlay/fdb.bash` | mount the IPv6-aware fdb.bash overlay so the scratch FDB container starts cleanly on the v6-only Docker bridge in CT 117 |

Run on demand:

```
ssh tack 'ts=$(cat /root/backups/.latest); /root/tack/scripts/backup-restore-test.sh "/root/backups/tack-$ts"'
```

Or via the Make target on CT 117:

```
ssh tack 'cd /root/tack && make backup-restore-test'
```

## Scheduling the restore-test (systemd)

Two unit files ship in `scripts/systemd/`:

```
scripts/systemd/tack-backup-restore-test.service
scripts/systemd/tack-backup-restore-test.timer
```

Install on CT 117:

```
ssh tack '
  cp /root/tack/scripts/systemd/tack-backup-restore-test.service /etc/systemd/system/
  cp /root/tack/scripts/systemd/tack-backup-restore-test.timer   /etc/systemd/system/
  systemctl daemon-reload
  systemctl enable --now tack-backup-restore-test.timer
'
```

Verify the timer is armed:

```
ssh tack 'systemctl list-timers tack-backup-restore-test.timer --no-pager'
```

Trigger an immediate run:

```
ssh tack 'systemctl start tack-backup-restore-test.service && journalctl -u tack-backup-restore-test.service -e --no-pager'
```

The service exits non-zero on any artifact failure. Operator-side
monitoring of failed systemd units (existing host-maintenance pattern)
will surface the failure within hours of the next backup.

## Restoring from a backup

These steps assume layer 2 has passed against the same backup directory.
Do not attempt a restore that has not been restore-tested on a scratch
cluster first.

### FDB

```
ssh tack '
  cd /root/tack
  docker compose stop app
  ts=$(cat /root/backups/.latest)
  src=/root/backups/tack-$ts
  # Stage the fdbbackup output where the production FDB container can
  # see it. Use a host bind mount, not the data volume.
  mkdir -p /root/restore-staging
  tar -xzf "$src/fdb-snapshot.tar.gz" -C /root/restore-staging
  # Replay into the live cluster. Run from a one-shot container that
  # shares the cluster file.
  docker run --rm \
    -v tack_fdb-config:/etc/foundationdb:ro \
    -v /root/restore-staging:/restore:ro \
    foundationdb/foundationdb:7.4.6 \
    fdbrestore start --dest-cluster-file /etc/foundationdb/fdb.cluster \
                     -r file:///restore/fdbbackup/backup-... \
                     --waitfordone
  docker compose start app
'
```

### Yugabyte (auth and audit)

If you have a `pg_dump` artifact, load it into a fresh database:

```
ssh tack '
  cat /root/backups/tack-<ts>/yugabyte.sql \
    | docker exec -i tack-yugabyte-1 ysqlsh -h yugabyte -p 5433 -U yugabyte -d tack
'
```

If you only have the audit-snapshot CSV bundle, recreate the schema and
`\copy` per table. Preserve `audit.chain_heads` from the running
production database where possible; restoring chain_heads from a stale
snapshot resets the hash chain.

### Meilisearch

```
ssh tack '
  docker compose stop meilisearch
  docker run --rm \
    -v tack_meili-data:/dst \
    -v /root/backups/tack-<ts>:/src:ro \
    alpine sh -c "cd /dst && rm -rf ./* && tar -xzf /src/tack_meili-data.tar.gz"
  docker compose start meilisearch
'
```

### Temporal-DB

```
ssh tack '
  docker compose stop temporal
  cat /root/backups/tack-<ts>/temporal-db.sql \
    | docker exec -i tack-temporal-db-1 psql -U temporal -d temporal
  docker compose start temporal
'
```

## When alerts fire

### Content check failed

The backup itself is suspect. Re-run the backup. If the content check
fails again, do not treat the new backup as a recovery resource; capture
the artifact list and shapes, escalate, and take a manual snapshot
following the `manual_audit_backup` pattern from the 2026-05-09 incident
directory.

### Restore-test failed

The backup is structurally fine but cannot be restored. Common causes
and triage steps:

| Symptom | Likely cause | First step |
|---------|--------------|------------|
| `fdbbackup describe failed` | truncated fdbbackup output (incomplete drain) | check the live `fdbbackup status` on production for in-flight backups, re-run the backup, do not delete the failed archive |
| `fdbrestore start failed` after describe ok | mutation log corruption, missing snapshot file | run with `--keep-on-failure`, inspect the scratch container's `/var/fdb/logs` |
| `psql refused the dump` | DDL conflict from a partial schema diff | inspect `head -200` of the dump, confirm it matches the current production schema; this can mean the production schema drifted |
| `audit.events count is zero` | `\copy` succeeded but the source CSV had only the header | inspect the CSV inside the bundle |
| `Meilisearch /health never returned` | volume tar missing `data.ms/` or version mismatch with `TACK_MEILI_IMAGE` | confirm `TACK_MEILI_IMAGE` matches the prod tag exactly |

In every case, retain the failed backup directory until the incident is
closed.

## Negative tests (proof the verification is real)

The verification scripts are useless if they pass on broken backups.
The repository ships negative test fixtures and the build report
records four passing checks:

1. positive: real `fdb-snapshot-20260509T051802Z.tar.gz` and real `audit-snapshot-20260509T164222Z.tar.gz` both pass.
2. negative: an FDB-shaped tarball containing only `lib/` and `data/` (the exact 2026-04-25 defect signature) fails with the expected error message.
3. negative: a tarball truncated to 4 KiB fails with `tar -tzf failed`.
4. negative: an empty `.sql` and a non-empty `.sql` with no `CREATE TABLE` both fail.

Re-run these checks before shipping any change to the verification
scripts. See `incident_2026-05-09_seed_parallel_org/backup_restore_test_report.md`
for the build verification log.

## Change-control rules

- Do not edit the verification scripts to silence a failure. If a check
  is wrong, the right fix is to make the check more accurate, not to
  loosen it.
- Do not skip the restore-test before treating a backup as authoritative.
- Do not delete a failed backup until layer 2 passes against a newer
  backup.
