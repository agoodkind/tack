# TACK-328: Audit ops commands

One durable doc for both the design and the implementation, so there is no drift
between a separate spec and plan. Binding rules also live in
`docs/operator-identity-and-audit.md`, which `AGENTS.md` points to.

## What this builds

Every `./server ops` command records who did what in the audit ledger
(`audit.events`). Today nothing in the ops surface records anything. An operator
can edit product data, rotate audit credentials, migrate, provision, or seed prod
with no ledger entry.

This unblocks TACK-327, which locks prod data-plane access to the ops surface.
Locking access is only safe once ops are audited. TACK-328 blocks TACK-327.

## The core mechanism: outbox everywhere

Commands never talk to Kafka. Each command writes its audit event into an
**outbox** inside a database it already uses:

- Commands that change FoundationDB write the event into an FDB outbox **in the
  same transaction as the change**. The change and its record are one
  all-or-nothing step. They can never disagree.
- Every other command writes event rows into a YugabyteDB outbox table. Where the
  work is itself a YugabyteDB transaction, the event rides in that transaction
  too, and is again all-or-nothing.
- A small **relay** tails both outboxes and produces the events to Kafka. The
  audit-consumer projects them into `audit.events` exactly as it does today.

Why this shape:

- The record and the change live or die together wherever a transaction can hold
  both. This is the transactional outbox pattern
  ([Confluent](https://developer.confluent.io/courses/microservices/the-transactional-outbox-pattern/)),
  the standard answer to the dual-write problem
  ([Confluent](https://www.confluent.io/blog/dual-write-problem/)).
- Once an event is in an outbox it is guaranteed to reach the ledger: the relay
  retries until Kafka accepts it, and the consumer's unique
  `(event_id, event_time)` index drops any duplicate, the idempotent-consumer
  upgrade from at-least-once to effectively-once
  ([Morling](https://www.morling.dev/blog/revisiting-the-outbox-pattern/)).
- Commands need no Kafka access. This deletes the earlier plan's loopback Kafka
  listener, the `tack-ops` broker env, and the configs broker variable. The
  availability of auditing equals the availability of the database the command
  was touching anyway.
- The single-writer invariant holds: the relay is the only ops-event producer,
  and the audit-consumer stays the only writer of `audit.events`.

## Binding rules

- One choke-point gates every command. It lives in `internal/clispec`. Do not
  weaken it, bypass it, or let a mutating command skip it.
- No action without a durable record, reads included. For FDB ops the record
  commits with the change. For everything else a record commits before the work
  starts, and if that write fails, nothing runs. Day 0, not later.
- Identity is pluggable via `audit.OperatorIdentitySource`. Never inline an
  identity lookup. Add a source type; select it at one site.
- Identity is never an env var and never a `config.Config` field.
- No identity resolves, the command fails loud, in both dry-run and execute.
- Every audited command is dry-run by default. `--execute` is the action gate.
- Ops never write `audit.events` directly. Events travel outbox, relay, Kafka,
  audit-consumer.

## Decisions

1. Identity comes from a pluggable source. The shipped sources are the local git
   config and `--operator-*` flags. OIDC is another source the same seam takes.
   Actor kind is a new `operator`.
2. Global ops record on a reserved system org. Entity ops record on the target
   node's real org.
3. Every command class gets the strongest record physics allows: same-transaction
   where a transaction can hold both the change and the event; intent-plus-outcome
   where the effect is external. The table below is exact.
4. Scope is all mutating ops plus read events for inspect/verify/validate.
5. Every command declares a static `AuditSpec` and runs through one choke-point.

## What each command class gets

| Class | Record | Can record and change disagree? |
| --- | --- | --- |
| FDB mutations (repair apply, reindex, backfill) | One event, committed in the same FDB transaction as the change | Never |
| seed-roles (YugabyteDB role DDL) | Intent and outcome rows. Verified 2026-06-09 on the prod-pinned image (2024.2.8.0-b85, test stack): role DDL escapes the transaction. `BEGIN; CREATE ROLE; INSERT; ROLLBACK` rolled back the row but the role survived. This is YugabyteDB's documented behavior, not a bug here: DDL in a transaction block runs autonomously and survives rollback, tracked since 2019 in [yugabyte-db #1404](https://github.com/yugabyte/yugabyte-db/issues/1404); full transactional DDL is that issue's phase 3, in progress upstream. Plain PostgreSQL would roll the role back; this is a known YB/PG compatibility gap | Only between intent and outcome |
| migrate | Intent and outcome rows in the YB outbox; goose owns its own per-migration transactions | Only between intent and outcome, and `goose_db_version` makes reconciling mechanical |
| External effects (deploy, backup, fdb configure) | Intent and outcome rows in the YB outbox | Only between intent and outcome; a registry or Docker daemon cannot join any transaction, so this is the physical ceiling ([Confluent EOS](https://www.confluent.io/blog/exactly-once-semantics-are-possible-heres-how-apache-kafka-does-it/), [KIP-98](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging)) |
| Reads (inspect, verify, validate, repair preview) | One access event row, written **before** the read runs, fail-closed: if the row cannot be written, the read does not run | A read changes nothing; the event records the access itself |

Reads are fail-closed on purpose. The incident behind TACK-327 was an
unauthorized read, so an unrecordable read is exactly what must not proceed.

Where intent and outcome exist, both carry one shared `op_id`, and the intent
carries the action's idempotent identifier (the image digest for a deploy, the
backup destination, the migration range), so an intent with no outcome is
resolvable by checking that identifier against reality.

Bootstrap is the one narrow exception: on a fresh environment the outboxes do not
exist until provision creates them (fdb configure, then migrate). Provision
therefore proceeds before the outboxes exist and records one terminal
`ops.provision` event as soon as migrate has created them. Identity is still
required; only the ledger write is deferred. After first boot, nothing is exempt.

## Verified facts

Each was read from the source named, not inferred.

- `audit.events.org_id` has no foreign key (migrations/002_audit.sql:39-57). A
  synthetic system org is legal.
- `audit.events` has `FORCE ROW LEVEL SECURITY`; only `audit_writer` may INSERT
  (002_audit.sql:206-224). The consumer is that writer; ops must not write it.
- `actor_kind` is `SMALLINT` (002_audit.sql:46). The string-to-int map is one
  function, `actorKindCode(ActorType) int16` (yugabyte.go): user=1, service=2,
  system=3, api_token=4. `operator=5` is free.
- `ActorType` is a Go string (recorder.go:62-69), not an int.
- `EventContext.OrgID` is mandatory (recorder.go:81-82). `Reader.Query` rejects
  `org_id == uuid.Nil` (reader.go). So `SystemOrgID` must be a fixed non-nil UUID.
- `KafkaRecorder.Record` produces synchronously with `acks=all` and returns the
  broker error, never swallowing it (kafka_recorder.go:100-145). The relay reuses
  it as-is.
- The Kafka partition key is `(org_id, shard)` and `event_id` is minted at record
  time (kafka_recorder.go:187-198, recorder.go:31-36). The relay preserves both,
  so chains and dedup behave identically to API events.
- `buildRoot` renders serve/migrate/seed/audit/ops through one `clispec` registry
  plus `RenderCobra` (commands.go:44-52). The only separate registry is the
  `ops.go` batch map, bridged by `registerBatchOps` (cli.go:50-72).
- `clispec.Operation.Run` is wrapped in `cobraCommand`'s `RunE` (cobra.go:83-92),
  which holds the `*cli.Factory`.
- `cli.Factory.RegisterGlobalFlags` registers a persistent `--output` flag
  (factory.go:49-52). The same pattern fits the operator flags.
- `repair apply` has a required `--actor` flag and runs `console.Apply` after
  `NewEnv` opens FDB plus postgres (cli_repair.go:142-217).
- `NodeReader.Resolve(ctx, nodeID)` returns `NodeResolve{OrgID, NodeType}`
  (domain/node/reader.go, view.go).
- `provision` runs `postgres.Migrate` and `RunAuditSeedRoles` in-process in the
  host-networked `tack-ops` (provision.go:64-92). `tack-ops` reaches YugabyteDB on
  loopback (`TACK_OPS_DATABASE_URL`, docker-compose.yml:384-424) but cannot reach
  the bridge-only Kafka or FDB, which is why commands must not need Kafka.
- `audit-consumer` runs on the bridge (docker-compose.yml:336-373), so it can
  reach both `kafka:9092` and, once the cluster file is mounted, FDB.
- FoundationDB versionstamped keys give commit-ordered, conflict-free outbox
  entries a tail can range-read from a high-water mark
  ([FoundationDB queues](https://apple.github.io/foundationdb/queues.html),
  [Record Layer paper](https://www.foundationdb.org/files/record-layer-paper.pdf)).
- `readGitCommit` execs `git` (deploy.go:135-152). The git-config identity source
  does not; it parses the gitconfig file to honor the no-shell-out rule.
- `config.Config` uses `caarlos0/env/v11`, no config file (config.go).

Source note: the outbox and exactly-once claims were collected by a deep-research
run (2026-06-09) from the cited primary sources; its adversarial verification was
rate-limited and did not complete, so they rest on source authority and
consistency. A re-run is cheap.

## Import-cycle constraint

`ops` imports `clispec`; `clispec` imports `cli`; `audit` imports none of them. So:

- `AuditSpec`, `OperatorIdentitySource`, `OperatorPrincipal`, and the outbox
  read/write code live in `audit` (plus the FDB key family in the fdb adapter).
- The git and flag identity sources live in `cli`.
- The choke-point lives in `clispec`.

## Design

### The choke-point

`clispec.cobraCommand`'s `RunE` is the one gate. Steps, in order:

1. If `AuditSpec.Verb == ""`, just run (serve only).
2. Resolve the operator. If it fails, abort loud, in both dry-run and execute.
3. If not `--execute`, print the operator and the action, then stop. Nothing
   runs, nothing records.
4. Build the prepared event (verb, operator, op_id) and put it on the context.
5. By class:
   - FDB-atomic op (`AuditSpec.Atomic`): the op's own transaction writes the
     event into the FDB outbox via the required helper. After `Run`, the
     choke-point checks the helper was called and fails loud if not.
   - Other mutation: the choke-point inserts the intent row into the YB outbox
     and aborts if that insert fails. Then `Run`. Then the outcome row,
     best-effort with a loud non-zero exit on failure, since the intent is
     already durable.
   - Read: the choke-point inserts the access event row first and aborts if that
     insert fails. Then `Run`. No outcome row.

### Identity

The actor is a kind plus id, name, email. Ops get a new kind `operator`. Identity
is one interface with many sources, so adding OIDC is one new type at one site.

- `internal/cli/operator_git.go` `GitConfigOperatorSource` reads `user.name` and
  `user.email` from the gitconfig file (`$HOME/.gitconfig`, honoring
  `$GIT_CONFIG_GLOBAL`), parsing the `[user]` section. No `os/exec`. The actor id
  is a stable UUIDv5 from the email.
- `internal/cli/factory.go` adds persistent flags `--operator-id`,
  `--operator-email`, `--operator-name`, and the gate `--execute`.
- `internal/cli/operator.go` `NewOperatorSource(f)` selects: flags when an id is
  given, else git config. Both implement `audit.OperatorIdentitySource`.
- The deploy pipeline passes a deploy-bot operator and `--execute` for
  non-interactive provision, so bootstrap also has identity.
- OIDC is a verified source the same seam takes. Its contract is in
  `docs/operator-identity-and-audit.md`.

### The outboxes

- FDB: a new key family in `internal/adapters/foundationdb/keys.go`,
  `(ops_outbox, versionstamp) -> Event JSON`, written with
  `SET_VERSIONSTAMPED_KEY` so entries are commit-ordered and conflict-free. A
  helper `AppendAuditEvent(txn, Event)` is the only writer.
- YugabyteDB: a new migration creates `ops_outbox (event_id UUID PRIMARY KEY,
  event JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL)`, owned by the normal
  app role, not the audit roles. `audit.WriteOutboxTx(ctx, tx, Event)` and
  `audit.WriteOutbox(ctx, pool, Event)` are the writers.

### The relay

A goroutine pair inside the existing audit-consumer binary, since it already runs
continuously on the bridge with Kafka and YB access; compose adds the FDB cluster
file mount.

- FDB side: range-read from a high-water-mark key, produce each event with the
  existing `KafkaRecorder`, then clear the read range and advance the mark in one
  FDB transaction.
- YB side: poll `ops_outbox` ordered by `created_at`, produce, delete the row
  after the broker ack.
- Duplicates are safe in both directions because the consumer's
  `(event_id, event_time)` index drops them, so the relay needs no leader
  election; running one instance is an efficiency choice, not a correctness one.

### Where events land

Global ops use the fixed `SystemOrgID`. Entity ops resolve the node's real org
via `reader.Resolve` and stamp it with `audit.SetScopeFields`, plus entity and
delta with `audit.SetOpsEvent`. The choke-point and the FDB helper read them
back. Events carry `event_id` minted at write time, so sharding and chain
placement behave exactly like API events.

## Implementation

Bite-sized, test-first, frequent commits. Branch `tack-328-audit-ops-commands`.
Build `make build`; unit `make test-unit`; integration `make test-integration`.

### Task 1: operator actor kind

- Add `ActorOperator ActorType = "operator"` to `recorder.go`.
- Add `case ActorOperator: return 5` to `actorKindCode` in `yugabyte.go`.
- Test `actorKindCode(ActorOperator) == 5`. Run, fail, implement, pass, commit.

### Task 2: AuditSpec and SystemOrgID

- Create `internal/audit/ops.go`:
  `var SystemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")` and
  `type AuditSpec struct { Verb string; Mutates, Atomic, BootstrapExempt, Reads bool }`.
  `Atomic` marks FDB ops whose event commits inside their own transaction.
- Test `SystemOrgID != uuid.Nil` and the literal. Commit.

### Task 3: event shape for intent and outcome

- Add `OutcomePending Outcome = "pending"` to `recorder.go`.
- Carry a shared `op_id` (in `EventContext.Reason` or `Event.Extra`; pick one and
  test it) so intent and outcome correlate.
- Test the pending outcome serializes and the op_id round-trips. Commit.

### Task 4: YB outbox

- New migration `ops_outbox` as in the design section. `make build` then run the
  migration in the integration stack.
- `internal/audit/outbox_yb.go`: `WriteOutboxTx`, `WriteOutbox`, plus
  `ReadOutboxBatch` and `DeleteOutbox` for the relay.
- Integration test: write a row, read it back, delete it. Commit.

### Task 5: FDB outbox

- Add the `ops_outbox` key family to `internal/adapters/foundationdb/keys.go`.
- Adapter helper `AppendAuditEvent(txn, Event)` using `SET_VERSIONSTAMPED_KEY`,
  plus `ReadOutboxFrom(mark)` and `ClearThrough(mark)` for the relay.
- Integration test: append two events in two transactions, read them back in
  commit order, clear, read empty. Commit.

### Task 6: relay in the audit-consumer

- Add the two relay goroutines to `cmd/audit-consumer`, reusing
  `audit.NewKafkaRecorder`. Wire shutdown into the existing lifecycle.
- docker-compose: mount `/etc/foundationdb:/etc/foundationdb:ro` into
  `audit-consumer` and set `FDB_CLUSTER_FILE`.
- Integration test with `kfake` (franz-go, already in go.mod): write one event to
  each outbox, run the relay once, assert both arrive on the topic with the right
  key and that the outboxes are empty. Commit.

### Task 7: ops-event context and identity interface

- Add to `internal/audit/ops.go`: `OperatorPrincipal{ID uuid.UUID; Email, Name,
  Source string}` and `OperatorIdentitySource{ Resolve(ctx) (OperatorPrincipal, error) }`.
- Add to `context.go`'s scope-builder an entity and delta field plus
  `SetOpsEvent` / `OpsEventFromContext`, and the prepared-event carrier the
  choke-point and FDB helper share.
- Test round trips. Commit.

### Task 8: operator flags and --execute on Factory

- Add `operatorID/Email/Name *string` and `execute *bool` to `Factory`, with
  `Operator()` and `Execute()` accessors; register all four as persistent flags
  in `RegisterGlobalFlags`.
- Test: parse and read back. Commit.

### Task 9: git config and selector sources

- `internal/cli/operator_git.go`: parse `[user]` from `$GIT_CONFIG_GLOBAL` or
  `$HOME/.gitconfig`; id is `uuid.NewSHA1(namespace, email)`; `Source` `"git"`;
  error when no email.
- `internal/cli/operator.go`: `FlagOperatorSource` (`Source` `"flag"`) and
  `NewOperatorSource(f)` preferring flags. Both return `audit.OperatorPrincipal`.
- Test: present, absent, bad id, selector preference. Commit.

### Task 10: Audit field on clispec.Operation

- Add `Audit audit.AuditSpec` to `Operation[I]` and `auditSpec()` to
  `renderable`. `make build`. Commit.

### Task 11: the choke-point

- `internal/clispec/audit.go`: `runAudited(ctx, spec, src, execute bool, outbox
  audit.OutboxWriter, run func(ctx) error) error` implementing the steps in the
  design section. `OutboxWriter` is a small interface so tests inject a fake.
- Wire into `cobra.go`'s `RunE`.
- Tests: mutation writes intent then outcome; mutation aborts and never runs when
  the intent write fails; atomic op fails loud when the helper was not called;
  aborts on unresolved operator in both modes; dry-run runs nothing and records
  nothing; read aborts and never runs when its access-event write fails. Commit.

### Task 12: register flags at root

- `f.RegisterGlobalFlags(root)` already runs in `buildRoot`. Smoke check
  `--operator-id` and `--execute` in `--help`. `make build`.

### Task 13: fold the batch map into clispec

- Convert `runReindex` and `runBackfillDefaultChildren` into `clispec.Operation`
  leaf ops under `batchGroup`; delete the `ops.go` map machinery; keep `Env`,
  `NewEnv`, `Close`. `make build`, `make test-unit`. Commit.

### Task 14: audit specs on every command

| Command | Verb | Mutates | Atomic | BootstrapExempt | Reads |
| --- | --- | --- | --- | --- | --- |
| repair apply | ops.repair.apply | true | true | false | false |
| repair preview | ops.repair.preview | false | false | false | true |
| repair classes | ops.repair.classes | false | false | false | true |
| audit seed-roles | ops.audit.seed_roles | true | false | true | false |
| provision | ops.provision | true | false | true | false |
| inspect read/find/query | ops.inspect.read/.find/.query | false | false | false | true |
| verify node | ops.verify.node | false | false | false | true |
| validate node | ops.validate.node | false | false | false | true |
| reindex | ops.reindex | true | true | false | false |
| backfill.default_children | ops.backfill.default_children | true | true | false | false |
| migrate | ops.migrate | true | false | true | false |
| seed | ops.seed | true | false | true | false |

- repair apply: drop `--actor` and `--yes` (identity from global flags, gate is
  `--execute`; keep `--confirm <token>`). Resolve the node org and stamp it; call
  `AppendAuditEvent` inside the apply transaction with the repair plan as delta;
  update the preview's printed example.
- reindex and backfill write their event in the same FDB transaction as their
  final batch, with counts in the delta.
- seed-roles: intent and outcome rows. Do not try to bind the role DDL and the
  event row in one transaction: verified on 2024.2.8.0-b85 that the DDL escapes
  the transaction (a ROLLBACK keeps the role), so a one-transaction bind would
  look correct and silently not be.
- External and SQL ops put the idempotent identifier in the intent (image digest,
  backup destination, migration range).
- `make build`, `make fmt`, `make test-unit`. Commit.

### Task 15: configs repo (separate PR, per the seam)

- `group_vars/tack_all.yml`: add `tack_deploy_operator_id`,
  `tack_deploy_operator_email` (identifiers, not secrets).
- `deploy-tack.yml` provision task: append
  `--operator-id {{ tack_deploy_operator_id }} --operator-email {{ tack_deploy_operator_email }} --execute`.
- No broker variable and no env template change: commands do not use Kafka.
- Link the configs PR from the tack PR.

### Task 16: gates

- `make build`, `make check`, `make test-unit`, `make test-integration` all clean.
- QA manual, never prod and never first:
  - `ops audit seed-roles --execute` with git-config identity in a shell, and
    `--operator-id` in the container; confirm the ledger rows (`actor_kind=5`,
    right verb, system org) and chain continuity via the audit MCP tools.
  - `repair apply` on a test node; confirm one event on the node's real org with
    the delta, and that killing the command mid-apply leaves either both the
    change and the event or neither.
  - Stop Kafka; run seed-roles; confirm it still completes and the event arrives
    after Kafka returns (outbox drains). Stop YugabyteDB; confirm a mutation
    aborts before doing anything and an inspect aborts before reading.
  - Run any command with no resolvable identity; confirm a loud failure in both
    dry-run and execute. Run without `--execute`; confirm dry-run prints identity
    and action and changes nothing.

## Handles to confirm at execution time

- The `Env.Stores` reader field name (`go doc ./internal/adapters/foundationdb Stores`).
- The scope-builder field names in `context.go`.
- The repair apply result's change-set accessor for the delta.
- The `NoopRecorder` file (`go doc ./internal/audit NoopRecorder`).
- audit-consumer lifecycle hooks for the relay goroutines.

## Tickets

TACK-328 blocks TACK-327. TACK-327 lockdown proceeds only after this lands and is
verified on QA.
