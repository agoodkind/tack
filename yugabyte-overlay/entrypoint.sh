#!/usr/bin/env bash
set -euo pipefail

source /opt/tack/yugabyte-secret.sh
load_ysql_password
export YSQL_PASSWORD
exec "$@"
