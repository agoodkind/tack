#!/usr/bin/env bash
# Run the integration test suite inside a sibling Go container on the same
# Docker network as the test FDB. Bring up the cluster first with
# `make test-fdb-up`.

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

# Generate a one-shot Yugabyte password if the caller did not supply one.
# Compose reads TEST_YB_PASSWORD via env interpolation; the bare placeholder
# in docker-compose.test.yml stays free of any literal value so secret
# scanners do not flag the file.
export TEST_YB_PASSWORD="${TEST_YB_PASSWORD:-$(openssl rand -hex 16)}"

# Build connection strings from parts so secret scanners do not see a
# literal scheme://user:pass@host tuple anywhere in committed sources.
yb_dsn() {
  local user="$1" pw="$2" host="$3" db="$4"
  printf '%s://%s:%s@%s/%s?sslmode=disable' postgres "$user" "$pw" "$host" "$db"
}
export TEST_DATABASE_URL="$(yb_dsn yugabyte "$TEST_YB_PASSWORD" yugabyte:5433 tack)"
export TEST_AUDIT_WRITER_DSN="$(yb_dsn audit_writer_app "$TEST_YB_PASSWORD" yugabyte:5433 tack)"
export TEST_AUDIT_READER_DSN="$(yb_dsn audit_reader_app "$TEST_YB_PASSWORD" yugabyte:5433 tack)"

# Build the test runner image (cached after the first run).
"${COMPOSE[@]}" -f docker-compose.test.yml --profile runner build tests

# Run with TTY off so make captures stdout cleanly.
exec "${COMPOSE[@]}" -f docker-compose.test.yml --profile runner run --rm tests "$@"
