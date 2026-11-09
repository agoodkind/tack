# Environment validation

Tack reads server settings from environment variables through `caarlos0/env`.
It does not load a server configuration file. Configs owns deployed settings,
secret files, and guest preparation. Changes to deployed settings require a
reviewed Configs change; operators must not edit live settings by hand.

Compose validates required interpolation expressions before it creates
containers. The server then resolves secret file references and parses its
configuration before it selects a command. Commands validate inputs that only
their own operations require.

These checks have different scopes. A setting that a backup command needs does
not automatically become a required setting for the HTTP server.

## Secret file references

A supported secret setting can use a value or a corresponding `_FILE`
environment variable. The configuration loader rejects a nonempty value with
a file reference, an empty file path, an unreadable file, or an empty secret.
It removes trailing carriage returns and newlines from file contents. It gives
the resolved value to the configuration parser without changing the process
environment.

Compose mounts the required secret files separately for each service. The
audit role passwords remain necessary: `ops audit seed-roles` creates or rotates
the audit logins and the application login, then sets the migrator password.
Removing those passwords would prevent role preparation.

## Command startup

The server loads configuration before it builds the command tree. Help and
version handling therefore do not bypass configuration loading. The command
tree does not provide a top-level `status` configuration report.

`DATABASE_URL` has a required parser tag in the shared server configuration.
The standalone audit consumer has its own required settings. Command-specific
checks remain necessary for operations such as role preparation, seeding, and
backup. A missing required input must fail with an error that identifies the
input.

## Audit service networking

The app mounts its application and operator credentials. It does not mount
ledger reader or redactor credentials. Audit host commands open the pools that
their reads, exports, or redactions require.

Queue commands run through `audit-ops` on the Docker bridge. The service
supplies a ledger reader connection, Kafka settings, and the FoundationDB
cluster file. The commands use a separate operator connection to record
access under the resolved operator identity. Inspection, offset reports, and replay
previews require this sequence:

1. The command obtains successful audit admission.
2. The command performs the read.
3. The command finalizes the audit evidence before returning the result.

- `ops audit dlq inspect --execute` reads the dead-letter summary from the ledger.
- `ops audit consumer-offsets --execute` reads committed offsets and the SQL
  operator outbox from YugabyteDB, partition end offsets from Kafka, and the
  operator outbox entry count from FoundationDB.
- `ops audit dlq replay` reads the ledger to select rows even for a preview.
  With `--execute`, the command records intent before selecting rows and opens
  a Kafka producer only when it has rows to publish.

The host-networked `tack-ops` service cannot resolve the bridge broker name.
It provides database administration credentials, Docker access, and alarm mail
settings for other operator commands.
