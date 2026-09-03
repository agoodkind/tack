#!/usr/bin/env bash
# Run the database-gated audit tests against the test YugabyteDB alone.
#
# The chain-append, outbox, and token tests skip without a migrated ledger
# DSN. This brings up only the yugabyte service from docker-compose.test.yml,
# migrates it through goose inside the sibling test runner, and runs the gated
# packages there with AUDIT_CHAIN_TEST_DSN set. FoundationDB is not started,
# so this works on hosts where the FDB image cannot run.
#
# The yugabyte service is pinned to the amd64 image (see the compose file):
# the arm64 build crashes under the concurrency test (TACK-459).
#
# The database keeps its volume between runs; `make test-fdb-down` removes it.

set -euo pipefail

cd "$(dirname "$0")/.."

# Mirror test-fdb-up.sh runtime selection.
find_docker() {
    if command -v docker >/dev/null 2>&1; then
        command -v docker
        return 0
    fi
    for p in /usr/local/bin/docker /Applications/Docker.app/Contents/Resources/bin/docker; do
        if [[ -x "$p" ]]; then
            echo "$p"
            return 0
        fi
    done
    return 1
}

COMPOSE=()
if [[ -n "${DOCKER_BIN:-}" ]]; then
    COMPOSE=("$DOCKER_BIN" compose)
else
    DOCKER_PATH="$(find_docker || true)"
    if [[ -n "$DOCKER_PATH" ]] \
        && "$DOCKER_PATH" compose version >/dev/null 2>&1 \
        && "$DOCKER_PATH" info >/dev/null 2>&1; then
        COMPOSE=("$DOCKER_PATH" compose)
    elif command -v podman >/dev/null 2>&1 && podman info >/dev/null 2>&1; then
        COMPOSE=(podman compose)
    else
        echo "no container runtime with a running compose-capable daemon found" >&2
        exit 1
    fi
fi

if [[ -d /Applications/Docker.app/Contents/Resources/bin ]]; then
    export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"
fi

# The password is set when the data volume is first created and must match on
# every later start. It is kept beside the compose project, never in the
# compose file, so secret scanners see no literal value.
PASSWORD_FILE=.test-audit-db/password
if [[ -z "${TEST_YB_PASSWORD:-}" ]]; then
    if [[ ! -f "$PASSWORD_FILE" ]]; then
        mkdir -p "$(dirname "$PASSWORD_FILE")"
        openssl rand -hex 16 > "$PASSWORD_FILE"
        chmod 600 "$PASSWORD_FILE"
    fi
    TEST_YB_PASSWORD="$(cat "$PASSWORD_FILE")"
fi
export TEST_YB_PASSWORD

# Percent-encode one URI user-info component, so a caller-supplied password
# holding @ : / ? % or # still parses as the password and not as the host.
url_encode() {
    local value="$1" out="" char
    local i
    # Bytewise, so a non-ASCII password encodes as its UTF-8 bytes rather
    # than as code points.
    local LC_ALL=C
    for (( i = 0; i < ${#value}; i++ )); do
        char="${value:i:1}"
        case "$char" in
            [A-Za-z0-9._~-]) out+="$char" ;;
            *) out+="$(printf '%%%02X' "'$char")" ;;
        esac
    done
    printf '%s' "$out"
}

yb_dsn() {
    local user="$1" pw="$2" host="$3" db="$4"
    printf '%s://%s:%s@%s/%s?sslmode=disable' postgres "$user" "$(url_encode "$pw")" "$host" "$db"
}
export TEST_DATABASE_URL="$(yb_dsn yugabyte "$TEST_YB_PASSWORD" yugabyte:5433 tack)"
export TEST_AUDIT_WRITER_DSN="$(yb_dsn audit_writer_app "$TEST_YB_PASSWORD" yugabyte:5433 tack)"
export TEST_AUDIT_READER_DSN="$(yb_dsn audit_reader_app "$TEST_YB_PASSWORD" yugabyte:5433 tack)"

echo ">> bringing up test YugabyteDB"
"${COMPOSE[@]}" -f docker-compose.test.yml up -d yugabyte

# Wait until the database answers a query as the admin role, the same probe
# the service's healthcheck runs, so this works on any compose implementation.
READY=0
for (( attempt = 0; attempt < 60; attempt++ )); do
    if "${COMPOSE[@]}" -f docker-compose.test.yml exec -T -e PGPASSWORD="$TEST_YB_PASSWORD" yugabyte \
        ysqlsh -h yugabyte -p 5433 -U yugabyte -d tack -t -c 'SELECT 1' >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 5
done
if [[ "$READY" -ne 1 ]]; then
    echo "test YugabyteDB did not answer within five minutes; check: ${COMPOSE[*]} -f docker-compose.test.yml logs yugabyte" >&2
    exit 1
fi

"${COMPOSE[@]}" -f docker-compose.test.yml --profile runner build tests

# The goose command pulls in every database driver it supports, none of which
# this module needs, so it cannot run from the module's own go.sum. Running it
# by an explicit version, the one go.mod already pins for the library, keeps
# the migrator and the runtime on the same goose without widening go.sum.
GOOSE_VERSION="$("${COMPOSE[@]}" -f docker-compose.test.yml --profile runner run --rm \
    --no-deps --entrypoint /usr/local/go/bin/go tests \
    list -m -f '{{.Version}}' github.com/pressly/goose/v3 | tr -d '\r')"
echo ">> migrating with goose ${GOOSE_VERSION}"
"${COMPOSE[@]}" -f docker-compose.test.yml --profile runner run --rm \
    --no-deps --entrypoint /usr/local/go/bin/go tests \
    run "github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}" -dir migrations postgres "$TEST_DATABASE_URL" up

# One package at a time (-p 1): the gated packages share this one database,
# and run together the org backfill tests see each other's orgs and the
# concurrency test's read lands on a catalog the other package is migrating.
echo ">> running database-gated audit tests"
exec "${COMPOSE[@]}" -f docker-compose.test.yml --profile runner run --rm \
    --no-deps -e AUDIT_CHAIN_TEST_DSN="$TEST_DATABASE_URL" \
    --entrypoint /usr/local/go/bin/go tests \
    test -count=1 -p 1 ./internal/audit/... ./internal/ops/... ./cmd/server/... "$@"
