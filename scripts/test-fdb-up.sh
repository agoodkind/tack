#!/usr/bin/env bash
# Bring up the single-node FDB cluster used by integration tests.
#
# Reads the FDB-generated cluster file from inside the container, rewrites
# the host:port to 127.0.0.1:4500 so a host process can reach the cluster
# through Docker's published port mapping, and writes the rewritten file to
# .test-fdb/fdb.cluster. The make test-integration target points
# FDB_CLUSTER_FILE at that path.
#
# This script is idempotent. Running it twice does not break a running
# cluster; it just refreshes the cluster file.

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p .test-fdb

echo ">> bringing up test FDB"
docker compose -f docker-compose.test.yml up -d --wait fdb

# Inside the container, the FDB image initializes a single-node cluster on
# first boot and writes /etc/foundationdb/fdb.cluster. Wait for it to exist.
for _ in $(seq 1 30); do
  if docker compose -f docker-compose.test.yml exec -T fdb test -f /etc/foundationdb/fdb.cluster; then
    break
  fi
  sleep 1
done

# Extract the file. It looks like:
#   description:id@172.18.0.2:4500
docker compose -f docker-compose.test.yml exec -T fdb cat /etc/foundationdb/fdb.cluster > .test-fdb/fdb.cluster.raw

# Rewrite the coordinator address to 127.0.0.1:4500 so the host can reach the
# cluster via the published port mapping. Match the whole coordinator suffix so
# bracketed IPv6 literals from the IPv6-safe wrapper are handled too.
sed -E 's|@.*$|@127.0.0.1:4500|' .test-fdb/fdb.cluster.raw > .test-fdb/fdb.cluster
chmod 644 .test-fdb/fdb.cluster

echo ">> cluster file at .test-fdb/fdb.cluster:"
cat .test-fdb/fdb.cluster
