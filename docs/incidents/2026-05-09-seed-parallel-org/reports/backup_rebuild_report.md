# Backup script rebuild report (2026-05-09 re-spin)

This report documents the re-spin of the backup-script rebuild whose
prior output was lost during a deploy-isolation step earlier today.

## 1. Files changed

- `scripts/backup.sh` (rewritten body, original shebang and `set -euo pipefail`
  preserved). Now drives FDB via `fdbbackup` against a `backup_agent`
  sidecar, asserts `Restorable: true`, dumps Yugabyte and Temporal-DB via
  `pg_dump`, snapshots the Meilisearch named volume, writes per-component
  subdirectories under `/root/backups/tack-<TS>/`, and emits
  `MANIFEST.txt` with one `<sha256>  <size>  <relpath>` line per file.
  Idempotent: removes any leftover `tack-backup-agent` sidecar at start
  and on every exit path (EXIT, INT, TERM traps).
- `scripts/backup-functions.sh` (new). Helpers namespaced
  `tack_backup_*`: `sha256`, `filesize`, `kill_sidecar`,
  `start_fdb_sidecar` (with `pgrep -x backup_agent` readiness probe),
  `run_fdb_start`, `resolve_fdb_subdir` (handles the timestamped
  `backup-*` subdir requirement that the parent path silently fails),
  `assert_restorable`, `dump_yugabyte` (uses
  `/home/yugabyte/postgres/bin/pg_dump --schema=public --schema=audit`),
  `dump_temporal_db`, `dump_meilisearch`, and `write_manifest`.
- `.env.example`: appended a commented block of optional
  `TACK_BACKUP_*` overrides (paths, network, image, timeout, sidecar
  name, Meilisearch volume, Temporal-DB DSN). Production defaults match
  CT 117.

No edits to the forbidden files (`docker-compose.yml`,
`scripts/host-maintenance.sh`, `scripts/seed-audit-roles.sh`, anything
under `internal/`, `cmd/`, `migrations/`).

## 2. Verification results

Run from the repo root on the host machine.

| Gate | Command | Result |
| --- | --- | --- |
| Syntax | `bash -n scripts/backup.sh scripts/backup-functions.sh` | passed |
| Lint | `shellcheck scripts/backup.sh scripts/backup-functions.sh` | passed (exit 0, zero warnings after fixing one SC2034 and two SC2295 hits) |
| Em/en dash | `grep -nP '[\x{2014}\x{2013}]' scripts/backup.sh scripts/backup-functions.sh .env.example` | empty (exit 1 = no matches) |
| Bare `--` in prose | `awk '/^[[:space:]]*#/' ... | fgrep -- '--'` | only `--entrypoint` remains, which is an explicitly allowed CLI flag reference. Decorative `# ----` divider lines were replaced with `# ====` to remove ambiguity. |

Both scripts are mode `0755`.

## 3. Wet-run status

**Skipped.** The local agent environment on darwin has no usable
container runtime against the production CT 117 stack: the script
expects `tack-yugabyte-1`, `tack-temporal-db-1`, `tack-fdb-1`, and the
`tack_default` Docker network to be live. None of those exist locally.
A wet run can only be performed on CT 117 itself (`ssh tack`), which
requires an explicit operator decision because it produces a real
backup artifact and tears up and down a `tack-backup-agent` container
against the live FDB cluster.

What remains unverified by this re-spin:

- Real `fdbbackup describe` output and the `Restorable: true` assertion
  against a live cluster. The pattern is taken verbatim from
  `incident_2026-05-09_seed_parallel_org/fdb_backup_report.md` which
  does record a verified `Restorable: true` run on 2026-05-09.
- That `docker exec tack-yugabyte-1 /home/yugabyte/postgres/bin/pg_dump
  ... --schema=public --schema=audit` actually emits both schemas in
  this Yugabyte build. The two-flag form (not comma-separated) is
  documented behavior for `pg_dump` and matches the task instructions.
- That `tack-temporal-db-1` accepts `pg_dump -h localhost -U temporal
  -d temporal` with the corresponding `PGPASSWORD` value. The defaults
  are read directly from `docker-compose.yml`'s `temporal-db` service
  env block.
- That `timeout 1800 bash -c <inner>` runs the FDB step under the
  configured ceiling on the production host. `timeout(1)` is standard
  GNU coreutils on the CT 117 Linux host.

The recommended next step for an operator is to run the script on CT
117 once and then inspect `MANIFEST.txt` plus `fdb/describe.txt` to
confirm the `Restorable: true` line.

## 4. Notable design decisions

- **Sidecar lifecycle by trap, not by step.** A single
  `tack_backup_cleanup` function runs from EXIT, INT, and TERM traps.
  It is idempotent via a `CLEANUP_DONE` guard and falls through
  `kill_sidecar` even when nothing is running. The script also calls
  `kill_sidecar` once unconditionally at start to remove leftovers from
  a prior crashed run.
- **FDB step wrapped in `timeout`.** A misregistered `backup_agent`
  would otherwise wedge `fdbbackup start -w` indefinitely. The default
  1800s ceiling and the `TACK_BACKUP_FDB_TIMEOUT_SECONDS` override match
  the task spec.
- **Backup URL points at the timestamped subdir.** `fdbbackup describe`
  against `file:///snapshot/<run-id>` (parent) returns
  `Restorable: false`. The script resolves the actual `backup-*`
  subdirectory before calling `assert_restorable`, which mirrors the
  proven pattern in `fdb_backup_report.md`.
- **Per-component subdirectories.** Output layout is `fdb/`,
  `yugabyte/`, `temporal-db/`, `meilisearch/`, plus `MANIFEST.txt` at
  the run root. The manifest format is exactly
  `<sha256>  <size>  <relpath>` (two-space separators, manifest itself
  excluded), produced from a sorted `find -print0` walk so the digest
  list is reproducible.
- **No env-var leakage in `ps`.** `PGPASSWORD` is passed to `docker
  exec` via `-e PGPASSWORD=$VAR`, never as a CLI argument string. The
  Yugabyte password is sourced from `/root/tack/.env` only after the
  FDB step completes, which keeps the secret out of the FDB sidecar's
  process tree.
- **Decorative `=` dividers instead of dashes.** The original draft
  used `# ----` ASCII rules. I replaced them with `# ====` to remove
  any doubt that a comment scan would flag a "double-hyphen in prose"
  match. Functionally inert.

## 5. Things intentionally not done

- **No anonymous-volume tar fallback for FDB.** The 2026-05-09 incident
  report described that as a belt-and-suspenders artifact, but the
  task spec says `fdbbackup` is the authoritative recovery resource
  and the script must assert `Restorable: true`. Adding the live tar
  would reintroduce the consistency caveat that motivated the rebuild.
  The full `fdbbackup` tree is materialized into
  `fdb/fdbbackup.tar.gz`, which is sufficient.
- **No CSV dumps of `users`, `api_tokens`, `org_members`.** The single
  `pg_dump --schema=public --schema=audit` covers them in the same
  artifact and additionally captures the audit ledger that the prior
  script was missing entirely. Keeping the CSV path would duplicate
  data and risk drift between the two formats.
- **No automatic wet-run hook.** Per the task spec, the wet run is
  optional and operator-driven. The script is safe to invoke manually
  on CT 117.
- **No new tests under `internal/` or `cmd/`.** Out of scope per the
  forbidden-files list.
- **No retention policy changes.** The script still defers to
  `scripts/host-maintenance.sh rotate-backups` exactly as before, and
  that file is on the forbidden list.
- **No edit to `docker-compose.yml`.** Adding a persistent
  `backup_agent` Compose service would simplify the FDB path, but
  `docker-compose.yml` is forbidden in this scope. The sidecar
  bring-up-and-tear-down approach is good enough for a periodic backup
  job.
