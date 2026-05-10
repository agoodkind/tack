# Production Sweep Findings (2026-05-09)

Read-only check for additional surprises beyond the three already
documented (empty backups, address-index drift, audit drop bug). One
sweep, no mutations.

## Verdict

Fourth surprise found on the targeted backup-coverage sweep:
**`tack_temporal-db-data` is not in the backup script**. Detail in
section 11.

Three other systems checked (Yugabyte, Meilisearch, Temporal main
container) are clean of the FDB-style anonymous-volume defect.

## What I checked

### Container health

All seven production containers report healthy and have not restarted
unexpectedly:

- `tack-app-1`: up 16 hours (since the maintenance-window restart)
- `tack-yugabyte-1`: up 4 days, healthy
- `tack-meilisearch-1`: up 5 days, healthy
- `tack-fdb-1`: up 5 days, healthy
- `tack-temporal-1`, `tack-temporal-db-1`, `tack-temporal-ui-1`: up
  4-5 days

### Disk usage

CT 117 root volume at 24% (22GB of 99GB used). No pressure. The
fdb-snapshots and audit-snapshot tarballs are accounted for in this.

### Recent app errors

`docker logs --since 1h tack-app-1` filtered to `error|fail|panic` and
excluding the known `audit.wal` lines: empty. No other errors in the
last hour.

### Yugabyte migrations

`public.goose_db_version` shows three rows: 0 (init), 1 (`001_schema.sql`),
2 (`002_audit.sql`), all applied 2026-05-05 02:34 UTC. No failed
migration entries.

### Auth tables

- `users`: 1 row.
- `api_tokens`: 5 rows for 1 user. Five tokens accumulated because
  each `seed` run creates a new one (documented in deployment-landmines
  memory as item 4). Not a new surprise; worth a future cleanup.
- `org_members`: 1 row, pointing at the legacy org
  `019dc5ad-0408-7e43-9c4d-d3e6736ac058`. Confirms the forward-fix's
  Phase 5 SQL DELETE worked: the stale new-org membership row is
  gone, only the legacy membership remains.

### Audit table state confirms drop bug

`audit.events` since 2026-05-09 06:00:00 UTC shows only state-change
verbs landing (notarizer activity). Read-class verbs absent. This is
the expected pattern given the known WAL drop bug; consistent with
the Phase 1 plan's diagnosis.

### What I did NOT check

- FDB key-space audit beyond what was already covered in the
  remediation playbook.
- Meilisearch index state.
- Temporal workflow state.
- IPv6 routing or NDP proxy state (network-layer, not a typical
  audit-style surprise).
- Backup volume mount hierarchy beyond the FDB issue (other
  named-volume shadowing risks for Yugabyte, Meili, Temporal).

The last item is worth a follow-up: the FDB anonymous-volume defect
might exist for other Compose services. A separate sweep targeted
specifically at that pattern could surface a fourth issue. Not done
in this pass; flagged.

## Notable but not a surprise

- 5 API tokens for 1 user. Each prior seed run printed a new one. The
  deployment-landmines memory item 4 already calls this out as a known
  pattern. Worth a future pass to delete stale tokens once the
  seed-during-transition fix lands.

## Action items surfaced

- Stale API token cleanup. Five tokens for one user, only one in
  active use. The unused four should be removed and the user's MCP
  config refreshed if it points at a stale one.
- Temporal-DB backup coverage gap. See section 11.

## 11. Targeted backup-coverage sweep

A second pass after the initial sweep, focused on the FDB
anonymous-volume defect pattern across other Compose services.

### Method

For each persistent-state container (`tack-yugabyte-1`,
`tack-meilisearch-1`, `tack-fdb-1`, `tack-temporal-1`,
`tack-temporal-db-1`) inspect the image's declared `VOLUME` paths and
the container's actual mount table. Anonymous-volume shadowing exists
when an image declares a `VOLUME` at a path that is a child of (or
identical to) a named-volume mount. The named-volume tar then misses
the actual data.

### Per-service findings

**`tack-fdb-1`**: defect already documented in retro section 1A. Image
declares `VOLUME /var/fdb/data`. Named volume `tack_fdb-data` mounts
at `/var/fdb`. Anonymous volume `7a90eb88...` shadows at
`/var/fdb/data` where the actual data lives. Backup script tars the
named volume which captures only `lib/`, trace logs, and an empty
`data/`.

**`tack-yugabyte-1`**: clean. Image declares `VOLUME /mnt/disk0` and
`VOLUME /mnt/disk1`. Yugabyte is configured with
`--base_dir=/home/yugabyte/var`, and the named volume
`tack_yugabyte-data` mounts at that path, not at the declared VOLUME
paths. The anonymous volumes at `/mnt/disk0` and `/mnt/disk1` hold
only a `cores` directory (one is empty); they are unused mount points
in this deployment. Backup script uses in-container `tar` of
`/home/yugabyte/var`, which captures the right data.

**`tack-meilisearch-1`**: clean. No `VOLUME` declarations in the image.
Named volume `tack_meili-data` mounts directly at `/meili_data`.
Backup script captures `tack_meili-data` correctly.

**`tack-temporal-1`** (the main Temporal server): clean. No `VOLUME`
declarations and no mounts. State is delegated to `tack-temporal-db-1`.
Nothing to back up at the Temporal-server layer.

**`tack-temporal-db-1`** (PostgreSQL): no anonymous-volume defect.
Image declares `VOLUME /var/lib/postgresql/data`, named volume
`tack_temporal-db-data` mounts AT that exact path. The named volume
is the real data store. **However**, `scripts/backup.sh` does not
include `tack_temporal-db-data` in its volume list.

### Severity of the Temporal-DB gap

Verified via direct inspection of the volume on the host:

- 77 MB of active PostgreSQL data.
- `pg_logical` directory modified 2026-05-09 13:32 UTC (the last
  hour). The DB is in active use.
- Files dated 2026-04-09 to 2026-05-09. Continuous activity since
  initial provisioning.

Workflow state lives here. If the Temporal stack needs recovery, the
operator has nothing to restore from. In-flight workflows would be
lost. Completed workflow history (used for replay and debugging)
would be lost.

### Recommended action

- Add `tack_temporal-db-data` to the backup script's volume list, OR
  switch to a logical backup via `pg_dump` from inside the
  `tack-temporal-db-1` container. The latter is preferred because it
  produces a portable, restore-tested artifact rather than a live
  volume tar.
- Alternatively, document explicitly that Temporal workflow state is
  ephemeral by design and accept the loss-on-recovery posture. If
  this is the chosen direction, audit current Temporal usage to
  confirm no workflow type relies on durability.

### Severity ranking of the backup defects found today

1. FDB data: empty backups for two weeks. Critical.
2. Temporal-DB: not backed up at all. Severity depends on Temporal
   usage in Tack; potentially as critical as #1 depending on what
   workflows run there.
3. Yugabyte: backups work via in-container tar. Live-tar consistency
   caveat applies (live tar of a running PostgreSQL is not a clean
   snapshot). Same caveat the original CLAUDE.md flag covered.

The Phase 1 / backup-rebuild track in the post-incident roadmap
should now treat the Temporal-DB gap as a peer of the FDB defect, not
a footnote.
