# TACK-328: Audit ops commands

This plan specifies audit recording for every `./server ops` command.
Follow the [operator identity and audit requirements](../operator-identity-and-audit.md).
TACK-328 blocks TACK-327. TACK-327 restricts production data access to ops commands
only after QA verifies command auditing.

## Record commands with transactional outboxes

Each command writes an audit event into an outbox, a durable queue in a database
the command already uses. Commands do not connect to Kafka.

- FoundationDB mutations write the event and the change in one transaction.
- Other commands write events into a YugabyteDB outbox. A YugabyteDB transaction
  commits the event with the change when the operation supports that transaction.
- A relay reads both outboxes and produces events to Kafka. The audit-consumer
  writes the events into `audit.events` and remains its only writer.

The [transactional outbox pattern](https://developer.confluent.io/courses/microservices/the-transactional-outbox-pattern/)
commits a change with its event and avoids the
[dual-write problem](https://www.confluent.io/blog/dual-write-problem/).
The relay retries until Kafka accepts each event. The consumer deduplicates by
`(event_id, event_time)`, as in the
[idempotent consumer pattern](https://www.morling.dev/blog/revisiting-the-outbox-pattern/).
Auditing requires the command's database, but it does not require Kafka access.

## Enforce audit and identity requirements

- Require one shared gate in `internal/clispec` for every command. Do not weaken
  the gate, bypass it, or permit a mutating command to skip it.
- Require a durable record for every action, including reads. Commit FDB records
  with changes. Commit other records before work starts. Refuse work if recording fails.
- Resolve identity through `audit.OperatorIdentitySource`. Add source types rather
  than inline lookups. Select the source at one site.
- Do not configure identity with an environment variable or a `config.Config` field.
- Fail with a clear error when identity resolution fails in dry-run or execute mode.
- Default audited commands to dry-run. Require `--execute` for actions.
- Do not write `audit.events` directly from ops commands.
- Record global operations under a reserved system org. Resolve entity operations
  to the target node's org.
- Declare a static `AuditSpec` for each command, including inspect, verify, and validate.

## Choose recording by operation

FDB mutations, including repair apply, reindex, and backfill, commit one event in
one transaction with the change. Migrations record intent and outcome in the YB
outbox because goose manages per-migration transactions. `goose_db_version`
provides evidence for reconciliation.

Role DDL requires intent and outcome records. A test on the production-pinned
2024.2.8.0-b85 image on 2026-06-09 ran `BEGIN; CREATE ROLE; INSERT; ROLLBACK`.
Rollback removed the inserted row but did not remove the role. YugabyteDB tracks
this behavior in [issue 1404](https://github.com/yugabyte/yugabyte-db/issues/1404).
The [transactional DDL preview](https://github.com/yugabyte/yugabyte-db/blob/master/docs/content/stable/explore/transactions/transactional-ddl.md)
from v2025.1 requires `ysql_yb_ddl_transaction_block_enabled` and
`allowed_preview_flags_csv`. The preview includes tables, indexes, and roles,
but it does not support savepoints with DDL. Correctness issues remained open
in June 2026, including read-restarts in issue 32104 and savepoint failures in issue 30995.
Do not depend on this preview for the compliance ledger. During TACK-329's
2025.2.x upgrade, repeat the rollback test with the flag enabled. Use atomic
role recording only after the test passes and the feature leaves preview.

Deploys, backups, and FDB configuration record intent and outcome because external
registries and Docker daemons cannot join a database transaction. See
[Kafka transaction limits](https://www.confluent.io/blog/exactly-once-semantics-are-possible-heres-how-apache-kafka-does-it/)
and [KIP-98](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging).
Intent and outcome share `op_id`. The intent stores the image digest, backup
destination, or migration range. Recovery checks that identifier against actual
state when an outcome does not exist.

Reads, including repair preview, require a durable access record before reading.
Refuse a read if its access record cannot commit. TACK-327 followed an unauthorized read.

## Initialize audit recording during bootstrap

`ops provision` and `ops ledger audit-bootstrap` defer audit writes only while
initializing the outboxes. Provision configures FDB, runs migrations, and records
one terminal `ops.provision` event after migration creates the outboxes.
`ops ledger audit-bootstrap` runs only migration and audit role seeding on the
first ledger node. It records intent and outcome after that work. Joining nodes
require the outbox for audited ledger waits before provision runs.
The command refuses a ledger that already contains the migration table, outbox,
or operator login. Both commands require identity. Apply the normal recording
requirements after first boot.

## Implement shared recording

`ops` imports `clispec`, and `clispec` imports `cli`. Keep audit types and outbox
access in `audit`, which does not import those packages. Keep identity sources in
`cli` and command gating in `clispec`.

1. Add `ActorOperator ActorType = "operator"` and map it to actor kind 5.
   Test `actorKindCode(ActorOperator) == 5`.
2. Define `AuditSpec` with `Verb`, `Mutates`, `Atomic`, `BootstrapExempt`, and `Reads`.
   `Atomic` requires the command transaction to commit its event with the change.
   Define `SystemOrgID` as `00000000-0000-0000-0000-0000000005ee` and test its value.
3. Add `OutcomePending = "pending"` and a shared `op_id` to correlate intent and
   outcome. Test serialization and correlation.
4. Create `ops_outbox (event_id UUID PRIMARY KEY, event JSONB NOT NULL,
   created_at TIMESTAMPTZ NOT NULL)` under the normal app role, not audit roles.
   Implement `WriteOutboxTx`, `WriteOutbox`, `ReadOutboxBatch`, and `DeleteOutbox`.
   Run the migration in the integration stack and test writing, reading, and deletion.
5. Define the FDB `(ops_outbox, versionstamp) -> Event JSON` key family.
   Implement `AppendAuditEvent(txn, Event)` with `SET_VERSIONSTAMPED_KEY`,
   `ReadOutboxFrom(mark)`, and `ClearThrough(mark)`. Test commit order and clearing.
6. Add two relay goroutines to audit-consumer and integrate shutdown.
   Reuse `audit.NewKafkaRecorder`. Mount `/etc/foundationdb:/etc/foundationdb:ro`
   and set `FDB_CLUSTER_FILE`. Read FDB events after a high-water-mark key and
   produce each event before clearing the range and advancing the mark in one transaction.
   Poll YB events by `created_at` and delete each row after Kafka acknowledges it.
   Preserve the `(org_id, shard)` partition key and `event_id` during retries.
   Deduplication permits multiple relay instances without leader election.
   Test one event from each outbox with `kfake`, including partition keys and deletion.
7. Define `OperatorPrincipal{ID uuid.UUID; Email, Name, Source string}` and
   `OperatorIdentitySource{Resolve(ctx) (OperatorPrincipal, error)}`.
   Add entity and delta fields, `SetOpsEvent`, `OpsEventFromContext`, and prepared
   events to the audit context. Resolve entity orgs with `reader.Resolve` and
   set them with `audit.SetScopeFields`. Set entity and delta with `audit.SetOpsEvent`.
   Test context round trips.
8. Add persistent `--operator-id`, `--operator-email`, `--operator-name`, and
   `--execute` flags to `Factory`, with `Operator()` and `Execute()` accessors.
   Test flag parsing and registration from `buildRoot`.
9. Implement `GitConfigOperatorSource` by parsing `[user]` from
   `$GIT_CONFIG_GLOBAL` or `$HOME/.gitconfig`, without `os/exec`.
   Derive a stable UUIDv5 with `uuid.NewSHA1(namespace, email)` and use source `"git"`.
   Require email. Implement `FlagOperatorSource` with source `"flag"`.
   `NewOperatorSource(f)` selects flags when an ID exists, otherwise git config.
   Test missing values, invalid IDs, and source preference. Keep OIDC verification
   behind the identity interface. Deploys supply deploy-bot identity and `--execute`.
10. Add `Audit audit.AuditSpec` to `Operation[OperationInput]` and `auditSpec()`
    to `renderable`.
11. Implement `runAudited` in `clispec`. Invoke `runAudited` from `cobraCommand.RunE`.
    Preserve the proposed signature `runAudited(ctx, spec, src, execute bool, outbox
    audit.OutboxWriter, run func(ctx) error) error` and the injectable outbox interface.
    For serve, allow an empty audit verb. Otherwise resolve identity first.
    In dry-run, print identity and action without running work or recording events.
    Prepare the event with verb, operator, and `op_id` in the context.
    For atomic mutations, require the transaction helper and verify its call after `Run`.
    For other mutations, commit intent before `Run` and record outcome afterward.
    Return a nonzero error if outcome recording fails after durable intent.
    For reads, commit the access event before `Run` without writing an outcome row.
    Test recording order, intent failures, missing atomic helpers, identity failures
    in both modes, dry-run behavior, and refusal of reads after access-record failures.
12. Verify global flags in `--help` and run `make build`.
13. Convert `runReindex` and `runBackfillDefaultChildren` to `clispec.Operation`
    under `batchGroup`. Remove the batch map but preserve `Env`, `NewEnv`, and `Close`.
14. Register command audit specs and apply the requirements below.
15. Update the configs repository separately. Add `tack_deploy_operator_id` and
    `tack_deploy_operator_email` to `group_vars/tack_all.yml` as identifiers, not secrets.
    Append `--operator-id {{ tack_deploy_operator_id }} --operator-email {{ tack_deploy_operator_email }} --execute`
    to provision in `deploy-tack.yml`. Do not add broker variables or change the
    environment template for Kafka. Link the configs PR from the Tack PR.
16. Run `make build`, `make fmt`, `make check`, `make test-unit`, and `make test-integration`.
    Verify the QA scenarios below before production.

## Apply command-specific audit requirements

- Require atomic recording for repair apply, reindex, and backfill.default_children.
- Require read recording for repair preview, repair classes, inspect read/find/query,
  verify node, and validate node.
- Require mutation intent and outcome for audit seed-roles, provision, migrate, and seed.
  Preserve bootstrap classification for these commands during initial provisioning.
- Assign repair apply, preview, and classes the verbs `ops.repair.apply`,
  `ops.repair.preview`, and `ops.repair.classes`, respectively.
- Assign inspect read, find, and query the verbs `ops.inspect.read`,
  `ops.inspect.find`, and `ops.inspect.query`, respectively.
- Assign verify node and validate node the verbs `ops.verify.node` and `ops.validate.node`.
- Assign audit seed-roles and provision the verbs `ops.audit.seed_roles` and `ops.provision`.
- Assign reindex and backfill.default_children the verbs `ops.reindex` and `ops.backfill.default_children`.
- Assign migrate and seed the verbs `ops.migrate` and `ops.seed`.
- Remove repair apply's `--actor` and `--yes`. Use global identity and `--execute`,
  but preserve `--confirm <token>`. Resolve the node org, record the repair delta
  with `AppendAuditEvent` inside the transaction, and update preview examples.
- Commit reindex and backfill events with their final batch and include counts in delta.
- Record seed-roles intent and outcome separately. Role DDL can survive rollback
  independently of the event insert on the tested YugabyteDB version.
- Include the idempotent operation identifier in external and SQL operation intents.

## Verify behavior on QA

1. Run `ops audit seed-roles --execute` with git-config identity in a shell and
   `--operator-id` in the container. Check actor kind 5, verb, system org, and chain continuity.
2. Run repair apply on a test node. Check one event with the node org and actual
   delta. Interrupt apply and confirm that the change and event commit together or neither commits.
3. Stop Kafka and run seed-roles. Confirm completion and later event projection
   after Kafka restarts. Stop YugabyteDB and confirm that mutations refuse work
   and inspect refuses reads when recording cannot commit.
4. Run with unresolved identity and confirm failure in dry-run and execute mode.
   Run without `--execute` and confirm that the command prints identity and action
   without changing data or recording events.

Confirm the `Env.Stores` reader with `go doc ./internal/adapters/foundationdb Stores`
and `NoopRecorder` with `go doc ./internal/audit NoopRecorder`. Check context scope
fields, the repair delta accessor, and audit-consumer lifecycle hooks before
implementation. Keep every new operation under the shared audit gate.
