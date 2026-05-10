#!/usr/bin/env bash
# backup-content-check.sh
#
# Fast structural verification of a Tack backup directory. Does NOT
# restore; only inspects each artifact's file inventory and sniffs a
# sample of its contents. Exits non-zero on the first defect.
#
# This script exists because the 2026-04-25 to 2026-05-09 production
# window shipped empty FDB tarballs every day for two weeks without
# anyone noticing. The minimum viable defense is a content-shape check
# that runs in seconds and refuses to pass on an empty archive.
#
# Usage:
#   backup-content-check.sh <backup-dir>
#
# Detected artifacts in <backup-dir> (any subset; missing ones are noted
# as warnings unless --strict is passed):
#
#   fdb-snapshot*.tar.gz | fdbbackup*.tar.gz | tack_fdb-data*.tar.gz
#       FDB physical or fdbbackup logical archive.
#   yugabyte*.tar.gz | tack_yugabyte-data*.tar.gz | audit-snapshot*.tar.gz
#       Yugabyte volume tar OR audit-table CSV bundle.
#   *.sql | yugabyte*.sql.gz | temporal-db*.sql.gz
#       pg_dump output for either Yugabyte or Temporal-DB.
#   meili*.tar.gz | tack_meili-data*.tar.gz
#       Meilisearch volume tar.
#
# Exit codes:
#   0  every detected artifact passed every check.
#   1  at least one artifact failed at least one check.
#   2  invocation error (missing arg, unreadable directory).

set -euo pipefail

readonly SCRIPT_NAME="backup-content-check.sh"

STRICT=0
BACKUP_DIR=""

usage() {
    cat <<EOF
usage: ${SCRIPT_NAME} [--strict] <backup-dir>

  --strict    treat missing artifact categories as errors, not warnings.
EOF
}

parse_args() {
    while (( $# > 0 )); do
        case "$1" in
            --strict)
                STRICT=1
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

# Track results.
PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
FAILURES=()

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
    FAILURES+=("${artifact}: ${detail}")
    echo "FAIL  ${artifact}: ${detail}" >&2
}

log_warn() {
    local artifact="$1"
    local detail="$2"
    WARN_COUNT=$((WARN_COUNT + 1))
    echo "WARN  ${artifact}: ${detail}"
}

# List a tar.gz quickly without extracting. Returns 0 on success and
# emits the file list on stdout. Returns 1 on a corrupt or empty tarball.
list_tar() {
    local tar_path="$1"
    if ! tar -tzf "${tar_path}" 2>/dev/null; then
        return 1
    fi
}

# Sniff plausible pg_dump content: must contain a CREATE TABLE statement
# somewhere in the first ~16 MiB. Handles both plain .sql and gzipped.
sniff_pg_dump() {
    local dump_path="$1"
    local reader=("cat" "${dump_path}")
    case "${dump_path}" in
        *.gz)
            reader=("gunzip" "-c" "${dump_path}")
            ;;
    esac

    # Disable pipefail in a subshell: grep -q exits on first match and SIGPIPEs
    # the upstream readers, which would otherwise fail the whole pipeline.
    if ( set +o pipefail; "${reader[@]}" 2>/dev/null | head -c 16777216 | grep -qE '^CREATE TABLE\b' ); then
        return 0
    fi
    return 1
}

check_fdb() {
    local artifact="$1"
    local listing
    if ! listing=$(list_tar "${artifact}"); then
        log_fail "$(basename "${artifact}")" "tar -tzf failed (corrupt or unreadable)"
        return
    fi

    local entry_count
    entry_count=$(printf '%s\n' "${listing}" | grep -c -v '^$' || true)
    if (( entry_count == 0 )); then
        log_fail "$(basename "${artifact}")" "archive is empty (0 entries)"
        return
    fi

    # Two valid FDB shapes:
    # 1) fdbbackup output: contains a backup-* directory with kvranges/ and
    #    snapshots/ and properties/log_begin_version.
    # 2) live volume tar: contains the .sqlite or .fdq data files. The
    #    2026-05-09 incident showed that the named-volume tar is empty in
    #    practice; we still allow it as a valid shape if the data files
    #    are actually present.
    local has_fdbbackup_marker
    local has_sqlite_data
    has_fdbbackup_marker=$(printf '%s\n' "${listing}" | grep -cE '(^|/)snapshots/snapshot,' || true)
    has_sqlite_data=$(printf '%s\n' "${listing}" | grep -cE '\.(sqlite|fdq)$' || true)

    if (( has_fdbbackup_marker > 0 )); then
        # Verify the supporting structure is intact.
        local has_logs has_kvranges has_props
        has_logs=$(printf '%s\n' "${listing}" | grep -cE '(^|/)logs/' || true)
        has_kvranges=$(printf '%s\n' "${listing}" | grep -cE '(^|/)kvranges/' || true)
        has_props=$(printf '%s\n' "${listing}" | grep -cE '(^|/)properties/log_begin_version$' || true)

        if (( has_logs == 0 )) || (( has_kvranges == 0 )) || (( has_props == 0 )); then
            log_fail "$(basename "${artifact}")" "fdbbackup archive missing logs/, kvranges/, or properties/log_begin_version"
            return
        fi
        log_pass "$(basename "${artifact}")" "fdbbackup archive (snapshots, logs, kvranges, properties present)"
    elif (( has_sqlite_data > 0 )); then
        log_pass "$(basename "${artifact}")" "FDB volume tar with ${has_sqlite_data} data file(s)"
    else
        log_fail "$(basename "${artifact}")" "no fdbbackup snapshot marker AND no .sqlite/.fdq files (this is the 2026-04-25 empty-backup signature)"
    fi
}

check_yugabyte_tar() {
    local artifact="$1"
    local listing
    if ! listing=$(list_tar "${artifact}"); then
        log_fail "$(basename "${artifact}")" "tar -tzf failed"
        return
    fi

    # Audit-snapshot bundles: the 2026-05-09 manual snapshot ships per-table
    # CSVs under sql/ plus a MANIFEST.txt. That is a valid shape.
    local audit_csv_count
    audit_csv_count=$(printf '%s\n' "${listing}" | grep -cE '(^|/)sql/[^/]+\.csv$' || true)
    local has_manifest
    has_manifest=$(printf '%s\n' "${listing}" | grep -cE '(^|/)MANIFEST\.txt$' || true)
    if (( audit_csv_count >= 4 )) && (( has_manifest > 0 )); then
        # Audit bundle must include events, chain_heads, notarizations, pii.
        local missing=""
        local table
        for table in events chain_heads notarizations pii; do
            if ! printf '%s\n' "${listing}" | grep -qE "(^|/)sql/${table}\.csv$"; then
                missing="${missing} ${table}"
            fi
        done
        if [[ -n "${missing}" ]]; then
            log_fail "$(basename "${artifact}")" "audit bundle missing tables:${missing}"
            return
        fi
        # Verify events.csv has at least a header. Extract just that one
        # entry to a temp file and check; cheap because CSV is small.
        local tmpdir
        tmpdir=$(mktemp -d)
        local cleanup_dir="${tmpdir}"
        # shellcheck disable=SC2317
        cleanup_tmp() { rm -rf "${cleanup_dir}"; }
        trap cleanup_tmp EXIT
        # Find the sql/events.csv member name from the listing and extract
        # only that one entry. This is portable across GNU and BSD tar; the
        # --wildcards flag is GNU-only.
        local events_member
        events_member=$(printf '%s\n' "${listing}" | grep -E '(^|/)sql/events\.csv$' | head -1)
        if [[ -z "${events_member}" ]]; then
            log_fail "$(basename "${artifact}")" "audit bundle listing claimed events.csv but member not found"
            cleanup_tmp
            trap - EXIT
            return
        fi
        if ! tar -xzf "${artifact}" -C "${tmpdir}" "${events_member}" 2>/dev/null; then
            log_fail "$(basename "${artifact}")" "could not extract events.csv from audit bundle"
            cleanup_tmp
            trap - EXIT
            return
        fi
        local events_csv
        events_csv="${tmpdir}/${events_member}"
        if [[ -z "${events_csv}" ]] || ! head -1 "${events_csv}" | grep -qE '^org_id,'; then
            log_fail "$(basename "${artifact}")" "events.csv has no recognizable header"
            cleanup_tmp
            trap - EXIT
            return
        fi
        cleanup_tmp
        trap - EXIT
        log_pass "$(basename "${artifact}")" "audit CSV bundle (${audit_csv_count} csv files, manifest present, events header valid)"
        return
    fi

    # Plain volume tar: must contain pg_data, postgresql.conf, or yb-data
    # directory entries. An empty volume-tar is the same defect class as
    # the FDB defect.
    local entry_count
    entry_count=$(printf '%s\n' "${listing}" | grep -c -v '^$' || true)
    if (( entry_count == 0 )); then
        log_fail "$(basename "${artifact}")" "archive is empty (0 entries)"
        return
    fi

    local has_data
    has_data=$(printf '%s\n' "${listing}" | grep -cE '(yb-data|pg_data|postgresql\.conf|tablet-)' || true)
    if (( has_data == 0 )); then
        log_fail "$(basename "${artifact}")" "volume tar has no Yugabyte data markers (yb-data, pg_data, postgresql.conf, tablet-*)"
        return
    fi
    log_pass "$(basename "${artifact}")" "Yugabyte volume tar (${entry_count} entries, ${has_data} data marker(s))"
}

check_pg_dump() {
    local artifact="$1"
    local label
    label=$(basename "${artifact}")
    if [[ ! -s "${artifact}" ]]; then
        log_fail "${label}" "dump file is empty (0 bytes)"
        return
    fi
    if ! sniff_pg_dump "${artifact}"; then
        log_fail "${label}" "no CREATE TABLE statement found in first 16 MiB"
        return
    fi
    log_pass "${label}" "pg_dump shape valid (CREATE TABLE present)"
}

check_meilisearch() {
    local artifact="$1"
    local listing
    if ! listing=$(list_tar "${artifact}"); then
        log_fail "$(basename "${artifact}")" "tar -tzf failed"
        return
    fi

    local entry_count
    entry_count=$(printf '%s\n' "${listing}" | grep -c -v '^$' || true)
    if (( entry_count == 0 )); then
        log_fail "$(basename "${artifact}")" "archive is empty"
        return
    fi

    # Meilisearch persistent state lives under data.ms/. The directory
    # contains an instance-uid file plus per-index subdirectories.
    local has_data_ms
    has_data_ms=$(printf '%s\n' "${listing}" | grep -cE '(^|/)data\.ms(/|$)' || true)
    local has_uid
    has_uid=$(printf '%s\n' "${listing}" | grep -cE 'instance-uid' || true)
    if (( has_data_ms == 0 )) && (( has_uid == 0 )); then
        log_fail "$(basename "${artifact}")" "no data.ms/ or instance-uid (suspicious empty Meilisearch volume)"
        return
    fi
    log_pass "$(basename "${artifact}")" "Meilisearch volume tar (${entry_count} entries)"
}

# Detect category from filename. Returns one of: fdb yugabyte_tar pg_dump
# meilisearch unknown.
detect_category() {
    local name
    name=$(basename "$1")
    case "${name}" in
        fdb-snapshot*.tar.gz|fdbbackup*.tar.gz|tack_fdb-data*.tar.gz|tack_fdb-cluster*.tar.gz)
            echo fdb
            ;;
        audit-snapshot*.tar.gz|yugabyte*.tar.gz|tack_yugabyte-data*.tar.gz)
            echo yugabyte_tar
            ;;
        *.sql|*.sql.gz)
            echo pg_dump
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

    echo ">> backup-content-check ${BACKUP_DIR}"

    local saw_fdb=0 saw_yb=0 saw_temporal=0 saw_meili=0
    local artifact category
    while IFS= read -r -d '' artifact; do
        category=$(detect_category "${artifact}")
        case "${category}" in
            fdb)
                saw_fdb=1
                check_fdb "${artifact}"
                ;;
            yugabyte_tar)
                saw_yb=1
                check_yugabyte_tar "${artifact}"
                ;;
            pg_dump)
                # Distinguish by filename whether this is Temporal-DB or Yugabyte.
                local name
                name=$(basename "${artifact}")
                case "${name}" in
                    temporal*|*temporal-db*)
                        saw_temporal=1
                        ;;
                    *)
                        saw_yb=1
                        ;;
                esac
                check_pg_dump "${artifact}"
                ;;
            meilisearch)
                saw_meili=1
                check_meilisearch "${artifact}"
                ;;
            unknown)
                # Skip silently. Some directories carry CSV exports, manifest
                # files, etc. that are not their own first-class artifacts.
                ;;
        esac
    done < <(find "${BACKUP_DIR}" -maxdepth 2 -type f \( -name '*.tar.gz' -o -name '*.sql' -o -name '*.sql.gz' \) -print0)

    local missing=""
    if (( saw_fdb == 0 )); then missing="${missing} fdb"; fi
    if (( saw_yb == 0 )); then missing="${missing} yugabyte"; fi
    if (( saw_temporal == 0 )); then missing="${missing} temporal-db"; fi
    if (( saw_meili == 0 )); then missing="${missing} meilisearch"; fi

    if [[ -n "${missing}" ]]; then
        if (( STRICT == 1 )); then
            for cat in ${missing}; do
                log_fail "${cat}" "no artifact found for category"
            done
        else
            for cat in ${missing}; do
                log_warn "${cat}" "no artifact found for category (run with --strict to fail)"
            done
        fi
    fi

    echo ""
    echo "summary: pass=${PASS_COUNT} fail=${FAIL_COUNT} warn=${WARN_COUNT}"
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
