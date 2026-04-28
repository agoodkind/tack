#!/usr/bin/env bash
# Bring up the single-node FDB cluster used by integration tests.
#
# The upstream FoundationDB image starts a server but does not configure a
# usable database on first boot. This script brings the container up and
# bootstraps "single memory" once when needed.
#
# Tests run in a sibling container (see docker-compose.test.yml `tests`
# service); they read the cluster file directly from the shared
# tack-test-fdb-config volume, so this script does not extract it to the host.
#
# Idempotent. Running it twice against an already configured cluster just
# verifies the database answers status.

set -euo pipefail

cd "$(dirname "$0")/.."

# Pick a container runtime CLI that speaks compose. Tack production runs
# Docker on CT 117 (with the IPv6-only NDP-proxied bridge), so local dev
# defaults to Docker for parity. Podman is accepted when Docker is missing
# or its daemon is not running.
#
# Order:
#   1. Caller-supplied DOCKER_BIN.
#   2. docker, when its compose plugin is registered AND the daemon answers.
#   3. podman, when a machine is up.
#   4. Hard fail with an actionable hint.
#
# Locate a docker binary even when /usr/local/bin is absent from PATH
# (Make's non-interactive shells often only have /opt/homebrew/bin).
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
    echo "start Docker Desktop (open -a Docker), or run: podman machine init --now" >&2
    exit 1
  fi
fi

# Docker Desktop ships docker-credential-desktop in its app bundle. Make and
# non-interactive shells inherit a minimal PATH that does not include it, so
# add it explicitly when present. Without this, image pulls fail with
# "exec: docker-credential-desktop: executable file not found in $PATH".
if [[ -d /Applications/Docker.app/Contents/Resources/bin ]]; then
  export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"
fi

echo ">> bringing up test FDB"
"${COMPOSE[@]}" -f docker-compose.test.yml up -d fdb

# Inside the container, the image writes the authoritative cluster file to
# /var/fdb/fdb.cluster. Wait for it to exist before bootstrapping.
for _ in $(seq 1 30); do
  if "${COMPOSE[@]}" -f docker-compose.test.yml exec -T fdb test -f /var/fdb/fdb.cluster; then
    break
  fi
  sleep 1
done

# Bootstrap the database only when status is not yet available.
if ! "${COMPOSE[@]}" -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null'; then
  echo ">> configuring single-node FDB"
  "${COMPOSE[@]}" -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 20 fdbcli --exec "configure new single memory"'
fi

# Wait for the database to answer status.
for _ in $(seq 1 30); do
  if "${COMPOSE[@]}" -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null'; then
    break
  fi
  sleep 1
done

# Sync the in-container cluster file into a stable location the FDB
# container itself owns. The fdb.bash entrypoint writes
# /var/fdb/fdb.cluster (process-local). For the sibling test runner we want
# /etc/foundationdb/fdb.cluster to point at the same coordinator. The
# cluster file is on the tack-test-fdb-config volume already mounted into
# both services.
"${COMPOSE[@]}" -f docker-compose.test.yml exec -T fdb sh -lc \
  'cp /var/fdb/fdb.cluster /etc/foundationdb/fdb.cluster'

echo ">> FDB ready"
