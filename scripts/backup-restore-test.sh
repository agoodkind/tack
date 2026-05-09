#!/usr/bin/env bash
# backup-restore-test.sh
#
# Restore-test verification for Tack backups. Spins up a scratch
# container per artifact, restores into it, runs a sanity query, and
# tears the container down. Exits non-zero on any verification failure.
#
# Designed to run on the deployment host (CT 117) where Docker is
# available. The companion script backup-content-check.sh runs anywhere
# and catches structural defects in seconds; this script catches
# defects that a structural check cannot, like a corrupt fdbbackup
# archive that lists fine but cannot be replayed.
#
# Usage:
#   backup-restore-test.sh [--keep-on-failure] <backup-dir>
#
# --keep-on-failure leaves the scratch containers and volumes alive on
# failure so the operator can inspect them. Without the flag, every run
# leaves zero scratch resources behind, success or fail.
#
# Required tools on the host:
#   docker, tar, gzip, sha256sum (or shasum -a 256)
#
# Exit codes:
#   0  every detected artifact restored cleanly and passed its sanity query.
#   1  at least one artifact failed.
#   2  invocation error or missing prerequisite.

set -euo pipefail

readonly SCRIPT_NAME="backup-restore-test.sh"
RUN_ID_RAW="rt$(date -u +%Y%m%dT%H%M%SZ)-$$"
readonly RUN_ID="${RUN_ID_RAW}"
readonly FDB_IMAGE="${TACK_FDB_IMAGE:-foundationdb/foundationdb:7.4.6}"
readonly PG_IMAGE="${TACK_PG_IMAGE:-postgres:16-alpine}"
readonly MEILI_IMAGE="${TACK_MEILI_IMAGE:-getmeili/meilisearch:v1.12}"

# Optional path to the IPv6-aware fdb.bash overlay from the repo. On the
# production host (CT 117) the Docker bridge is IPv6-only, and the upstream
# foundationdb/foundationdb image's fdb.bash entrypoint does not bracket
# IPv6 addresses, so a scratch container fails with "Could not parse
# network address" before the cluster file is ever written. Mounting the
# repo's overlay fixes that. The script auto-detects the overlay when
# invoked from a Tack checkout; set TACK_FDB_OVERLAY to override.
SCRIPT_DIR_RAW="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR="${SCRIPT_DIR_RAW}"
FDB_OVERLAY="${TACK_FDB_OVERLAY:-${SCRIPT_DIR}/../fdb-overlay/fdb.bash}"
if [[ ! -f "${FDB_OVERLAY}" ]]; then
    FDB_OVERLAY=""
fi
readonly FDB_OVERLAY

KEEP_ON_FAILURE=0
BACKUP_DIR=""

# Resources that need teardown. Tracked globally so the trap can clean
# them up regardless of where the script exits from.
SCRATCH_CONTAINERS=()
SCRATCH_VOLUMES=()
SCRATCH_DIRS=()
INTERRUPTED=0

usage() {
    cat <<EOF
usage: ${SCRIPT_NAME} [--keep-on-failure] <backup-dir>

  --keep-on-failure   leave scratch containers and volumes alive when a
                      check fails so the operator can inspect them.
EOF
}

parse_args() {
    while (( $# > 0 )); do
        case "$1" in
            --keep-on-failure)
                KEEP_ON_FAILURE=1
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            -*)
                echo "${SCRIPT_NAME}: unknown flag: $1" >&2
                usage >&2
                exit 2
                ;;
            *)
                if [[ -n "${BACKUP_DIR}" ]]; then
                    echo "${SCRIPT_NAME}: extra positional arg: $1" >&2
                    exit 2
                fi
                BACKUP_DIR="$1"
                shift
                ;;
        esac
    done
    if [[ -z "${BACKUP_DIR}" ]]; then
        usage >&2
        exit 2
    fi
    if [[ ! -d "${BACKUP_DIR}" ]]; then
        echo "${SCRIPT_NAME}: not a directory: ${BACKUP_DIR}" >&2
        exit 2
    fi
}

check_prereqs() {
    if ! command -v docker >/dev/null 2>&1; then
        echo "${SCRIPT_NAME}: docker is required and was not found" >&2
        exit 2
    fi
    if ! docker info >/dev/null 2>&1; then
        echo "${SCRIPT_NAME}: docker daemon is not reachable" >&2
        exit 2
    fi
}

# shellcheck disable=SC2329  # invoked from EXIT trap.
cleanup() {
    local force_keep="${1:-0}"
    if (( force_keep == 1 )); then
        echo ">> --keep-on-failure: leaving ${#SCRATCH_CONTAINERS[@]} container(s) and ${#SCRATCH_VOLUMES[@]} volume(s) for inspection"
        return
    fi
    local container volume dir
    if (( ${#SCRATCH_CONTAINERS[@]} > 0 )); then
        for container in "${SCRATCH_CONTAINERS[@]}"; do
            docker rm -f "${container}" >/dev/null 2>&1 || true
        done
    fi
    if (( ${#SCRATCH_VOLUMES[@]} > 0 )); then
        for volume in "${SCRATCH_VOLUMES[@]}"; do
            docker volume rm "${volume}" >/dev/null 2>&1 || true
        done
    fi
    if (( ${#SCRATCH_DIRS[@]} > 0 )); then
        for dir in "${SCRATCH_DIRS[@]}"; do
            rm -rf "${dir}"
        done
    fi
}

# shellcheck disable=SC2329  # invoked from INT/TERM trap.
on_signal() {
    INTERRUPTED=1
    echo ""
    echo ">> interrupted, cleaning up scratch resources"
    cleanup 0
    exit 130
}

# shellcheck disable=SC2329  # invoked from EXIT trap.
on_exit() {
    if (( INTERRUPTED == 1 )); then
        return
    fi
    if (( KEEP_ON_FAILURE == 1 )) && (( EXIT_FAIL == 1 )); then
        cleanup 1
    else
        cleanup 0
    fi
}

trap on_signal INT TERM
trap on_exit EXIT

# Result tracking.
PASS_COUNT=0
FAIL_COUNT=0
EXIT_FAIL=0
FAILURES=()

log_step() {
    echo "++ $*"
}

log_pass() {
    local artifact="$1"
    local detail="$2"
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "PASS  ${artifact}: ${detail}"
}

log_fail() {
    local artifact="$1"
    local detail="$2"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    EXIT_FAIL=1
    FAILURES+=("${artifact}: ${detail}")
    echo "FAIL  ${artifact}: ${detail}" >&2
}

new_scratch_dir() {
    local dir
    dir=$(mktemp -d)
    SCRATCH_DIRS+=("${dir}")
    echo "${dir}"
}

new_scratch_volume() {
    local name="$1"
    docker volume create "${name}" >/dev/null
    SCRATCH_VOLUMES+=("${name}")
}

new_scratch_container() {
    SCRATCH_CONTAINERS+=("$1")
}

# Wait for a docker exec command to succeed within a timeout. Returns 0
# on success and 1 on timeout. Polls once per second.
wait_for_exec() {
    local container="$1"
    local timeout_seconds="$2"
    shift 2
    local elapsed=0
    while (( elapsed < timeout_seconds )); do
        if docker exec "${container}" "$@" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    return 1
}

# ------------------------------------------------------------------
# FDB
# ------------------------------------------------------------------
restore_fdb() {
    local artifact="$1"
    local label
    label=$(basename "${artifact}")
    log_step "FDB restore-test: ${label}"

    local extract_dir
    extract_dir=$(new_scratch_dir)
    if ! tar -xzf "${artifact}" -C "${extract_dir}" 2>/dev/null; then
        log_fail "${label}" "tar -xzf failed"
        return
    fi

    # Locate fdbbackup root (a directory containing snapshots/ and logs/).
    local fdbbackup_root
    fdbbackup_root=$(find "${extract_dir}" -type d -name 'backup-*' -print -quit 2>/dev/null || true)
    if [[ -z "${fdbbackup_root}" ]]; then
        log_fail "${label}" "no fdbbackup backup-* directory inside archive (cannot replay)"
        return
    fi

    # Spin up a scratch FDB container with a fresh data volume.
    local volume_name="tack-rt-fdb-data-${RUN_ID}"
    local container_name="tack-rt-fdb-${RUN_ID}"
    new_scratch_volume "${volume_name}"
    new_scratch_container "${container_name}"

    # The foundationdb/foundationdb image's entrypoint requires
    # FDB_CLUSTER_FILE_CONTENTS or FDB_COORDINATOR. We pass an explicit
    # cluster file targeting the container's own v4 loopback so the
    # entrypoint writes a self-referential cluster file. On the IPv6-only
    # production host we additionally mount the repo's IPv6-aware fdb.bash
    # overlay so the entrypoint brackets v6 addresses correctly.
    local docker_run_args=(
        -d --name "${container_name}"
        -e FDB_NETWORKING_MODE=container
        -e FDB_PORT=4500
        -e FDB_COORDINATOR_PORT=4500
        -e FDB_CLUSTER_FILE_CONTENTS=docker:docker@127.0.0.1:4500
        -v "${volume_name}:/var/fdb/data"
        -v "${extract_dir}:/restore:ro"
    )
    if [[ -n "${FDB_OVERLAY}" ]]; then
        docker_run_args+=( -v "${FDB_OVERLAY}:/var/fdb/scripts/fdb.bash:ro" )
    fi
    if ! docker run "${docker_run_args[@]}" "${FDB_IMAGE}" >/dev/null; then
        log_fail "${label}" "failed to start scratch FDB container"
        return
    fi

    # The upstream FoundationDB image starts the server process but does
    # not configure a usable database on first boot. Wait for the cluster
    # file to exist, then bootstrap with `configure new single memory`,
    # then wait for status to come up. The image's FDB_CLUSTER_FILE env
    # default is /var/fdb/fdb.cluster.
    if ! wait_for_exec "${container_name}" 30 test -f /var/fdb/fdb.cluster; then
        log_fail "${label}" "scratch FDB never wrote /var/fdb/fdb.cluster (30s timeout)"
        return
    fi
    if ! docker exec "${container_name}" \
        sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null 2>&1'; then
        if ! docker exec "${container_name}" \
            sh -lc 'timeout 20 fdbcli --exec "configure new single memory"' \
            >/dev/null 2>&1; then
            log_fail "${label}" "fdbcli configure new single memory failed"
            return
        fi
    fi
    if ! wait_for_exec "${container_name}" 60 \
        sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null'; then
        log_fail "${label}" "scratch FDB never reported a healthy cluster after bootstrap (60s timeout)"
        return
    fi

    # Compute the backup URL relative to the container mount.
    local backup_relpath backup_url
    backup_relpath="${fdbbackup_root#"${extract_dir}/"}"
    backup_url="file:///restore/${backup_relpath}"

    # `fdbbackup describe` parses backup metadata. It is the cheapest
    # end-to-end check that the archive is replayable; truncated or
    # corrupt archives pass structural inspection but fail describe.
    local describe_out
    if ! describe_out=$(docker exec "${container_name}" \
        fdbbackup describe -d "${backup_url}" 2>&1); then
        log_fail "${label}" "fdbbackup describe failed: ${describe_out}"
        return
    fi
    if ! echo "${describe_out}" | grep -qE 'Snapshot|Backup'; then
        log_fail "${label}" "fdbbackup describe output missing Snapshot/Backup markers: ${describe_out}"
        return
    fi

    # Start a backup_agent inside the scratch container. The agent is the
    # process that actually drains the restore queue; without it,
    # `fdbrestore start` queues forever and `fdbcli` reports "Database is
    # locked". The agent connects to the same cluster file the server uses.
    if ! docker exec -d "${container_name}" \
        sh -lc 'backup_agent --cluster-file /var/fdb/fdb.cluster --logdir /var/fdb/logs >/var/fdb/logs/backup_agent.out 2>&1'; then
        log_fail "${label}" "could not start backup_agent in scratch container"
        return
    fi
    # Give the agent a moment to register.
    sleep 2

    # Replay the snapshot into the scratch cluster. fdbrestore --waitfordone
    # blocks until the agent finishes draining; cap with a 10-minute outer
    # timeout. The 4.6 MB scratch dataset typically restores in well under
    # a minute on CT 117 hardware.
    local restore_log
    if ! restore_log=$(docker exec "${container_name}" \
        timeout 600 fdbrestore start \
        --dest-cluster-file /var/fdb/fdb.cluster \
        -r "${backup_url}" --waitfordone 2>&1); then
        log_fail "${label}" "fdbrestore start --waitfordone failed (cannot replay backup): ${restore_log}"
        return
    fi

    # The cluster ends restore in a locked state; unlock with the UID
    # the restore reported, then range-scan to assert user rows exist.
    local uid
    uid=$(docker exec "${container_name}" \
        fdbrestore status --dest-cluster-file /var/fdb/fdb.cluster 2>&1 \
        | grep -oE 'UID: [a-f0-9]{32}' | head -1 | awk '{print $2}')
    if [[ -n "${uid}" ]]; then
        docker exec "${container_name}" \
            fdbcli --exec "unlock ${uid}" >/dev/null 2>&1 || true
    fi

    local rangeout
    if ! rangeout=$(docker exec "${container_name}" \
        fdbcli --exec 'getrange "" \xff 5' 2>&1); then
        log_fail "${label}" "fdbcli getrange failed: ${rangeout}"
        return
    fi
    if ! echo "${rangeout}" | grep -qE 'Range limited to|^[\`]'; then
        log_fail "${label}" "fdbcli getrange produced no recognizable output: ${rangeout}"
        return
    fi

    log_pass "${label}" "fdbbackup describe ok, fdbrestore --waitfordone succeeded, getrange returned data"
}

# ------------------------------------------------------------------
# Postgres-shaped artifacts (Yugabyte audit + Temporal-DB)
# ------------------------------------------------------------------
restore_pg_dump() {
    local artifact="$1"
    local role_label="$2" # "yugabyte" or "temporal-db"
    local label
    label=$(basename "${artifact}")
    log_step "${role_label} restore-test: ${label}"

    local container_name="tack-rt-pg-${role_label}-${RUN_ID}"
    new_scratch_container "${container_name}"

    local rt_pg_pass
    rt_pg_pass="rt-$(openssl rand -hex 8 2>/dev/null || echo "${RANDOM}-${RANDOM}")"

    if ! docker run -d --name "${container_name}" \
        -e POSTGRES_PASSWORD="${rt_pg_pass}" \
        -e POSTGRES_DB=rttest \
        "${PG_IMAGE}" >/dev/null; then
        log_fail "${label}" "failed to start scratch Postgres"
        return
    fi

    if ! wait_for_exec "${container_name}" 30 pg_isready -U postgres; then
        log_fail "${label}" "scratch Postgres never reported ready"
        return
    fi

    # Stream the dump in. Handle gz and plain.
    local reader=("cat" "${artifact}")
    case "${artifact}" in
        *.gz) reader=("gunzip" "-c" "${artifact}") ;;
    esac

    if ! "${reader[@]}" | docker exec -i "${container_name}" \
        psql -v ON_ERROR_STOP=1 -U postgres -d rttest >/dev/null 2>&1; then
        log_fail "${label}" "psql refused the dump (syntax error or schema conflict)"
        return
    fi

    case "${role_label}" in
        yugabyte)
            # Audit ledger sanity. Tolerant of either schema-qualified or
            # search-path-only references.
            local count
            if ! count=$(docker exec "${container_name}" \
                psql -U postgres -d rttest -tAc \
                "SELECT count(*) FROM audit.events" 2>/dev/null); then
                log_fail "${label}" "SELECT count(*) FROM audit.events failed"
                return
            fi
            if [[ -z "${count}" ]] || (( count == 0 )); then
                log_fail "${label}" "audit.events restored with 0 rows"
                return
            fi
            log_pass "${label}" "audit.events count=${count}"
            ;;
        temporal-db)
            local table_count
            if ! table_count=$(docker exec "${container_name}" \
                psql -U postgres -d rttest -tAc \
                "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" 2>/dev/null); then
                log_fail "${label}" "table-count query failed"
                return
            fi
            if (( table_count < 5 )); then
                log_fail "${label}" "expected at least 5 Temporal tables, got ${table_count}"
                return
            fi
            local has_executions
            has_executions=$(docker exec "${container_name}" \
                psql -U postgres -d rttest -tAc \
                "SELECT count(*) FROM information_schema.tables WHERE table_name='executions'" 2>/dev/null || echo 0)
            if (( has_executions == 0 )); then
                log_fail "${label}" "Temporal 'executions' table is missing from restored schema"
                return
            fi
            log_pass "${label}" "Temporal schema restored (${table_count} tables, executions present)"
            ;;
    esac
}

# Audit-snapshot CSV bundle: not a pg_dump, but the 2026-05-09 manual
# snapshot ships per-table CSVs. We restore by creating the audit schema
# manually and \\copy-ing each CSV in. The sanity check is the same:
# audit.events count > 0.
restore_audit_csv_bundle() {
    local artifact="$1"
    local label
    label=$(basename "${artifact}")
    log_step "yugabyte audit-csv restore-test: ${label}"

    local extract_dir
    extract_dir=$(new_scratch_dir)
    if ! tar -xzf "${artifact}" -C "${extract_dir}" 2>/dev/null; then
        log_fail "${label}" "tar -xzf failed"
        return
    fi
    local sql_dir
    sql_dir=$(find "${extract_dir}" -type d -name sql -print -quit)
    if [[ -z "${sql_dir}" ]] || [[ ! -f "${sql_dir}/events.csv" ]]; then
        log_fail "${label}" "no sql/events.csv in audit bundle"
        return
    fi

    local container_name="tack-rt-pg-audit-${RUN_ID}"
    new_scratch_container "${container_name}"

    local rt_pg_pass
    rt_pg_pass="rt-$(openssl rand -hex 8 2>/dev/null || echo "${RANDOM}-${RANDOM}")"

    if ! docker run -d --name "${container_name}" \
        -e POSTGRES_PASSWORD="${rt_pg_pass}" \
        -e POSTGRES_DB=rttest \
        -v "${sql_dir}:/csv:ro" \
        "${PG_IMAGE}" >/dev/null; then
        log_fail "${label}" "failed to start scratch Postgres"
        return
    fi

    if ! wait_for_exec "${container_name}" 30 pg_isready -U postgres; then
        log_fail "${label}" "scratch Postgres never reported ready"
        return
    fi

    # Minimal schema inferred from events.csv header. We do not need the
    # full audit DDL; count() works with a permissive table definition.
    local schema_sql
    schema_sql=$(cat <<'EOF'
CREATE SCHEMA IF NOT EXISTS audit;
CREATE TABLE audit.events (
    org_id text, shard int, event_time timestamptz, event_id text,
    seq bigint, actor_id text, actor_kind int, action text,
    entity_kind text, entity_id text, context text, delta text,
    pii_ref text, prev_hash text, row_hash text, idempotency_key text
);
EOF
)
    if ! echo "${schema_sql}" | docker exec -i "${container_name}" \
        psql -v ON_ERROR_STOP=1 -U postgres -d rttest >/dev/null 2>&1; then
        log_fail "${label}" "could not create audit.events scratch schema"
        return
    fi

    if ! docker exec "${container_name}" \
        psql -v ON_ERROR_STOP=1 -U postgres -d rttest -c \
        "\\copy audit.events FROM '/csv/events.csv' WITH (FORMAT csv, HEADER true)" \
        >/dev/null 2>&1; then
        log_fail "${label}" "\\copy events.csv into audit.events failed"
        return
    fi

    local count
    count=$(docker exec "${container_name}" \
        psql -U postgres -d rttest -tAc \
        "SELECT count(*) FROM audit.events" 2>/dev/null || echo 0)
    if [[ -z "${count}" ]] || (( count == 0 )); then
        log_fail "${label}" "audit.events loaded with 0 rows"
        return
    fi
    log_pass "${label}" "audit-csv bundle loaded; audit.events count=${count}"
}

# ------------------------------------------------------------------
# Meilisearch
# ------------------------------------------------------------------
restore_meilisearch() {
    local artifact="$1"
    local label
    label=$(basename "${artifact}")
    log_step "meilisearch restore-test: ${label}"

    local volume_name="tack-rt-meili-data-${RUN_ID}"
    local container_name="tack-rt-meili-${RUN_ID}"
    new_scratch_volume "${volume_name}"
    new_scratch_container "${container_name}"

    # Untar the volume contents into the scratch volume via a one-shot
    # populator container. Avoids host-OS tar idiosyncrasies.
    local extract_dir
    extract_dir=$(new_scratch_dir)
    cp "${artifact}" "${extract_dir}/snapshot.tar.gz"
    if ! docker run --rm \
        -v "${volume_name}:/dst" \
        -v "${extract_dir}:/src:ro" \
        alpine sh -c 'cd /dst && tar -xzf /src/snapshot.tar.gz' >/dev/null 2>&1; then
        log_fail "${label}" "could not extract snapshot into scratch volume"
        return
    fi

    if ! docker run -d --name "${container_name}" \
        -v "${volume_name}:/meili_data" \
        -e MEILI_MASTER_KEY=rt-scratch \
        -e MEILI_NO_ANALYTICS=true \
        "${MEILI_IMAGE}" >/dev/null; then
        log_fail "${label}" "failed to start scratch Meilisearch"
        return
    fi

    if ! wait_for_exec "${container_name}" 60 wget -q -O- http://127.0.0.1:7700/health; then
        log_fail "${label}" "scratch Meilisearch /health never returned (60s timeout)"
        return
    fi

    log_pass "${label}" "scratch Meilisearch came up and reported healthy"
}

# ------------------------------------------------------------------
# Dispatch
# ------------------------------------------------------------------
detect_category() {
    local name
    name=$(basename "$1")
    case "${name}" in
        fdb-snapshot*.tar.gz|fdbbackup*.tar.gz|tack_fdb-data*.tar.gz|tack_fdb-cluster*.tar.gz)
            echo fdb
            ;;
        audit-snapshot*.tar.gz)
            echo audit_csv
            ;;
        yugabyte*.tar.gz|tack_yugabyte-data*.tar.gz)
            echo yugabyte_volume
            ;;
        temporal*.sql|temporal*.sql.gz|*temporal-db*.sql|*temporal-db*.sql.gz)
            echo pg_dump_temporal
            ;;
        *.sql|*.sql.gz)
            echo pg_dump_yugabyte
            ;;
        tack_meili-data*.tar.gz|meilisearch*.tar.gz|meili*.tar.gz)
            echo meilisearch
            ;;
        *)
            echo unknown
            ;;
    esac
}

main() {
    parse_args "$@"
    check_prereqs

    echo ">> backup-restore-test ${BACKUP_DIR} (run_id=${RUN_ID})"

    local artifact category
    while IFS= read -r -d '' artifact; do
        category=$(detect_category "${artifact}")
        case "${category}" in
            fdb)               restore_fdb "${artifact}" ;;
            audit_csv)         restore_audit_csv_bundle "${artifact}" ;;
            yugabyte_volume)   echo "WARN  $(basename "${artifact}"): live volume tar restore not implemented (use audit-snapshot or pg_dump)" ;;
            pg_dump_yugabyte)  restore_pg_dump "${artifact}" yugabyte ;;
            pg_dump_temporal)  restore_pg_dump "${artifact}" temporal-db ;;
            meilisearch)       restore_meilisearch "${artifact}" ;;
            unknown)           ;;
        esac
    done < <(find "${BACKUP_DIR}" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.sql' -o -name '*.sql.gz' \) -print0)

    echo ""
    echo "summary: pass=${PASS_COUNT} fail=${FAIL_COUNT}"
    if (( FAIL_COUNT > 0 )); then
        echo "failures:" >&2
        for entry in "${FAILURES[@]}"; do
            echo "  - ${entry}" >&2
        done
        exit 1
    fi
    exit 0
}

main "$@"
