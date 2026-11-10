# Provision an environment

Prepare the guest, stack, secret files, network, and engine containers through a
reviewed Configs change before running Tack provisioning. Validate migrations,
seeding, backfills, and restores on QA before production. Run tests in QA or
Docker containers; production tests require explicit operator authorization.

The current [Configs release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf)
updates application images and runs checks against an existing installation.
It does not install a fresh stack, prepare credentials, or apply migrations.
Confirm a reviewed Configs procedure for those prerequisites before a fresh
installation or QA replacement. Do not substitute the former Ansible commands
or hand-edit live configuration.

## Confirm the target and identity

1. Record the target environment, guest identities, Configs revision, Tack
   revision, image tags and digests, and current store state. Check the live
   guest and container state against the reviewed configuration.
2. Confirm the mounted cluster directory, coordinator list, database
   credentials, audit role passwords, and any required backup configuration.
   Require a verified empty FoundationDB cluster before authorizing initial
   configuration. A failed status query does not prove that a cluster is empty.
   Never run `configure new` against an existing cluster or pass
   `--allow-fdb-init` on production.
3. Supply the actual operator identity to every direct command. Agent runs use
   `--operator-service`, `--operator-session`, `--operator-id`, and
   `--operator-email` for the agent and accountable person. Supply
   `--operator-name` when the command needs the person's display name. Inspect
   the command's dry run and reject a shared or incorrect identity before
   adding `--execute`. A `docker compose run` command needs its own identity
   arguments; it does not inherit the app service's normal `serve` arguments.

## Prepare an empty ledger

1. For a from-empty rebuild, supply the engine superuser credential only for
   bootstrap. Keep that credential out of the rendered environment file.
   Use the `tack_migrator` role for normal migrations and provisioning.
2. Run the audited `ops ledger audit-bootstrap` command against the first empty
   ledger node. Review its dry run, then execute with the verified identity.
   The command applies migrations and prepares audit roles. It refuses a ledger
   that already contains the migration table, operator outbox, or operator
   login. A connection failure must stop bootstrap.
3. Join ledger nodes one at a time through the reviewed deployment procedure.
   Require the audited `ops ledger bootstrap-wait` result after the second node
   before starting the third. Require three live masters, three registered
   tablet servers, replication factor three, and zero under-replicated tablets
   before completing a three-node bootstrap.

## Run ordered provisioning

1. Run `ops provision` through `tack-ops` with the verified identity. Inspect its
   dry run before adding `--execute`. Add `--allow-fdb-init` only for the
   verified fresh, nonproduction cluster. Start enough store processes to
   support the configured redundancy before initialization.
2. Inspect every recorded step. Provisioning checks FoundationDB first, applies
   migrations, creates or rotates audit login roles, starts continuous
   FoundationDB backup when configured, and then seeds product data when both
   `SEED_EMAIL` and `SEED_NAME` exist. A failed step stops later work.
3. If provisioning reports `seed_deferred`, start the app through the reviewed
   deployment procedure and rerun provisioning. The command reports an
   incomplete outcome when it cannot execute the seed in the app container.
   Do not treat that outcome as a ready environment. When either seed setting
   is absent, complete the direct seed procedure below before declaring an
   environment that needs initial product data ready.
4. Require pending migrations before starting the corresponding app and audit
   consumer images. An image-only release does not apply them. Use the audited
   `migrate` command when a separate migration run is necessary.
5. Check consumer startup and topic creation. The audit consumer retries its
   database connection until the ledger accepts it, then ensures the audit
   topic. A fresh broker does not require manual topic creation. Verify actual
   topic settings and consumer progress before declaring the audit path ready.

Provisioning runs store administration in one-shot containers through the
Docker socket. It does not require a local FoundationDB server container on the
owner guest. The store overlay preserves an existing nonempty cluster file;
it does not replace that file with the local process address on every start.

## Seed initial product data

Use `/server seed` to create the initial user, organization, workspace, and API
token.

1. Complete pending migrations through `migrate` or the ordered `ops provision`
   migration step before invoking seed.
2. Use a one-shot `app` container with its bridge network and writable
   FoundationDB cluster-directory mount. Confirm that the command can connect
   to FoundationDB and YugabyteDB and that ledger bootstrap and role preparation
   have completed.
3. Mount the reviewed database URL for `tack_migrator` as a command-only secret
   file and set `DATABASE_URL_FILE` to its path. Supply the operator-outbox
   secret through `AUDIT_OPERATOR_DSN_FILE`. The seed runs migrations and creates
   authentication records as well as product nodes. Keep these command-only
   credentials and mounts separate from the normal app's `tack_app` credentials.
4. Set both `SEED_EMAIL` and `SEED_NAME` for the seed invocation. To supply a
   specific API token, mount its secret file and set `SEED_API_TOKEN_FILE` to
   that file. Otherwise, the command generates a token.
5. Confirm that the target does not contain organization memberships. The
   command refuses existing `org_members` rows unless `--allow-reseed` is set.
   Use that override only for an explicitly authorized reseed.
6. Invoke `/server seed` in the one-shot app container with the verified
   operator identity from the preflight. Inspect its identity preview without
   `--execute`, then add `--execute` to run the seed. The preview does not perform
   the membership check or validate `SEED_EMAIL` and `SEED_NAME`.
7. Save the newly printed production token in a protected secret file. The
   command does not print an existing token again. Keep the token out of shared
   command evidence, logs, and chat. Use the API token for production
   authentication; the printed user UUID is a bearer only in development.

## Verify readiness and preserve audit controls

1. Run audited `ops deploy verify` with the reviewed image tag and expected
   server and consumer digests. Record image verification separately from
   provisioning and public behavior checks.
2. Check FoundationDB status, ledger replication, audit partitions, signer
   verification, configured backup sessions, and public behavior. Preserve
   previous accepted signing keys when retained backups still require them.
3. Use `tack-ops` for the recorded `ops db sql` command if a diagnostic SQL read
   is necessary. The command requires a reason, sends alarm mail, records
   intent, and permits one statement in a read-only database session. An
   undelivered alarm or failed intent record prevents execution. Use a
   migration or reviewed ops command for database writes. Preserve the audit
   and partman schema guard.
4. Record the final revisions, image digests, operator provenance, command
   outcomes, and verification evidence. Stop on a failed audited command
   instead of repairing the database with direct SQL or a shell script.
