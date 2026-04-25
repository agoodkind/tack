#!/usr/bin/env bash
# Tear down the integration test FDB cluster and remove its volumes.

set -euo pipefail

cd "$(dirname "$0")/.."

docker compose -f docker-compose.test.yml down -v
rm -f .test-fdb/fdb.cluster .test-fdb/fdb.cluster.raw
rmdir .test-fdb 2>/dev/null || true
