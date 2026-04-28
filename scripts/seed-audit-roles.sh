#!/usr/bin/env bash
# Create the three LOGIN-capable derived roles for the audit subsystem and
# rotate their passwords on every run.
#
# The migration in migrations/002_audit.sql created four NOLOGIN base roles:
#   audit_writer, audit_reader, audit_redactor, audit_archiver
# This script adds three LOGIN derived roles that inherit from them:
#   audit_writer_app, audit_reader_app, audit_redactor_app
# The archiver role stays NOLOGIN. Its login derivative is created on demand
# by the partition rotation operator, never the application.
#
# Passwords are read from /root/tack/.env. Variables required:
#   AUDIT_WRITER_PASSWORD
#   AUDIT_READER_PASSWORD
#   AUDIT_REDACTOR_PASSWORD
#
# The script is idempotent: re-running rotates the passwords to whatever
# .env currently holds.
set -euo pipefail

ENV_FILE=${ENV_FILE:-/root/tack/.env}
[ -f "$ENV_FILE" ] || { echo "no env file at $ENV_FILE" >&2; exit 1; }

# shellcheck disable=SC1090
. "$ENV_FILE"

: "${AUDIT_WRITER_PASSWORD:?AUDIT_WRITER_PASSWORD required in $ENV_FILE}"
: "${AUDIT_READER_PASSWORD:?AUDIT_READER_PASSWORD required in $ENV_FILE}"
: "${AUDIT_REDACTOR_PASSWORD:?AUDIT_REDACTOR_PASSWORD required in $ENV_FILE}"
: "${YUGABYTE_PASSWORD:?YUGABYTE_PASSWORD required in $ENV_FILE}"

# Render the SQL on the host, ship via stdin to ysqlsh inside the container
# so we sidestep dollar-quoting headaches that shell-quote-through-docker
# imposes on inline -c arguments.

build_sql() {
    cat <<SQL
DO \$do\$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_writer_app') THEN
        CREATE ROLE audit_writer_app LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '$AUDIT_WRITER_PASSWORD';
    ELSE
        ALTER ROLE audit_writer_app PASSWORD '$AUDIT_WRITER_PASSWORD';
    END IF;
    GRANT audit_writer TO audit_writer_app;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_reader_app') THEN
        CREATE ROLE audit_reader_app LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '$AUDIT_READER_PASSWORD';
    ELSE
        ALTER ROLE audit_reader_app PASSWORD '$AUDIT_READER_PASSWORD';
    END IF;
    GRANT audit_reader TO audit_reader_app;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_redactor_app') THEN
        CREATE ROLE audit_redactor_app LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '$AUDIT_REDACTOR_PASSWORD';
    ELSE
        ALTER ROLE audit_redactor_app PASSWORD '$AUDIT_REDACTOR_PASSWORD';
    END IF;
    GRANT audit_redactor TO audit_redactor_app;
END
\$do\$;
SELECT rolname, rolcanlogin FROM pg_roles WHERE rolname LIKE 'audit_%_app' ORDER BY rolname;
SQL
}

build_sql | docker exec -i tack-yugabyte-1 env PGPASSWORD="$YUGABYTE_PASSWORD" bash -c \
    'ysqlsh -h $(getent ahostsv6 $(hostname) | awk "NR==1{print \$1}") -p 5433 -U yugabyte -d tack -f -'
