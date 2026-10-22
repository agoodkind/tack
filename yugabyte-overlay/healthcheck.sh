#!/usr/bin/env bash
set -euo pipefail

source /opt/tack/yugabyte-secret.sh
load_ysql_password
export PGPASSWORD="$YSQL_PASSWORD"
exec /home/yugabyte/postgres/bin/ysqlsh -h "${HOSTNAME:?HOSTNAME is required}" -p 5433 \
    -U "${YSQL_USER:?YSQL_USER is required}" -d "${YSQL_DB:?YSQL_DB is required}" \
    -f /opt/tack/yugabyte-healthcheck.sql -t
