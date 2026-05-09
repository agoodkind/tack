#!/usr/bin/env bash
# Snapshot every Tack datastore using durable mechanisms:
#   FDB:           fdbbackup against the live cluster via a backup_agent
#                  sidecar. The named tack_fdb-data volume is empty
#                  because the FDB image declares VOLUME /var/fdb/data,
#                  which spawns an anonymous volume that shadows the
#                  named mount. Tar of the named volume captures
#                  nothing useful, so we use the FDB-native path.
#   Yugabyte:      pg_dump of public + audit schemas (auth + compliance
#                  audit ledger). No live volume tar.
#   Temporal-DB:   pg_dump of the temporal database (workflow state).
#   Meilisearch:   tar of the named volume (no shadowing risk).
#
# Output: /root/backups/tack-<UTC-timestamp>/ with one subdirectory per
# component plus a MANIFEST.txt of sha256+size+relpath for every file.
#
# Idempotent on crash: any leftover backup_agent sidecar from a previous
# run is removed at start, and on every exit path (success, failure,
# signal). Retention is handled by scripts/host-maintenance.sh when it
# is present.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=scripts/backup-functions.sh
source "${SCRIPT_DIR}/backup-functions.sh"

TACK_BACKUP_ROOT="${TACK_BACKUP_ROOT:-/root/backups}"
TACK_BACKUP_SNAPSHOT_ROOT="${TACK_BACKUP_SNAPSHOT_ROOT:-/root/fdb-snapshots}"
TACK_BACKUP_COMPOSE_FILE="${TACK_BACKUP_COMPOSE_FILE:-/root/tack/docker-compose.yml}"
TACK_BACKUP_ENV_FILE="${TACK_BACKUP_ENV_FILE:-/root/tack/.env}"
TACK_BACKUP_FDB_NETWORK="${TACK_BACKUP_FDB_NETWORK:-tack_default}"
TACK_BACKUP_FDB_IMAGE="${TACK_BACKUP_FDB_IMAGE:-foundationdb/foundationdb:7.4.6}"
TACK_BACKUP_FDB_TIMEOUT_SECONDS="${TACK_BACKUP_FDB_TIMEOUT_SECONDS:-1800}"
TACK_BACKUP_MEILI_VOLUME="${TACK_BACKUP_MEILI_VOLUME:-tack_meili-data}"
TACK_BACKUP_FDB_SIDECAR="${TACK_BACKUP_FDB_SIDECAR:-tack-backup-agent}"

TS=$(date -u +%Y%m%dT%H%M%SZ)
RUN_ID="snapshot-${TS}"
DEST="${TACK_BACKUP_ROOT}/tack-${TS}"
SNAPSHOT_DIR="${TACK_BACKUP_SNAPSHOT_ROOT}/${RUN_ID}"

CLEANUP_DONE=0
function tack_backup_cleanup() {
    if [[ "$CLEANUP_DONE" -eq 1 ]]; then
        return
    fi
    CLEANUP_DONE=1
    tack_backup_kill_sidecar "$TACK_BACKUP_FDB_SIDECAR"
}

function tack_backup_on_signal() {
    tack_backup_cleanup
    exit 130
}

trap tack_backup_cleanup EXIT
trap tack_backup_on_signal INT TERM

# Remove any leftover sidecar from a prior crashed run before we start.
tack_backup_kill_sidecar "$TACK_BACKUP_FDB_SIDECAR"

mkdir -p "$DEST/fdb" "$DEST/yugabyte" "$DEST/temporal-db" "$DEST/meilisearch"
mkdir -p "$SNAPSHOT_DIR"
echo ">> backup dir: $DEST"
echo ">> fdb snapshot dir: $SNAPSHOT_DIR"

# ===========================================================================
# FDB: fdbbackup with sidecar backup_agent.
#
# The 2026-05-09 incident proved that without a running backup_agent,
# fdbbackup start enqueues the backup but nothing moves. The agent must
# be on the same network as the cluster and must override the image's
# default entrypoint, which exits immediately. The describe URL must
# point at the timestamped backup-* subdirectory inside /snapshot/<run-id>,
# not the parent. All of this is wrapped in a configurable timeout so a
# misregistered agent fails the run rather than hangs it.
# ===========================================================================
echo ">> fdb (fdbbackup via sidecar agent, timeout=${TACK_BACKUP_FDB_TIMEOUT_SECONDS}s)"
timeout "${TACK_BACKUP_FDB_TIMEOUT_SECONDS}" bash -c "$(cat <<'INNER'
set -euo pipefail
SCRIPT_DIR="$1"
SIDECAR="$2"
IMAGE="$3"
NETWORK="$4"
SNAPSHOT_DIR="$5"
RUN_ID="$6"
# shellcheck source=scripts/backup-functions.sh
source "${SCRIPT_DIR}/backup-functions.sh"
tack_backup_start_fdb_sidecar "$SIDECAR" "$IMAGE" "$NETWORK" "$SNAPSHOT_DIR"
tack_backup_run_fdb_start "$IMAGE" "$NETWORK" "$SNAPSHOT_DIR" "$RUN_ID"
INNER
)" _ \
    "$SCRIPT_DIR" \
    "$TACK_BACKUP_FDB_SIDECAR" \
    "$TACK_BACKUP_FDB_IMAGE" \
    "$TACK_BACKUP_FDB_NETWORK" \
    "$SNAPSHOT_DIR" \
    "$RUN_ID"

FDB_BACKUP_URL=$(tack_backup_resolve_fdb_subdir "$TACK_BACKUP_SNAPSHOT_ROOT" "$RUN_ID")
echo ">> fdb backup url: $FDB_BACKUP_URL"
tack_backup_assert_restorable \
    "$TACK_BACKUP_FDB_IMAGE" \
    "$TACK_BACKUP_FDB_NETWORK" \
    "$TACK_BACKUP_SNAPSHOT_ROOT" \
    "$FDB_BACKUP_URL" \
    >"$DEST/fdb/describe.txt"

# Materialize the fdbbackup tree into the per-run output directory.
tar -C "$SNAPSHOT_DIR" -czf "$DEST/fdb/fdbbackup.tar.gz" .
printf '%s\n' "$FDB_BACKUP_URL" >"$DEST/fdb/backup_url.txt"

# Tear down the sidecar now that we have the artifact.
tack_backup_kill_sidecar "$TACK_BACKUP_FDB_SIDECAR"

# ===========================================================================
# Yugabyte: pg_dump of public + audit schemas.
# ===========================================================================
echo ">> yugabyte (pg_dump public + audit)"
set -a
# shellcheck disable=SC1090
source "$TACK_BACKUP_ENV_FILE"
set +a
if [[ -z "${YUGABYTE_PASSWORD:-}" ]]; then
    echo "YUGABYTE_PASSWORD missing from $TACK_BACKUP_ENV_FILE" >&2
    exit 1
fi
tack_backup_dump_yugabyte \
    "$DEST/yugabyte/tack.sql" \
    "$YUGABYTE_PASSWORD" \
    "${YUGABYTE_USER:-yugabyte}" \
    "${YUGABYTE_DB:-tack}"

# ===========================================================================
# Temporal-DB: pg_dump of the temporal workflow database.
# Defaults match docker-compose.yml's temporal-db service env block.
# ===========================================================================
echo ">> temporal-db (pg_dump)"
tack_backup_dump_temporal_db \
    "$DEST/temporal-db/temporal.sql" \
    "${TACK_BACKUP_TEMPORAL_DB_PASSWORD:-temporal}" \
    "${TACK_BACKUP_TEMPORAL_DB_USER:-temporal}" \
    "${TACK_BACKUP_TEMPORAL_DB_NAME:-temporal}"

# ===========================================================================
# Meilisearch: tar of the named volume. No shadowing risk here.
# ===========================================================================
echo ">> meilisearch (named volume tar)"
tack_backup_dump_meilisearch \
    "$DEST/meilisearch/${TACK_BACKUP_MEILI_VOLUME}.tar.gz" \
    "$TACK_BACKUP_MEILI_VOLUME"

# ===========================================================================
# Manifest. One line per file: sha256, size, relpath.
# ===========================================================================
echo ">> manifest"
tack_backup_write_manifest "$DEST"

echo "$TS" >"${TACK_BACKUP_ROOT}/.latest"

if [[ -x /root/tack/scripts/host-maintenance.sh ]]; then
    /root/tack/scripts/host-maintenance.sh rotate-backups
fi

echo ""
echo "complete:"
ls -lhR "$DEST"
