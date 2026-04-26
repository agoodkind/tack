#!/usr/bin/env bash
# Tear down the integration test FDB cluster and remove its volumes.

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

"$DOCKER_BIN" compose -f docker-compose.test.yml down -v
rm -f .test-fdb/fdb.cluster .test-fdb/fdb.cluster.raw
rmdir .test-fdb 2>/dev/null || true
