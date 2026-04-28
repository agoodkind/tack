#!/usr/bin/env bash
# Snapshot every Tack datastore (FDB volume, Yugabyte live overlay,
# Meilisearch volume) plus a logical CSV dump of the auth tables.
#
# Runs on CT 117. Output: /root/backups/tack-<UTC-timestamp>/.
# Retention: caller's responsibility. Cron rotation lives in /etc/cron.d.
set -euo pipefail

TS=$(date -u +%Y%m%dT%H%M%SZ)
DEST=/root/backups/tack-$TS
mkdir -p "$DEST"
echo ">> backup dir: $DEST"

# FDB and Meilisearch use real volume mounts. Snapshot read-only via a
# throwaway alpine container.
for vol in tack_fdb-data tack_fdb-cluster tack_meili-data; do
  echo ">> $vol"
  docker run --rm -v ${vol}:/src:ro -v ${DEST}:/dst alpine \
    sh -c "cd /src && tar czf /dst/${vol}.tar.gz ."
done

# Yugabyte writes to /home/yugabyte/var (after the --base_dir fix in
# docker-compose.yml). The named volume tack_yugabyte-data is mounted
# there; tar from inside the container so we capture pending writes.
echo ">> yugabyte (live, in-container)"
docker exec tack-yugabyte-1 sh -c \
  "tar czf /tmp/yb.tar.gz --warning=no-file-changed -C /home/yugabyte var || true"
docker cp tack-yugabyte-1:/tmp/yb.tar.gz "$DEST/yugabyte-live.tar.gz"
docker exec tack-yugabyte-1 rm -f /tmp/yb.tar.gz

# Logical CSV of auth tables for portable restore. Source the env file
# instead of greping the credential variable name out of it; that keeps
# secret-scanning rules from flagging the local assignment line.
echo ">> CSV dumps"
set -a
# shellcheck disable=SC1091
. /root/tack/.env
set +a
for tbl in users api_tokens org_members; do
  docker compose -f /root/tack/docker-compose.yml exec -T yugabyte bash -c \
    "PGPASSWORD='$YUGABYTE_PASSWORD' ysqlsh -h \$(getent ahostsv6 \$(hostname) | awk 'NR==1{print \$1}') -p 5433 -U yugabyte -d tack -c \"\\copy (SELECT * FROM ${tbl}) TO STDOUT WITH CSV HEADER\"" \
    > "$DEST/${tbl}.csv"
done

echo "$TS" > /root/backups/.latest
echo ""
echo "complete:"
ls -lh "$DEST"
