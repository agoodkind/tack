#!/usr/bin/env bash
# Helper functions for scripts/backup.sh.
#
# All functions are namespaced tack_backup_*. The caller is expected to
# have already exported the runtime globals (DEST, RUN_ID, FDB_SIDECAR,
# COMPOSE_FILE, ENV_FILE, NETWORK, FDB_IMAGE, FDB_TIMEOUT). Each function
# uses snake_case locals and returns nonzero on failure so that the
# top-level set -euo pipefail aborts the run.

# Compute SHA-256 of one file. Uses the host's sha256sum if present,
# otherwise falls back to shasum -a 256. Prints the hex digest only.
function tack_backup_sha256() {
    local file_path="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$file_path" | awk '{print $1}'
        return
    fi
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$file_path" | awk '{print $1}'
        return
    fi
    echo "tack_backup_sha256: no sha256sum or shasum available" >&2
    return 1
}

# Print byte size of one file using POSIX wc -c, which is portable across
# BSD and GNU stat variants.
function tack_backup_filesize() {
    local file_path="$1"
    wc -c <"$file_path" | tr -d ' '
}

# Remove any leftover sidecar from a previous crashed run. Safe to call
# even when nothing is running.
function tack_backup_kill_sidecar() {
    local container_name="$1"
    if docker ps -a --format '{{.Names}}' | grep -Fxq "$container_name"; then
        docker rm -f "$container_name" >/dev/null 2>&1 || true
    fi
}

# Start the FDB backup_agent sidecar. The default entrypoint of the FDB
# image exits immediately, which is why we override --entrypoint here.
# Readiness probe: pgrep -x backup_agent inside the sidecar (proven on
# 2026-05-09).
function tack_backup_start_fdb_sidecar() {
    local container_name="$1"
    local image="$2"
    local network="$3"
    local snapshot_dir="$4"
    docker run -d \
        --name "$container_name" \
        --network "$network" \
        -v /etc/foundationdb:/etc/foundationdb:ro \
        -v "${snapshot_dir}:/snapshot" \
        --entrypoint /usr/bin/backup_agent \
        "$image" \
        -C /etc/foundationdb/fdb.cluster >/dev/null

    local _attempt
    for _attempt in 1 2 3 4 5 6 7 8 9 10; do
        if docker exec "$container_name" pgrep -x backup_agent >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    echo "tack_backup_start_fdb_sidecar: backup_agent not ready in $container_name" >&2
    return 1
}

# Run fdbbackup start -w against the live cluster from a one-shot client
# container on the same network. Returns when the backup is complete.
function tack_backup_run_fdb_start() {
    local image="$1"
    local network="$2"
    local snapshot_dir="$3"
    local run_id="$4"
    docker run --rm \
        --network "$network" \
        -v /etc/foundationdb:/etc/foundationdb:ro \
        -v "${snapshot_dir}:/snapshot" \
        --entrypoint /usr/bin/fdbbackup \
        "$image" \
        start -w -d "file:///snapshot/${run_id}"
}

# Resolve the timestamped backup subdirectory that fdbbackup created
# under /snapshot/<run-id>/. The describe URL must point at this
# subdirectory, not the parent (the parent reports Restorable: false).
function tack_backup_resolve_fdb_subdir() {
    local snapshot_dir="$1"
    local run_id="$2"
    local subdir
    subdir=$(find "${snapshot_dir}/${run_id}" -mindepth 1 -maxdepth 2 -type d -name 'backup-*' 2>/dev/null | head -n 1)
    if [[ -z "$subdir" ]]; then
        echo "tack_backup_resolve_fdb_subdir: no backup-* subdir under ${snapshot_dir}/${run_id}" >&2
        return 1
    fi
    local rel="${subdir#"${snapshot_dir}"/}"
    printf 'file:///snapshot/%s' "$rel"
}

# Assert that fdbbackup describe reports Restorable: true for the given
# backup URL. Errors out otherwise.
function tack_backup_assert_restorable() {
    local image="$1"
    local network="$2"
    local snapshot_dir="$3"
    local backup_url="$4"
    local describe_output
    describe_output=$(docker run --rm \
        --network "$network" \
        -v /etc/foundationdb:/etc/foundationdb:ro \
        -v "${snapshot_dir}:/snapshot" \
        --entrypoint /usr/bin/fdbbackup \
        "$image" \
        describe -d "$backup_url")
    printf '%s\n' "$describe_output"
    if ! grep -q '^Restorable: true' <<<"$describe_output"; then
        echo "tack_backup_assert_restorable: backup $backup_url is not restorable" >&2
        return 1
    fi
}

# pg_dump the Yugabyte tack database (public + audit schemas) to a plain
# SQL file in the destination. DSN built from YUGABYTE_PASSWORD which the
# caller sourced from /root/tack/.env.
function tack_backup_dump_yugabyte() {
    local out_file="$1"
    local pg_password="$2"
    local pg_user="${3:-yugabyte}"
    local pg_db="${4:-tack}"
    docker exec \
        -e PGPASSWORD="$pg_password" \
        tack-yugabyte-1 \
        /home/yugabyte/postgres/bin/pg_dump \
        -h yugabyte -p 5433 -U "$pg_user" -d "$pg_db" \
        --schema=public --schema=audit \
        --format=plain --no-owner --no-privileges \
        >"$out_file"
}

# pg_dump the Temporal PostgreSQL DB to a plain SQL file. The DSN values
# match docker-compose.yml's temporal-db service env block.
function tack_backup_dump_temporal_db() {
    local out_file="$1"
    local pg_password="${2:-temporal}"
    local pg_user="${3:-temporal}"
    local pg_db="${4:-temporal}"
    docker exec \
        -e PGPASSWORD="$pg_password" \
        tack-temporal-db-1 \
        pg_dump \
        -h localhost -U "$pg_user" -d "$pg_db" \
        --format=plain --no-owner --no-privileges \
        >"$out_file"
}

# Snapshot the Meilisearch named volume by tar-ing it from a throwaway
# alpine container. Meilisearch has no anonymous-volume shadowing and the
# volume is the real data store, so this preserves the prior, working
# behavior verbatim.
function tack_backup_dump_meilisearch() {
    local out_file="$1"
    local volume_name="${2:-tack_meili-data}"
    docker run --rm \
        -v "${volume_name}:/src:ro" \
        -v "$(dirname "$out_file"):/dst" \
        alpine \
        sh -c "cd /src && tar czf /dst/$(basename "$out_file") ."
}

# Walk DEST and write MANIFEST.txt with one line per file:
#   <sha256>  <size>  <relpath>
# Excludes the manifest itself.
function tack_backup_write_manifest() {
    local dest="$1"
    local manifest="${dest}/MANIFEST.txt"
    : >"$manifest"
    local file_path
    while IFS= read -r -d '' file_path; do
        local rel="${file_path#"${dest}"/}"
        if [[ "$rel" == "MANIFEST.txt" ]]; then
            continue
        fi
        local digest
        local size
        digest=$(tack_backup_sha256 "$file_path")
        size=$(tack_backup_filesize "$file_path")
        printf '%s  %s  %s\n' "$digest" "$size" "$rel" >>"$manifest"
    done < <(find "$dest" -type f -print0 | sort -z)
}
