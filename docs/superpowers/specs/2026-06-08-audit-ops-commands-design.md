# TACK-328: Audit ops commands (design)

## What this does

Make every mutating ops command, plus every read op, write an audit event.
Today the Recorder fires only at the API/MCP boundary
(`internal/adapters/mcp/tools/helpers.go` `recordToolAudit`, plus the auth
middleware). Ops commands emit `slog` only.

So an operator can edit product data (`ops repair apply`), rotate audit
credentials (`ops audit seed-roles`), migrate, provision, or seed prod with no
entry in `audit.events`. That is the SOC2/GDPR/HIPAA/ISO gap the ticket names.

This unblocks TACK-327 (lock prod data-plane access to the ops surface). Locking
access is only safe once ops are audited, otherwise it just relocates the blind
spot. TACK-328 already carries a `blocks` relationship to TACK-327.

## Settled decisions

1. Operator identity comes from a pluggable source, not hard-coded env. Actor
   kind is a new `operator`.
2. Global ops record on a reserved system-org chain. Entity ops (`repair apply`)
   record on the target node's real org.
3. Steady-state mutations are fail-closed. Bootstrap ops are exempt. Reads
   require an operator but never block on a down ledger.
4. Scope: all mutating ops plus read events for inspect/verify/validate.
5. Wiring: every op declares a required audit spec and runs through one dispatch
   choke-point.

## Verified facts this design rests on

Each was read directly from the source named, not inferred.

- `audit.events.org_id` has **no foreign key** (migrations/002_audit.sql:39-57),
  so a synthetic system org is legal.
- `audit.events` has `FORCE ROW LEVEL SECURITY`; only `audit_writer` may INSERT,
  with policy `WITH CHECK (true)` (002_audit.sql:206-224). The consumer is that
  writer, so a system-org row inserts fine and ops must never write SQL directly.
- `actor_kind` is `SMALLINT` (002_audit.sql:46). The string-to-int mapping is the
  single function `actorKindCode(ActorType) int16` (internal/audit/yugabyte.go),
  with user=1, service=2, system=3, api_token=4, default=0. `operator=5` is free.
- `ActorType` is a Go **string** type with values `user/service/system/api_token`
  (internal/audit/recorder.go:62-69), not an int.
- `EventContext.OrgID` is mandatory (recorder.go:81-82), and `Reader.Query`
  rejects `org_id == uuid.Nil` (internal/audit/reader.go), so `SystemOrgID` must
  be a fixed **non-nil** UUID to stay queryable.
- `buildAuditRecorder` (cmd/server/audit_runtime.go:78-110) selects Kafka when
  `AUDIT_KAFKA_BROKERS` is set, else YB when `AUDIT_WRITER_DSN` is set, else
  `NoopRecorder`; `NoopRecorder.Record` returns nil.
- `KafkaRecorder` holds a `*kgo.Client` (franz-go) and connects lazily, so a down
  broker is not detected until first produce (kafka_recorder.go:23-27, 49-51).
- `buildRoot` already renders serve/migrate/seed/audit/ops through one `clispec`
  registry + `RenderCobra` (cmd/server/commands.go:44-52). The only separate
  registry is the `ops.go` batch map, bridged in by `registerBatchOps`
  (internal/ops/cli.go:50-72).
- `repair apply` has a required `--actor` flag and runs `console.Apply` after
  `NewEnv` opens FDB+postgres (internal/ops/cli_repair.go:142-217).
- `NodeReader.Resolve(ctx, nodeID) (*NodeResolve, error)` returns
  `NodeResolve{OrgID, NodeType}` (internal/domain/node/reader.go, view.go).
- `provision` runs `postgres.Migrate` and `RunAuditSeedRoles` in-process in the
  host-networked `tack-ops` and reaches bridge containers only via the Docker
  socket (internal/ops/provision.go:64-92). `seed` uses `audit.WithSuppressed`
  (cmd/server/seed.go:72).
- `config.Config` uses `caarlos0/env/v11`, no config file, and has no operator
  fields today (internal/config/config.go).

## The broker reachability problem and its fix

Verified from `docker-compose.yml`: `tack-ops` is `network_mode: host` (line 383)
with no `AUDIT_KAFKA_BROKERS` in its env (lines 384-424); the `kafka` service
advertises `PLAINTEXT://kafka:9092` on the v6 bridge, is profile-gated `[audit]`,
and publishes no host port (lines 278-312). So `tack-ops` cannot reach Kafka. The
`app` service, on the bridge, has `AUDIT_KAFKA_BROKERS` (line 46) and can.

Where each op runs, verified:

- `repair apply`, `reindex`, `backfill` open FDB, which has no host port, so they
  already run in the **app container** (bridge). They reach `kafka:9092`.
- `migrate`, `seed-roles`, `provision`, `backup`, `deploy` run in **tack-ops**
  (host) for the Docker socket and loopback DB access. They cannot reach Kafka.

Fix: add a second, loopback-published Kafka listener on the LXC and point
`tack-ops` at it, mirroring how `tack-ops` already reaches `yugabyte` over
loopback. The verified precedent is `tack_ops_database_url` in the configs repo
`group_vars/tack_all.yml`, which is `...@[::1]:5433/...`, so the address family is
IPv6 loopback `[::1]`, not `127.0.0.1`. The exact diffs are in the appendix below;
the shape is:

- `kafka` service: add a `HOST` listener
  (`KAFKA_LISTENERS` gains `HOST://[::]:9094`, `KAFKA_ADVERTISED_LISTENERS` gains
  `HOST://[::1]:9094`, the security map gains `HOST:PLAINTEXT`) and publish it on
  the LXC IPv6 loopback only (`ports: ["[::1]:9094:9094"]`). The HOST listener is a
  plain client listener, not the controller or inter-broker listener, so KRaft
  combined mode keeps working. Loopback-only keeps Kafka internal to the LXC.
- `tack-ops` cannot reuse the `app` value `AUDIT_KAFKA_BROKERS=kafka:9092`, so it
  gets a **separate** variable `TACK_OPS_AUDIT_KAFKA_BROKERS` (rendered by configs)
  mapped to `AUDIT_KAFKA_BROKERS` inside the container, exactly as `tack-ops`
  already maps `TACK_OPS_DATABASE_URL` to `DATABASE_URL`.
- FDB-touching ops (`repair apply`, `reindex`, `backfill`) run via
  `docker compose exec app /server ops ...` because `tack-ops` cannot reach FDB on
  host networking (the same reason seed runs in the app container, TACK-318). The
  `app` container keeps `kafka:9092`. The recorder is built identically in both;
  only the broker address differs by container, supplied by env.

## Section 1: one audit spec, one dispatch choke-point

- Add a required `Audit AuditSpec` field to `clispec.Operation` so a mutating op
  cannot be declared without saying how it audits. `AuditSpec`: `Verb string`,
  `Mutates bool`, `BootstrapExempt bool`, `Reads bool`, and optional callbacks
  `ResolveOrg func(ctx, *Env) (uuid.UUID, error)` (default returns `SystemOrgID`),
  `Entity func() audit.Entity`, `Delta func() *audit.Delta`.
- Fold the `ops.go` batch map (`registry`, `Register`, `Run`) into `clispec`
  operations so there is one registry. `registerBatchOps` goes away.
- Route the `clispec` run path through one `Dispatch(ctx, cfg, spec, orgID, fn)`
  choke-point so exactly one place records. `serve` is the only op with no audit.
- Specialized ops (backup, deploy, provision) still build their own Docker/git
  deps inside their run func; common `Pool`/`Stores` come from `Env`.

## Section 2: operator identity (pluggable)

The actor on an event is a kind plus an id, name, and email. Ops events get a new
kind `operator` so they are filterable apart from seed/system actions.

- New `internal/ops/operator.go`:
  - `OperatorPrincipal`: id, email, name, source.
  - `OperatorIdentitySource`: one method, `Resolve(ctx)`.
  - `EnvOperatorSource`: one implementation, reads `TACK_OPERATOR_ID` /
    `TACK_OPERATOR_EMAIL` / `TACK_OPERATOR_NAME`. Missing or bad id returns an
    error, which is what makes a steady-state op refuse to run.
- `Dispatch` and commands only call `Resolve`; nothing in them reads env. A later
  RBAC/OAuth/SSO/IdP source for the internal Tack engineering and operations team
  implements the same one method and is selected at the single construction site.
  This identity layer authenticates the internal team running ops, separate from
  the product's multi-tenant end-user auth (`api_tokens`/`org_members`).
- `internal/config/config.go` gains the three `TACK_OPERATOR_*` fields via
  `caarlos0/env`.
- `internal/audit/recorder.go` gains `ActorOperator ActorType = "operator"`, and
  `actorKindCode` (internal/audit/yugabyte.go) gains `case ActorOperator: return 5`.

## Section 3: recorder, system-org, preflight

- Extract `buildAuditRecorder` into `internal/audit` as `NewRecorderFromConfig`
  and have `cmd/server` call it too, so ops build the identical recorder.
- `Dispatch` requires the **Kafka** recorder for mutating ops. A `NoopRecorder`
  (no broker) or `YBRecorder` (direct SQL write, which violates the single-writer
  invariant) is rejected for mutations, so a missing broker cannot masquerade as a
  successful record. This closes the Noop-returns-nil hole.
- Add a fixed non-nil `SystemOrgID` UUID in `internal/audit` for global-op chains.
  It is only a chain partition key, not a product node in FDB.
- Add an optional interface `ReachabilityChecker` with `Ping(ctx)`. On
  `KafkaRecorder` it delegates to the existing client:
  `func (k *KafkaRecorder) Ping(ctx) error { return k.client.Ping(ctx) }`. Verified
  against franz-go `v1.21.1` (go.mod:22): `(*kgo.Client).Ping(ctx) error` at
  `pkg/kgo/client.go:626` sends a Metadata request to the brokers and returns nil
  on the first success. On the YB recorder it is `r.pool.Ping(ctx)`; `NoopRecorder`
  returns nil. The core `Recorder` interface and the MCP path stay untouched.
  `Dispatch` pings before a mutating op so it aborts before changing anything.

Global op example (`ops audit seed-roles` on live prod): records
`action=ops.audit.seed_roles`, `actor_kind=5`, `org_id=SystemOrgID`.

Entity op example (`ops repair apply --node 7a31...`): `ResolveOrg` runs
`reader.Resolve(nodeID)` and uses `NodeResolve.OrgID`, so the event lands on the
customer's real org with the repair plan as the delta. The `--actor` flag is
dropped in favor of the resolved operator, and the preview's printed
`ApplyCommand` example (cli_repair.go:124) drops `--actor` too.

## Section 4: failure handling

| Class | Operator required | Ledger down (Ping fails) | Record fails |
| --- | --- | --- | --- |
| Steady mutation (repair apply, reindex, backfill, seed-roles live, backup, deploy) | Yes, abort if unresolved | Abort before mutating | Exit non-zero, loud Error |
| Bootstrap (provision, first-boot migrate/seed/seed-roles) | No, record if available | Proceed | Best-effort; provision emits terminal `ops.provision` once the stack is up |
| Read (inspect, verify, validate, repair preview) | Yes, abort if unresolved | Proceed | Best-effort |

Honest limit on fail-closed: no transaction spans FoundationDB/Yugabyte and
Kafka, and cross-database transactions are forbidden in this repo. The guarantee
is preflight, then mutate, then record. The `Ping` before the mutation is what
prevents an unrecorded change when the ledger is down. If the record fails in the
small window after a passing ping, the mutation already happened, so the op exits
non-zero and logs loudly for reconciliation. True atomic mutate-plus-record is
not achievable here.

## Section 5: verification

- `make build` (go-makefile baseline-gated pipeline) and `make check`.
- Unit (`make test-unit`, `./internal/ops/...`, `--no-deps`) with a fake
  recorder, identity source, and `ReachabilityChecker`:
  - `EnvOperatorSource`: missing id, bad id, valid id.
  - `Dispatch`: steady mutation aborts on unresolved operator; aborts on Ping
    failure; aborts when the recorder is Noop or YB; records on success. Read
    aborts on unresolved operator; proceeds on Ping failure (best-effort).
    Bootstrap proceeds with no operator and no ledger.
  - Registry: every registered mutating op carries a non-empty audit verb.
- Integration (`make test-integration`, single-node FDB + Yugabyte): run a real
  mutating op (reindex or repair apply), assert a row in `audit.events` with
  `actor_kind=5`, the right `action`, the expected `org_id` (SystemOrgID for
  global, the real node org for repair apply), and that `audit.chain_heads`
  advanced for that `(org, shard)`.
- QA manual, never prod and never first: with the host Kafka listener live, set
  operator identity, run `repair apply` (app container) and `audit seed-roles`
  (tack-ops) and confirm both produce events and chain continuity via the audit
  MCP tools. Run a mutation with the broker down (expect abort). Run an `inspect`
  with the broker down (expect it proceeds, recorded once the ledger returns).

## Files

New: `internal/ops/operator.go`, `internal/ops/audit.go` (`AuditSpec`,
`Dispatch`).

Changed:
- `docker-compose.yml`: add the `HOST` Kafka listener + loopback publish; add
  `AUDIT_KAFKA_*` and `TACK_OPERATOR_*` to the `tack-ops` env; add `TACK_OPERATOR_*`
  to the `app` env. Exact lines in the appendix. Paired with the configs-repo
  changes (also in the appendix).
- `internal/clispec`: add the `Audit` field to `Operation` and thread `Dispatch`
  into the run path.
- `internal/ops/ops.go`, `internal/ops/cli.go`: fold the batch map into clispec.
- `internal/ops/cli_repair.go`, `cli_audit.go`/`audit_seed_roles.go`,
  `provision.go`, `cli_backup.go`, `cli_deploy.go`, `cli_inspect.go`,
  `cli_verify.go`, `cli_validate.go`, `reindex.go`, `backfill_default_children.go`:
  declare audit specs.
- `cmd/server/commands.go` (`migrate`), `cmd/server/seed.go`,
  `cmd/server/audit_runtime.go`.
- `internal/config/config.go`: `TACK_OPERATOR_*` fields.
- `internal/audit/recorder.go` (`ActorOperator`), `internal/audit/yugabyte.go`
  (`actorKindCode` case), plus `NewRecorderFromConfig`, `SystemOrgID`, and
  `ReachabilityChecker` in `internal/audit`.

## Appendix A: exact `docker-compose.yml` changes (tack repo)

`kafka` service env (three existing lines gain a `HOST` entry):

```yaml
KAFKA_LISTENERS: PLAINTEXT://[::]:9092,CONTROLLER://[::]:9093,HOST://[::]:9094
KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:9092,HOST://[::1]:9094
KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,HOST:PLAINTEXT
```

`kafka` service gains a `ports` block (it has none today):

```yaml
ports:
  - "[::1]:9094:9094"
```

`app` service env gains operator identity (FDB ops run via `exec app`):

```yaml
TACK_OPERATOR_ID: ${TACK_OPERATOR_ID:-}
TACK_OPERATOR_EMAIL: ${TACK_OPERATOR_EMAIL:-}
TACK_OPERATOR_NAME: ${TACK_OPERATOR_NAME:-}
```

`tack-ops` service env gains its own producer endpoint plus operator identity:

```yaml
AUDIT_KAFKA_BROKERS: ${TACK_OPS_AUDIT_KAFKA_BROKERS:-[::1]:9094}
AUDIT_KAFKA_TOPIC: ${AUDIT_KAFKA_TOPIC:-audit.events.v1}
AUDIT_KAFKA_CLIENT_ID: ${AUDIT_KAFKA_CLIENT_ID:-tack-ops-audit-producer}
AUDIT_KAFKA_PRODUCE_TIMEOUT: ${AUDIT_KAFKA_PRODUCE_TIMEOUT:-10s}
TACK_OPERATOR_ID: ${TACK_OPERATOR_ID:-}
TACK_OPERATOR_EMAIL: ${TACK_OPERATOR_EMAIL:-}
TACK_OPERATOR_NAME: ${TACK_OPERATOR_NAME:-}
```

## Appendix B: exact configs repo changes (paired PR, per the seam)

`tack/tack.env.j2` gains:

```
TACK_OPERATOR_ID={{ tack_operator_id }}
TACK_OPERATOR_EMAIL={{ tack_operator_email }}
TACK_OPERATOR_NAME={{ tack_operator_name }}
TACK_OPS_AUDIT_KAFKA_BROKERS={{ tack_ops_audit_kafka_brokers }}
```

`ansible/inventory/group_vars/tack_all.yml` gains (shared, non-secret):

```yaml
tack_ops_audit_kafka_brokers: "[::1]:9094"
tack_operator_email: ops@goodkind.io
tack_operator_name: "Tack Operator"
```

`tack_servers.yml` and `tack_qa_servers.yml` each gain a minted, per-environment
`tack_operator_id` (a UUID, mirroring how `tack_kafka_cluster_id` is per-env), so
prod and QA operator actions are distinguishable in the ledger. The id is an
identifier, not a secret, so it lives in group_vars, not vault. Per-human
attribution before SSO lands comes from
`docker compose run -e TACK_OPERATOR_ID=... tack-ops ...` at invocation.

## Residual unknown (only one, QA-gated)

Kafka accepting the extra `HOST` listener in KRaft combined mode, and the
`[::1]:9094` host publish behaving like the verified `[::1]:5433` yugabyte
precedent, must be confirmed by a QA bring-up before prod, the same gate any
listener change goes through.

## Tickets

TACK-328 blocks TACK-327. TACK-327 lockdown proceeds only after this lands and is
verified on QA.
