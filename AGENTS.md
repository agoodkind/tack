# Tack

Tack is a horizontally scalable, multi-tenant project management platform: a
replacement for Plane CE, Linear, and Jira. It is not a personal tool or a
prototype. Decide every feature for a real multi-tenant product with real users;
never justify a decision by assuming a single user. `CLAUDE.md` is a symlink to
this file.

## How to read this file

This file defines durable rules and settled decisions only. It does not restate
values from code or configuration, because copies drift. For concrete values
(keys, schema, env vars, services, hosts), read the sources in
[Check current sources](#check-current-sources). Correct descriptive statements
in this file when they conflict with verified code or configuration. Correct code
that violates a binding rule; an existing violation does not override the rule.

## Repository responsibilities

The LXC defines the repository boundary.

- The configs repo (`github.com/agoodkind/configs`) manages Proxmox, the LXC,
  host networking, the rendered `.env`, the audit signing key, the FoundationDB
  cluster directory, and the SeaweedFS object-store LXC.
- This repo manages everything Docker runs inside the LXC: `docker-compose.yml`
  (the stack source of truth), the overlays (`fdb-overlay/fdb.bash`,
  `yugabyte-overlay/yugabyted`), the application code, and `./server ops`.

Any live value absent from the configs repo is drift, and drift is an incident.
Change configuration in the configs repo first; never hand-edit it on the host.
Ansible re-renders `.env` from
[`tack.env.j2`](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2)
on every deploy.

Use one deploy path:

- The repository's build workflow builds and pushes the `tack-server` and
  `tack-audit-consumer` images for every commit on `main`.
- Ansible
  [`deploy-tack.yml`](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-tack.yml)
  handles first boot and every change: it prepares the LXC, renders `.env` with
  the image tag, fetches the stack from this repo at the deployed ref, and runs
  `docker compose up -d` for every service.
- `./server ops deploy verify` runs on the guest afterward and asserts that
  the `app` and `audit-consumer` containers run the images the deploy specified.
  Do not build, push, or replace deployment images from a laptop.

## Settled decisions (do not re-litigate)

- SeaweedFS runs in a configs-owned Proxmox LXC, not as a tack service. The
  `weed` binary runs under systemd with S3 on port 8333. Clients inside the tack
  LXC connect over S3. `docker-compose.yml` does not define a `seaweedfs` service.
  The configs repo manages its provisioning and configuration.
- QA (`tack_qa`) is disposable. Destroy and recreate it freely to match prod.
  Validate every migration, seed, backfill, and restore on QA before prod; QA is
  a fresh host with vault-sourced secrets and does not require live state
  protection.
- The audit ledger keeps one `audit.events` table. The Kafka cutover makes
  the `audit-consumer` the single writer and continues the existing
  per-`(org, shard)` hash chain through `audit.chain_heads`. Do not rename the
  table, add an archive table, or create `events_v2`. Follow
  [the audit Kafka cutover design](docs/plans/audit-kafka-cutover.md).
- Run migrations via `./server migrate` only, never on HTTP startup.
- Build with `make build`. It runs the go-makefile pipeline (vet, golangci,
  staticcheck-extra, govulncheck) against the baseline. Do not call `go build`
  directly.

## Check current sources

Read these sources for current state. Do not memorize or copy their values into
this file.

| For | Read |
| --- | --- |
| FDB key families | `internal/adapters/foundationdb/keys.go` |
| SQL schema (auth + audit only) | `migrations/*.sql` |
| Server config and every env var | `internal/config/config.go` |
| The running stack and its services | `docker-compose.yml` |
| Ops commands | `./server ops help` (`internal/ops`) |
| Deploy-time values and `.env` | configs [`tack.env.j2`](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2) |
| Hosts, networking, the LXC | configs [`deploy-tack.yml`](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-tack.yml) and `service_mapping.yml` |
| Roadmap and ticket state | TACK issues via the MCP tools |
| Cross-session context | the memory handoff under the session memory directory |
| Local test stack and targets | `docker-compose.test.yml`, `Makefile` (`test-unit`, `test-integration`) |

## Local testing

Run tests in QA or Docker containers. Run tests against production only with
explicit operator authorization. Use `docker-compose.test.yml` for local container
tests. `make test-unit` and `make test-integration` build and run the test runner
with their package selections. Tests start required stores through
[internal/testenv](internal/testenv) and remove their owned engines when the test binary exits.
After an interrupted runner leaves engines behind, `make test-env-down` removes
engines of exited test processes and preserves engines of running tests.

Tests connect to FoundationDB on the Docker network because Docker Desktop on
macOS drops FoundationDB's connect-packet exchange through its TCP port
forwarder. Local FoundationDB tests use a single node rather than the production
IPv6-only bridge. `make test` excludes the integration package, which CI runs
separately through `make test-store-host-shard`.

## Architecture (binding)

### Everything is a node in FoundationDB

Every entity (org, workspace, project, state, label, issue, epic, cycle, module,
comment, activity, custom type) is one node. Each follows the same pattern:
NodeValue (primary record) plus NodeListView (materialized read row) plus
NodeResolve (global resolution record). Callers address every entity by its UUID
alone without providing org context. Edges are Relationship records.
Behavior follows NodeType metadata, never hardcoded type names.

### SQL is auth plus compliance audit only

YugabyteDB stores `users`, `api_tokens`, `org_members`, and the `audit.*` ledger.
Do not store product entities, configuration, relationships, or read-model tables
in SQL. Treat YugabyteDB backups as compliance artifacts: auth gates and the audit
ledger must survive every recovery.

### FoundationDB stores all product data

FoundationDB stores all entities, relationships, NodeListView read rows,
NodeResolve records, activity, comments, properties, sequences, and automation
rules.

### Data access

Use `NodeReader` for every read; the service layer never calls
`EntityRepository`, `PropertyRepository`, `AssignmentRepository`, or
`LabelRepository` directly for reads. Use `EntityRepository.CreateAtomic` or
`EntityRepository.Set` for every write. These methods write NodeValue, property
values, NodeResolve, NodeListView, assignments, and labels in one FDB transaction.
Do not use cross-database transactions or multi-step creates.

`orgID` is never a caller parameter. Derive it: entity-scoped ops resolve the
entity (`reader.Resolve`) to its `OrgID`; workspace-scoped ops read the workspace
node. The storage layer uses `orgID` internally for key locality and never
exposes it to the API or service layer.

### Auth

Authentication hashes the bearer token from `Authorization: Bearer <token>`
with SHA-256, looks up `api_tokens`, and returns a `userID`. In `ENV=development`, authentication
uses the bearer as a raw user UUID without a DB lookup. Per-entity auth resolves
the entity to its `orgID` and checks `org_members`.

### API layer

- MCP Streamable HTTP (`/mcp`) is the primary interface. NodeType metadata drives
  per-user tool registration and declares human-readable workspace and project
  address inputs. `orgID` never appears as an input field.
- Connect-RPC (`/tack.v1.*`) is the typed API for the future frontend and TUI.
  Entity-scoped ops take a UUID; collection ops take `workspace_id` or
  `project_id`.

### Durable invariants

- Use UUIDv7 for all new entities (k-sortable, coordination-free range scans).
- Declare human-readable references as metadata; use the UUID as canonical
  identity. Keep tool, type, and display names separate from node identity and
  address values.
- `CanLiveUnder` defines hierarchy as a DAG; the resolver walks the scope chain
  for N levels without assuming a depth.
- Add a field to NodeValue/NodeListView only if a node in a completely different
  product on this architecture would need it (`ID`, `OrgID`, `NodeType`, `Name`,
  timestamps, `CreatedBy`, `UpdatedBy`, `Props`). Represent everything else as a
  property, a NodeResolve scope position, or a relationship.
- Store descriptions as Markdown TEXT, never HTML.
- Make every update partial: change only provided fields.
- Declare optional MCP input fields as `*string` with `json:",omitempty"`
  (required by `google/jsonschema-go`); use `jsonschema:"..."` tags for
  descriptions, not validation constraints.
- Do not defer scale requirements as technical debt. If a design will not scale,
  do not ship it.

## YugabyteDB on the IPv6-only bridge

The `default` Docker network is IPv6-only (`enable_ipv4: false`,
`gateway_mode_v6: routed`). Do not flip it to IPv4 or dual-stack; v4 lets
services silently fall back. Service hostnames (`fdb`, `yugabyte`) resolve to v6
only.

YugabyteDB's database identity must be the stable Docker DNS name `yugabyte`, not
a container GUA. The `yugabyted` command advertises and listens on `yugabyte`
(`--advertise_address=yugabyte --listen=yugabyte`); never derive it from
`getent` or pin a literal GUA, because stale persisted addresses prevent YSQL
from starting.

The image's `is_port_available` opens an IPv4 socket and fails on this bridge.
`yugabyte-overlay/yugabyted` includes the upstream `bin/yugabyted` with the
AF_UNSPEC fix from upstream PR
[#23158](https://github.com/yugabyte/yugabyte-db/pull/23158). Docker bind-mounts
it `:ro`. On any image bump, refresh the overlay (re-fetch upstream
`bin/yugabyted` for the new tag and re-apply the `is_port_available` patch),
verify on QA first, then prod. `.gitleaks.toml` and `.gitguardian.yaml` allowlist
the overlay.

## Recovery and backups

Use quorum replication across fault domains for disaster recovery from node or
zone failure. Replication preserves committed data while enough replicas survive
to form a quorum. Replication does not protect against corruption or accidental
deletion. Use point-in-time recovery and backups to recover from those failures.
Object storage stores backups and does not replace quorum replication. Preserve
`audit.chain_heads` during audit recovery to continue the hash chain. Follow
[the recovery procedures](docs/runbooks/recovery.md).

## First boot on a fresh environment

A fresh environment (QA recreate, a new server, a DR restore) is not ready after
`deploy-tack` alone. The deploy runs `docker compose up -d` and checks fdb health,
but some initialization steps still require manual commands. fdb cannot become
healthy until the first step runs. Until Tack implements the provisioning layer
(TACK-303, "deploy is not provision"), run these steps in order:

1. A fresh `tack_fdb-data` volume does not contain a configured database.
   `tack-fdb-1` stays unhealthy and the deploy aborts. Initialize it once:
   `docker exec tack-fdb-1 fdbcli --exec 'configure new single ssd'`, then re-run
   `deploy-tack`. The fdb overlay writes the client cluster file at
   `/etc/foundationdb/fdb.cluster` as `docker:docker@fdb:4500` (the DNS name) on
   start. Clients use that name after container IP changes. TACK-290 automates
   the configure step. NEVER run `configure new` against an already-configured
   cluster (prod): it is the one destructive step in this list.
2. Run `docker compose run --rm tack-ops migrate --execute`.
3. Run `docker compose run --rm tack-ops ops audit seed-roles`. The app and the
   audit-consumer cannot authenticate to YugabyteDB until this runs. The
   audit-consumer pings Yugabyte until the roles exist, then ensures the Kafka
   topic and starts without a restart (TACK-301, TACK-305). The app does not open
   ledger read or redaction connections. Host commands (`audit query`,
   `audit get`, `audit export`, `audit redact-actor`) open their own reader and
   redactor pools; MCP tools do not provide audit reads or redactions
   (TACK-456, TACK-458). The audit queue commands (`ops audit consumer-offsets`,
   `ops audit dlq inspect`, `ops audit dlq replay`) run in the `audit-ops` service,
   which mounts the reader DSN and sets the Kafka brokers on the bridge network.
   tack-ops is host-networked and cannot resolve the broker (TACK-610).
   Run the recorded break-glass command (`ops db sql`) in `tack-ops`. The
   `tack-ops` environment has the alarm mail settings the command needs; the
   `app` service connects as `tack_app`, which cannot read the ledger
   (TACK-327). The command runs one statement in a read-only session.
   `tack-ops` connects as `tack_migrator`, which owns the schema and is not a
   superuser. Only a from-empty rebuild uses the engine superuser. The deploy
   passes that credential for that run and does not render it into `.env`.
   Write to the database only with a migration or a reviewed ops command. The
   schema guard of migration 019 refuses a hand-written schema change on the
   `audit` and `partman` schemas (TACK-552).
   Apply migration 011 before deploying the audit-consumer version that requires
   it. The full deploy runs `ops provision --execute`, which applies pending
   migrations before starting the app and consumer (TACK-453).
   `./server ops deploy` swaps images only. If the database has not applied
   migration 011, run step 2 first (TACK-336).
4. The audit-consumer ensures `audit.events.v1` with 256 partitions on startup
   once it can connect to Yugabyte (TACK-305). A fresh broker creates the topic
   automatically after step 3; do not create it manually.
5. Run `docker compose exec -T app /server seed` for the initial user, org,
   workspace, and API token. Use the app container, not `tack-ops`: tack-ops uses
   `network_mode: host`, which cannot resolve `fdb` in its FDB cluster file.
   A tack-ops seed cannot write FoundationDB nodes (TACK-318). migrate and
   seed-roles still use tack-ops because they are SQL-only.

TACK-303 tracks the missing provisioning layer. TACK-304 tracks whether QA stays
running or requires recreation each time.

## Binding rules for code in this repo

1. Represent everything as a node (see Architecture). Define behavior with
   NodeType metadata, never hardcoded type names.
2. Do not use shell scripts or `os/exec` of CLIs in tack Go. Run engine CLIs
   (`fdbbackup`, `yb-admin`, `ysql_dump`, `tar`) inside one-shot containers through
   the Docker Go SDK helpers in `internal/ops/dockerctl.go`.
   Do not create or use non-Go scripts, including temporary and standalone
   helpers, without explicit operator approval. Go implementations must pass
   this repository's canonical lint and quality checks.
3. Record every `./server ops` command through the one `clispec` audit
   choke-point; do not weaken it or bypass it per command. Keep operator
   identity pluggable via `audit.OperatorIdentitySource`. Follow
   [operator identity and audit requirements](docs/operator-identity-and-audit.md).
4. Add `ops qa datagen` coverage in the same change for every new node type,
   property type, relationship shape, audit verb, or user-visible code path.
   Run it through the `app` service, which can connect to FoundationDB and has
   the audit DSNs that the host-networked `tack-ops` container lacks:

   ```bash
   docker compose run --rm app ops qa datagen seed --scale small --commit
   docker compose run --rm app ops qa datagen soak --duration 60s --rate 3 --commit
   ```

   Writing requires `TACK_DATAGEN_ALLOW_TARGET` to be exactly `qa` or `local`.
   tack-qa sets it and production leaves it empty. The generator refuses writes
   on production. Dry run is the default and does not require a marker.

5. Declare a lifetime for every one-time command:
   `Lifetime: clispec.Lifetime{Ticket: "TACK-nnn", RemoveBy: time.Date(...)}`.
   It renders under a `backfill` group with the `once-` prefix, and
   `make build` fails once the removal day has passed. Declare
   `clispec.Permanent` for every other command.
   Only `internal/service` may read the seed sample data (TACK-512).

## Auditability gate

Require complete audit evidence before merging or releasing any new or changed
operation that reads, lists, searches, exports, creates, updates, deletes, or
otherwise processes user data. Apply this gate to public APIs, generated tools,
operator commands, background jobs, provider hooks, and storage writers.

Follow the [complete audit recording contract](docs/superpowers/specs/2026-10-21-audit-completeness-design.md)
through the existing shared operation definitions, transactional outboxes, and
permanent ledger. Apply it to custom node types and relationships.

1. Record the verified actor or explicit unauthenticated identity, operation,
   complete targets, original time, committed order, and outcome. Record the
   initiating identity during background work, retries, and recovery. Record
   refusals, failures, partial work, and interruptions.
2. Record actual old and new values for every changed field and relationship,
   including removals. Commit change evidence atomically with database changes.
   Record complete relationship identities and apply existing protected-value
   storage and redaction rules.
3. Admit reads durably and finalize their evidence before returning data,
   including each streaming page or chunk. Record returned identities, result
   counts, selection criteria, and payload fingerprints through the shared read
   contract. A fingerprint does not reconstruct the returned content.
4. Refuse unaudited operations when required identity or recording fails. Do not
   release a read result or commit a database change without its required audit
   evidence. Record external-operation intent before effects and verify actual
   state during recovery.
5. Add regression coverage through the affected public operation with real
   dependencies. Inspect permanent audit records for the actual actor, targets,
   outcomes, change values, and read manifests and fingerprints. Exercise
   authorization refusals and recording failures; declarations and log messages
   alone do not prove coverage.
6. Include the audit evidence and test results in the PR. Require the existing
   `runtime audit coverage` check and applicable integration checks to pass before
   merge. Verify the accepted behavior on QA before production release.

Do not add recording opt-outs, exclude new operations from coverage, weaken audit
assertions, bypass required checks, or defer recording to a follow-up ticket.
Treat an unaudited existing implementation as a defect, not an exception to this
gate. A healthy deployment or a passing unrelated test does not satisfy it.

## No config files

Configure the server only with environment variables through `caarlos0/env`.
Do not introduce TOML, YAML, JSON, or any file-based config loading.

## Logging

Use `log/slog` throughout; `telemetry.Setup` initializes the global logger and
handlers retrieve the context logger via `telemetry.L(ctx)`. Log every
significant event: entity lifecycle, background jobs, auth failures, startup, and
shutdown. Use named `slog.Attr` fields, never positional. Use `noun.verb` message
names (`issue.created`, `worker.started`). Use only `Info` for normal flow,
`Debug` for trace detail, and `Error` for actual failures.

## File size and concern separation

Keep every file at or below 200 lines. Split larger files by concern (one file per
entity for conversions, bulk ops separate from CRUD). Name a file after its
responsibility; use `utils.go` and `helpers.go` only as a last resort for genuinely
shared code.

## Readability over conciseness

Use the full domain term, not abbreviations (`workspaceID`, not `wsID`). Every
error includes context and the relevant identifier
(`fmt.Errorf("get issue %s: %w", id, err)`). Every package has a `// Package X`
doc comment; every exported type and non-obvious field has a doc comment.

## What not to do

- Do not add a config file format (TOML, YAML, JSON).
- Do not add error handling for scenarios that provably cannot happen.
- Do not add backwards-compatibility shims, unused exports, or re-exports.
- Do not add docstrings or comments to unchanged code.
- Do not add features not explicitly requested.
- Do not restate concrete values in this file; reference the source-of-truth
  files above.
