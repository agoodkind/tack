#!/usr/bin/env bash
# Bring up the single-node FDB cluster used by integration tests.
#
# The upstream FoundationDB image starts a server and writes its cluster file
# to /var/fdb/fdb.cluster, but it does not configure a usable database on first
# boot. This script brings the container up, bootstraps "single memory" once
# when needed, then rewrites the cluster file to 127.0.0.1:4500 so host-side Go
# tests can connect through Docker's published port mapping.
#
# This script is idempotent. Running it twice against an already configured
# cluster just refreshes the rewritten host cluster file.

set -euo pipefail

cd "$(dirname "$0")/.."

DOCKER_BIN="${DOCKER_BIN:-$(command -v docker || true)}"
if [[ -z "$DOCKER_BIN" && -x /usr/local/bin/docker ]]; then
  DOCKER_BIN=/usr/local/bin/docker
fi
if [[ -z "$DOCKER_BIN" ]]; then
  echo "docker not found on PATH and /usr/local/bin/docker is unavailable" >&2
  exit 1
fi

mkdir -p .test-fdb

echo ">> bringing up test FDB"
"$DOCKER_BIN" compose -f docker-compose.test.yml up -d fdb

# Inside the container, the image writes the authoritative cluster file to
# /var/fdb/fdb.cluster. Wait for it to exist before bootstrapping.
for _ in $(seq 1 30); do
  if "$DOCKER_BIN" compose -f docker-compose.test.yml exec -T fdb test -f /var/fdb/fdb.cluster; then
    break
  fi
  sleep 1
done

# Bootstrap the database only when status is not yet available.
if ! "$DOCKER_BIN" compose -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null'; then
  echo ">> configuring single-node FDB"
  "$DOCKER_BIN" compose -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 20 fdbcli --exec "configure new single memory"'
fi

# Wait for the database to answer status before handing the cluster file to the
# host-side Go client.
for _ in $(seq 1 30); do
  if "$DOCKER_BIN" compose -f docker-compose.test.yml exec -T fdb sh -lc 'timeout 5 fdbcli --exec "status minimal" >/dev/null'; then
    break
  fi
  sleep 1
done

# Extract the file. It looks like:
#   description:id@[fda5:e728:7bcb:2::2]:4500
"$DOCKER_BIN" compose -f docker-compose.test.yml exec -T fdb cat /var/fdb/fdb.cluster > .test-fdb/fdb.cluster.raw

# Rewrite the coordinator address to 127.0.0.1:4500 so the host can reach the
# cluster via the published port mapping. Match the whole coordinator suffix so
# bracketed IPv6 literals from the IPv6-safe wrapper are handled too.
sed -E 's|@.*$|@127.0.0.1:4500|' .test-fdb/fdb.cluster.raw > .test-fdb/fdb.cluster
chmod 644 .test-fdb/fdb.cluster

echo ">> cluster file at .test-fdb/fdb.cluster:"
cat .test-fdb/fdb.cluster
