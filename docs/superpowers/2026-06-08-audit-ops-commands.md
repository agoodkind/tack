# TACK-328: Audit ops commands

One durable doc for both the design and the implementation, so there is no drift
between a separate spec and plan. Binding rules also live in
`docs/operator-identity-and-audit.md`, which `AGENTS.md` points to.

## What this builds

Every `./server ops` command records an audit event through the same recorder the
API uses. Today the recorder fires only at the API/MCP boundary. Ops commands emit
`slog` only. So an operator can edit product data, rotate audit credentials,
migrate, provision, or seed prod with no entry in `audit.events`.

This unblocks TACK-327, which locks prod data-plane access to the ops surface.
Locking access is only safe once ops are audited. TACK-328 blocks TACK-327.

## Binding rules

- One choke-point records every command. It lives in `internal/clispec`. Do not
  weaken it, bypass it, or let a mutating command skip it.
- Identity is pluggable via `audit.OperatorIdentitySource`. Never inline an
  identity lookup. Add a source type; select it at one site.
- Identity is never an env var and never a `config.Config` field.
- No identity resolves, the command fails loud, in both dry-run and execute.
- Every audited command is dry-run by default. `--execute` is the action gate.
- The audit-consumer stays the only writer of `audit.events`. Ops produce to
  Kafka; they never write the ledger SQL directly.

## Decisions

1. Identity now is the local git config, with `--operator-*` flags as the override
   for containers. Later it is OIDC/SSO. Actor kind is a new `operator`.
2. Global ops record on a reserved system org. Entity ops record on the target
   node's real org.
3. Steady mutations are fail-closed. Bootstrap is exempt from ledger reachability
   only, never from identity. Reads never block on a down ledger.
4. Scope is all mutating ops plus read events for inspect/verify/validate.
5. Every command declares a static `AuditSpec` and runs through one choke-point.

## Verified facts

Each was read from the source named, not inferred.

- `audit.events.org_id` has no foreign key (migrations/002_audit.sql:39-57). A
  synthetic system org is legal.
- `audit.events` has `FORCE ROW LEVEL SECURITY`; only `audit_writer` may INSERT,
  policy `WITH CHECK (true)` (002_audit.sql:206-224). The consumer is that writer,
  so a system-org row inserts fine, and ops must not write SQL directly.
- `actor_kind` is `SMALLINT` (002_audit.sql:46). The string-to-int map is one
  function, `actorKindCode(ActorType) int16` (yugabyte.go): user=1, service=2,
  system=3, api_token=4. `operator=5` is free.
- `ActorType` is a Go string, values `user/service/system/api_token`
  (recorder.go:62-69), not an int.
- `EventContext.OrgID` is mandatory (recorder.go:81-82). `Reader.Query` rejects
  `org_id == uuid.Nil` (reader.go). So `SystemOrgID` must be a fixed non-nil UUID.
- `buildAuditRecorder` selects Kafka when `AUDIT_KAFKA_BROKERS` is set, else YB
  when `AUDIT_WRITER_DSN` is set, else `NoopRecorder`, whose `Record` returns nil
  (audit_runtime.go:78-110).
- `KafkaRecorder` holds a `*kgo.Client` (franz-go) and connects lazily, so a down
  broker is not seen until first produce (kafka_recorder.go:23-27, 49-51).
- `(*kgo.Client).Ping(ctx) error` exists in franz-go v1.21.1 (go.mod:22) at
  `pkg/kgo/client.go:626`; it sends a Metadata request and returns nil on first
  success.
- `buildRoot` renders serve/migrate/seed/audit/ops through one `clispec` registry
  plus `RenderCobra` (commands.go:44-52). The only separate registry is the
  `ops.go` batch map, bridged in by `registerBatchOps` (cli.go:50-72).
- `clispec.Operation.Run` is wrapped in `cobraCommand`'s `RunE` (cobra.go:83-92),
  which holds the `*cli.Factory`.
- `cli.Factory.RegisterGlobalFlags` registers a persistent `--output` flag and
  stores a `*string` (factory.go:49-52). The same pattern fits the operator flags.
- `repair apply` has a required `--actor` flag and runs `console.Apply` after
  `NewEnv` opens FDB+postgres (cli_repair.go:142-217).
- `NodeReader.Resolve(ctx, nodeID) (*NodeResolve, error)` returns
  `NodeResolve{OrgID, NodeType}` (domain/node/reader.go, view.go).
- `provision` runs `postgres.Migrate` and `RunAuditSeedRoles` in-process in the
  host-networked `tack-ops` and reaches bridge containers only via the Docker
  socket (provision.go:64-92). `seed` uses `audit.WithSuppressed` (seed.go:72).
- `readGitCommit` execs `git` (deploy.go:135-152). The git-config source does not;
  it parses the gitconfig file to honor the no-shell-out rule.
- `config.Config` uses `caarlos0/env/v11`, no config file (config.go).

## Import-cycle constraint

`ops` imports `clispec`; `clispec` imports `cli`; `audit` imports none of them. So:

- `AuditSpec`, `OperatorIdentitySource`, and `OperatorPrincipal` live in `audit`.
- The git and flag sources live in `cli`.
- The choke-point lives in `clispec`.

## Design

### The choke-point

`clispec.cobraCommand`'s `RunE` is the one place that records. Steps, in order:

1. If `AuditSpec.Verb == ""`, just run (serve only).
2. Resolve the operator. If it fails, abort loud, in both dry-run and execute.
3. If not `--execute`, print the operator and the action, then stop. Nothing runs,
   nothing records.
4. Build the recorder. For a mutation, require the Kafka recorder; reject Noop or
   YB, so a missing broker cannot look like a successful record.
5. Preflight with `Ping`. A steady mutation aborts if it fails. Bootstrap and reads
   proceed.
6. Run the command.
7. Record one event: actor kind operator, the verb, the org (real or system), the
   entity and delta the command stamped, and the outcome.

### Identity

The actor is a kind plus id, name, email. Ops get a new kind `operator`.

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
- OIDC/SSO is the planned verified source. Its full contract is in
  `docs/operator-identity-and-audit.md`.

### Recorder, system org, preflight

- Extract `buildAuditRecorder` into `audit.NewRecorderFromConfig(ctx, cfg)`;
  `cmd/server` calls it too.
- Add a fixed non-nil `SystemOrgID` for global-op chains.
- Add `ReachabilityChecker{ Ping(ctx) }`. Kafka delegates to `k.client.Ping`; YB
  uses `r.pool.Ping`; Noop returns nil.

### Where events land

Global ops use `SystemOrgID`. Entity ops resolve the node's real org via
`reader.Resolve` and stamp it onto the audit context with `SetScopeFields`, plus
the entity and delta with `SetOpsEvent`. The choke-point reads both back after the
command runs.

### Failure handling

| Class | Operator | Ledger down | Record fails |
| --- | --- | --- | --- |
| Steady mutation (repair apply, reindex, backfill, seed-roles live, backup, deploy) | Required, abort if unresolved | Abort before mutating | Exit non-zero, loud Error |
| Bootstrap (provision, first-boot migrate/seed/seed-roles) | Required, abort if unresolved | Proceed | Best-effort; provision records one terminal event |
| Read (inspect, verify, validate, repair preview) | Required, abort if unresolved | Proceed | Best-effort |

Without `--execute` every row is a dry-run: identity still resolves and still
fails loud if absent, nothing runs, nothing records.

### The dual-write limit, and the upgrade path

The guarantee is preflight, then mutate, then record. No transaction spans
FoundationDB/Yugabyte and Kafka, and cross-database transactions are forbidden
here, so a broker death in the small window after a passing ping leaves a mutation
recorded only in `slog`. This is the dual-write problem
([Confluent](https://www.confluent.io/blog/dual-write-problem/)).

The durable fix is a transactional outbox
([Confluent course](https://developer.confluent.io/courses/microservices/the-transactional-outbox-pattern/)).
For FDB-mutating ops it is cheap, because FoundationDB versionstamps give an
ordered outbox with no polling worker
([FoundationDB queues](https://apple.github.io/foundationdb/queues.html),
[versionstamps](https://fragno.dev/blog/versionstamps)):

- FDB ops (repair apply, reindex, backfill): write the event into an FDB outbox
  subspace via `SET_VERSIONSTAMPED_KEY` in the same transaction as the mutation. A
  relay range-reads from a high-water mark and produces to Kafka. The existing
  `(event_id, event_time)` unique index dedups, so at-least-once becomes
  effectively-once. The consumer stays the single ledger writer.
- SQL DDL ops (seed-roles): no clean transactional bind to the ledger; record
  intent durably, then the outcome.
- External-effect ops (deploy, backup, fdb configure): the side effect is Docker
  or a registry, not a transactional store, so atomicity is impossible; intent
  plus outcome is the ceiling.

The research sharpened one point. The consumer-side projection is **already
effectively-once**: one Kafka partition is consumed by one consumer, and the unique
`(event_id, event_time)` index is the idempotent-consumer-with-unique-key dedup the
literature describes
([Morling](https://www.morling.dev/blog/revisiting-the-outbox-pattern/),
[lydtech](https://www.lydtechconsulting.com/blog/kafka-idempotent-consumer-transactional-outbox)).
So the only unclosed gap is producer-side (mutation done, produce failed); an outbox
closes that gap, not the projection. The versionstamp tail is also conflict-free:
CloudKit chose a version index over an update-counter precisely because a counter
created conflicts between otherwise non-conflicting transactions
([Record Layer paper](https://www.foundationdb.org/files/record-layer-paper.pdf)), so
a versionstamp outbox does not serialize concurrent ops the way a counter would. And
the external-effect ceiling is firm: Kafka exactly-once does not extend to external
side effects and cannot roll back a remote system
([Confluent EOS](https://www.confluent.io/blog/exactly-once-semantics-are-possible-heres-how-apache-kafka-does-it/)),
and Kafka transactions alone do not make a store mutation and an emit atomic
([KIP-98](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging)).

Decision for v1: keep preflight plus synchronous `acks=all`, which shrinks the
window to a rare broker-death-mid-call. The FDB-versionstamp outbox is the named
upgrade for FDB ops when the window must be fully closed. It is not built in v1.

Note on sources: a deep-research run (2026-06-09) collected these claims from the
primary and authoritative sources cited above, but its adversarial verification
stage was rate-limited and did not complete, so these are vouched on source
authority and consistency, not an independent three-vote pass. Re-running the
verification is a cheap follow-up.

## Implementation

Bite-sized, test-first, frequent commits. Branch `tack-328-audit-ops-commands`.
Build `make build`; unit `make test-unit`; integration `make test-integration`.

### Task 1: operator actor kind

- Add `ActorOperator ActorType = "operator"` to `recorder.go`.
- Add `case ActorOperator: return 5` to `actorKindCode` in `yugabyte.go`.
- Test `actorKindCode(ActorOperator) == 5`. Run, fail, implement, pass, commit.

### Task 2: AuditSpec and SystemOrgID

- Create `internal/audit/ops.go`: `var SystemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")` and `type AuditSpec struct { Verb string; Mutates, BootstrapExempt, Reads bool }`.
- Test `SystemOrgID != uuid.Nil` and the literal. Commit.

### Task 3: ReachabilityChecker

- Add to `ops.go`: `type ReachabilityChecker interface { Ping(ctx context.Context) error }`.
- `KafkaRecorder.Ping` returns `k.client.Ping(ctx)`. `YBRecorder.Ping` returns `r.pool.Ping(ctx)`. `NoopRecorder.Ping` returns nil.
- Test `NoopRecorder` satisfies the interface and returns nil. Commit.

### Task 4: NewRecorderFromConfig

- Create `internal/audit/from_config.go` `NewRecorderFromConfig(ctx, cfg) Recorder` by moving the selection body out of `cmd/server/audit_runtime.go`. `audit` importing `config` is acyclic.
- Rewire `buildAuditRecorder` to call it.
- Test empty config returns `NoopRecorder`; brokers set returns `*KafkaRecorder`. Commit.

### Task 5: ops-event context and identity interface

- Add to `ops.go`: `OperatorPrincipal{ID uuid.UUID; Email, Name, Source string}` and `OperatorIdentitySource{ Resolve(ctx) (OperatorPrincipal, error) }`.
- Add to `context.go`'s scope-builder holder an entity and delta field, plus `SetOpsEvent(ctx, Entity, *Delta)` and `OpsEventFromContext(ctx) (Entity, *Delta)`.
- Test a round trip. Commit.

### Task 6: operator flags and --execute on Factory

- Add `operatorID/Email/Name *string` and `execute *bool` to `Factory`, an `Operator() (OperatorFlags, bool)` accessor, and an `Execute() bool` accessor.
- In `RegisterGlobalFlags`, register persistent `--operator-id`, `--operator-email`, `--operator-name`, and `--execute` (bool, default false).
- Test: parse `--operator-id ... --operator-email ... --execute` and read them back. Commit.

### Task 7: git config and selector sources

- Create `internal/cli/operator_git.go` `GitConfigOperatorSource`: parse the `[user]` section of `$GIT_CONFIG_GLOBAL` or `$HOME/.gitconfig`; derive id `uuid.NewSHA1(namespace, []byte(email))`; `Source` is `"git"`; error if no email.
- Create `internal/cli/operator.go`: `FlagOperatorSource` (id from `--operator-*`, `Source` `"flag"`) and `NewOperatorSource(f)` that returns the flag source when an id is set, else the git source. Both return `audit.OperatorPrincipal`.
- Test: git parse present and absent; flag override valid and bad; selector prefers flags. Commit.

### Task 8: Audit field on clispec.Operation

- Add `Audit audit.AuditSpec` to `Operation[I]` and `auditSpec()` to the `renderable` interface. `make build`. Commit.

### Task 9: the choke-point

- Create `internal/clispec/audit.go` with `runAudited(ctx, spec, src, execute bool, newRecorder func() audit.Recorder, run func(ctx) error) error` implementing the seven steps above. Generic over the recorder constructor so a fake injects in tests.
- In `cobra.go`, replace the final `return op.Run(...)` with a `runAudited(...)` call passing `op.Audit`, `cli.NewOperatorSource(f)`, `f.Execute()`, a recorder built from `audit.NewRecorderFromConfig(ctx, f.Cfg)`, and the `op.Run` closure.
- Tests with a capturing fake recorder and a fake source: records on success; aborts a mutation on unresolved operator in both modes; dry-run runs nothing; mutation rejects Noop/YB; mutation aborts on Ping failure. Commit.

### Task 10: register flags at root

- `f.RegisterGlobalFlags(root)` already runs in `buildRoot`, so the new flags appear. Smoke check `--operator-id` and `--execute` show in `--help`. `make build`.

### Task 11: fold the batch map into clispec

- Convert `runReindex` and `runBackfillDefaultChildren` into `clispec.Operation` leaf ops under `batchGroup`, opening `NewEnv` inside `Run`, each with its `Audit` spec.
- Remove `registerBatchOps`; register the two directly. Delete `registry`, `Register`, `Get`, `List`, `Run`, `Operation` from `ops.go`; keep `Env`, `NewEnv`, `Close`.
- `make build`, `make test-unit`. Commit.

### Task 12: declare audit specs on every command

Add `Audit: audit.AuditSpec{...}` to each command literal:

| Command | Verb | Mutates | BootstrapExempt | Reads |
| --- | --- | --- | --- | --- |
| repair apply | ops.repair.apply | true | false | false |
| repair preview | ops.repair.preview | false | false | true |
| repair classes | ops.repair.classes | false | false | true |
| audit seed-roles | ops.audit.seed_roles | true | true | false |
| provision | ops.provision | true | true | false |
| inspect read/find/query | ops.inspect.read/.find/.query | false | false | true |
| verify node | ops.verify.node | false | false | true |
| validate node | ops.validate.node | false | false | true |
| reindex | ops.reindex | true | false | false |
| backfill.default_children | ops.backfill.default_children | true | false | false |
| migrate | ops.migrate | true | true | false |
| seed | ops.seed | true | true | false |

- For repair apply: remove the `--actor` flag and `ActorID`; the operator comes
  from the global flags. After resolving the node, stamp the org:
  `audit.SetScopeFields(ctx, audit.Scope{OrgID: resolve.OrgID})` from
  `reader.Resolve(nodeID)`. After apply, stamp entity and delta with
  `audit.SetOpsEvent`. Drop the `--yes` flag in favor of `--execute`; keep
  `--confirm <token>`. Update the preview's printed example to use `--operator-id`
  and `--execute`.
- For inspect/verify/validate on a `--node`, stamp the resolved org too. Other
  reads default to `SystemOrgID`.
- `make build`, `make fmt`, `make test-unit`. Commit.

### Task 13: integration test via kfake

- Add `internal/test/integration/audit_ops_test.go` (build tag `integration`).
  Stand up a `kfake` broker (franz-go `pkg/kfake`, already in go.mod), point a
  `KafkaRecorder` at it, drive `runAudited` with a mutation spec, a resolved
  operator, and execute true, then consume the record and assert the decoded
  `Event` has the verb, `Actor.Type == ActorOperator`, and `Context.OrgID ==
  SystemOrgID`. The consumer projection (actor_kind=5 row) is the consumer's own
  test plus the QA step, since the test stack has no consumer.
- `make test-integration`. Commit.

### Task 14: docker-compose Kafka HOST listener

- `kafka` env: append `,HOST://[::]:9094` to `KAFKA_LISTENERS`, `,HOST://[::1]:9094`
  to `KAFKA_ADVERTISED_LISTENERS`, `,HOST:PLAINTEXT` to the security map.
- `kafka` gains `ports: ["[::1]:9094:9094"]`.
- `tack-ops` env gains `AUDIT_KAFKA_BROKERS: ${TACK_OPS_AUDIT_KAFKA_BROKERS:-[::1]:9094}`,
  `AUDIT_KAFKA_TOPIC`, `AUDIT_KAFKA_CLIENT_ID: tack-ops-audit-producer`,
  `AUDIT_KAFKA_PRODUCE_TIMEOUT`. No operator env.
- Validate `docker compose --profile audit --profile ops config >/dev/null`. Commit.

### Task 15: configs repo (separate PR, per the seam)

- `tack/tack.env.j2`: add `TACK_OPS_AUDIT_KAFKA_BROKERS={{ tack_ops_audit_kafka_brokers }}`.
- `group_vars/tack_all.yml`: add `tack_ops_audit_kafka_brokers: "[::1]:9094"`,
  `tack_deploy_operator_id`, `tack_deploy_operator_email`.
- `deploy-tack.yml` provision task: add `--operator-id {{ tack_deploy_operator_id }} --operator-email {{ tack_deploy_operator_email }} --execute`.
- Not part of the tack-repo commit. Link the configs PR from the tack PR.

### Task 16: gates

- `make build`, `make check`, `make test-unit`, `make test-integration` all clean.
- QA manual, never prod and never first: with the audit profile up and the HOST
  listener live, run `ops audit seed-roles --operator-id <uuid> --operator-email you@x --execute`
  and `docker compose exec app /server ops repair apply --operator-id <uuid> --node <uuid> --confirm <token> --execute`.
  Confirm `actor_kind=5`, the verbs, system vs real org, and chain continuity via
  the audit MCP tools. Run a mutation with the broker down and confirm it aborts.
  Run an inspect with the broker down and confirm it proceeds. Run any command
  without `--operator-id` and confirm it fails loud in both dry-run and execute.

## Handles to confirm at execution time

- The `Env.Stores` reader field name (`go doc ./internal/adapters/foundationdb Stores`).
- The scope-builder holder field names in `context.go`.
- The repair apply result's change-set accessor for the delta; omit Delta if none.
- The telemetry attr helper names; fall back to `slog.String`.
- The `NoopRecorder` file (`go doc ./internal/audit NoopRecorder`).

## Appendix: exact docker-compose lines

`kafka` env:

```yaml
KAFKA_LISTENERS: PLAINTEXT://[::]:9092,CONTROLLER://[::]:9093,HOST://[::]:9094
KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:9092,HOST://[::1]:9094
KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,HOST:PLAINTEXT
```

`kafka` ports:

```yaml
ports:
  - "[::1]:9094:9094"
```

`tack-ops` env:

```yaml
AUDIT_KAFKA_BROKERS: ${TACK_OPS_AUDIT_KAFKA_BROKERS:-[::1]:9094}
AUDIT_KAFKA_TOPIC: ${AUDIT_KAFKA_TOPIC:-audit.events.v1}
AUDIT_KAFKA_CLIENT_ID: ${AUDIT_KAFKA_CLIENT_ID:-tack-ops-audit-producer}
AUDIT_KAFKA_PRODUCE_TIMEOUT: ${AUDIT_KAFKA_PRODUCE_TIMEOUT:-10s}
```

## Residual unknown (QA-gated)

Kafka accepting the extra HOST listener in KRaft combined mode, and the
`[::1]:9094` publish behaving like the verified `[::1]:5433` yugabyte precedent,
must be confirmed on a QA bring-up before prod.

## Tickets

TACK-328 blocks TACK-327. TACK-327 lockdown proceeds only after this lands and is
verified on QA.
