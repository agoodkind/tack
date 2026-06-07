# Tack

Tack is a horizontally scalable, multi-tenant project management platform: a
replacement for Plane CE, Linear, and Jira with a better architecture. It is not
a personal tool or a prototype. Decide every feature for a real multi-tenant
product with real users; never frame a decision as "for now, since it's just
you." `CLAUDE.md` is a symlink to this file.

## How to read this file

This file holds durable rules and settled decisions only. It does not restate
values that live in code or config, because copies drift. For anything concrete
(keys, schema, env vars, services, hosts), read the source of truth named in
[Where the truth lives](#where-the-truth-lives). If this file and the code
disagree, the code wins; fix this file.

## The seam: configs repo vs tack repo

One boundary, drawn at the LXC.

- **Up to and including the LXC is the configs repo**
  (`github.com/agoodkind/configs`). Proxmox, the LXC itself, host networking, the
  rendered `.env`, the audit signing key, the FoundationDB cluster directory, and
  the SeaweedFS object-store LXC. Everything around the container runtime.
- **Inside the LXC is this repo.** Everything Docker runs: `docker-compose.yml`
  (the stack source of truth), the overlays (`fdb-overlay/fdb.bash`,
  `yugabyte-overlay/yugabyted`), the application code, and `./server ops`.

Any live value absent from the configs repo is drift, and drift is an incident.
Config goes to the configs repo first and is never hand-edited on the host;
Ansible re-renders `.env` from
[`tack.env.j2`](https://github.com/agoodkind/configs/blob/main/tack/tack.env.j2)
on every deploy.

Two deploy actions, split by phase:

- **Ansible
  [`deploy-tack.yml`](https://github.com/agoodkind/configs/blob/main/ansible/playbooks/deploy-tack.yml)**
  does first boot and any full-stack change: it prepares the LXC, renders
  `.env`, fetches the stack from this repo at the deployed ref, and runs
  `docker compose up -d` for every service.
- **`./server ops deploy`** does app-image updates only (`app`,
  `audit-consumer`): build, push, pull, `up -d`, and verify the running digest.
  It never does first boot and never starts the databases.

`make deploy` is retired.

## Settled decisions (do not re-litigate)

- **SeaweedFS is a configs-owned LXC, not a tack service.** The object store runs
  as its own Proxmox LXC (the `weed` binary under systemd, S3 on port 8333),
  reached over S3 from inside the tack LXC. `docker-compose.yml` has no
  `seaweedfs` service. Its provisioning and config live in the configs repo.
- **QA (`tack_qa`) is disposable.** Destroy and recreate it freely to match prod.
  Validate every migration, seed, backfill, and restore on QA before prod; QA is
  a fresh host with vault-sourced secrets and no live state to protect.
- **The audit ledger keeps one `audit.events` table.** The Kafka cutover makes
  the `audit-consumer` the single writer and continues the existing
  per-`(org, shard)` hash chain through `audit.chain_heads`: no rename, no archive
  table, no `events_v2`. Design of record:
  [`docs/plans/audit-kafka-cutover.md`](docs/plans/audit-kafka-cutover.md).
- **Migrations run via `./server migrate` only**, never on HTTP startup.
- **Build with `make build`.** It runs the go-makefile pipeline (vet, golangci,
  staticcheck-extra, govulncheck) baseline-gated. Do not call `go build`
  directly.

## Where the truth lives

Find current state here; do not memorize or copy it into this file.

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

## Architecture (binding)

### Everything is a node in FoundationDB

Every entity (org, workspace, project, state, label, issue, epic, cycle, module,
comment, activity, custom type) is one node. Each follows the same pattern:
NodeValue (primary record) plus NodeListView (materialized read row) plus
NodeResolve (global resolution record). Every entity is addressable by its UUID
alone, with no org context from the caller. Edges are Relationship records.
Behavior follows NodeType metadata, never hardcoded type names.

### SQL is auth plus compliance audit only

YugabyteDB holds `users`, `api_tokens`, `org_members`, and the `audit.*` ledger.
No product entity, config, relationship, or read-model table lives in SQL. Treat
YugabyteDB backups as compliance artifacts: auth gates and the audit ledger must
survive every recovery.

### FoundationDB holds all product data

All entity storage, all relationships, all NodeListView read rows, all
NodeResolve records, plus activity, comments, properties, sequences, and
automation rules.

### Data access

Every read goes through `NodeReader`; the service layer never calls
`EntityRepository`, `PropertyRepository`, `AssignmentRepository`, or
`LabelRepository` directly for reads. Every write goes through
`EntityRepository.CreateAtomic` or `EntityRepository.Set`, which write NodeValue,
property values, NodeResolve, NodeListView, assignments, and labels in one FDB
transaction. No cross-database transactions, no multi-step creates.

`orgID` is never a caller parameter. Derive it: entity-scoped ops resolve the
entity (`reader.Resolve`) to its `OrgID`; workspace-scoped ops read the workspace
node. The storage layer uses `orgID` internally for key locality and never
surfaces it to the API or service layer.

### Auth

`Authorization: Bearer <token>` hashes to SHA-256, looks up `api_tokens`, and
yields a `userID`. In `ENV=development` the bearer is the raw user UUID with no
DB lookup. Per-entity auth resolves the entity to its `orgID` and checks
`org_members`.

### API layer

- **MCP Streamable HTTP** (`/mcp`) is the primary interface: per-user tool
  registration driven by NodeType metadata. Human-readable workspace and project
  inputs are address values declared by metadata. `orgID` never appears as an
  input field.
- **Connect-RPC** (`/tack.v1.*`) is the typed API for the future frontend and
  TUI. Entity-scoped ops take a UUID; collection ops take `workspace_id` or
  `project_id`.

### Durable invariants

- UUIDv7 for all new entities (k-sortable, coordination-free range scans).
- Human-readable references are declared metadata; the UUID is canonical
  identity. Tool, type, and display names are separate from node identity and
  from address values.
- Hierarchy is a DAG defined by `CanLiveUnder`; the resolver walks the scope
  chain for N levels with no depth assumption.
- Universal-field test: a field belongs on NodeValue/NodeListView only if it
  would exist on a node in a completely different product on the same
  architecture (`ID`, `OrgID`, `NodeType`, `Name`, timestamps, `CreatedBy`,
  `UpdatedBy`, `Props`). Everything else is a property, a NodeResolve scope
  position, or a relationship.
- Descriptions are Markdown TEXT, never HTML.
- Updates are partial everywhere: only provided fields change.
- Optional MCP input fields are `*string` with `json:",omitempty"` (required by
  `google/jsonschema-go`); `jsonschema:"..."` tags are descriptions, not
  validation constraints.
- No tech debt disguised as "we can fix this later for scale." If it won't scale,
  don't ship it.

## YugabyteDB on the IPv6-only bridge

The `default` Docker network is IPv6-only (`enable_ipv4: false`,
`gateway_mode_v6: routed`). Do not flip it to IPv4 or dual-stack; v4 lets
services silently fall back. Service hostnames (`fdb`, `yugabyte`, `meilisearch`,
`temporal`) resolve to v6 only.

YugabyteDB's database identity must be the stable Docker DNS name `yugabyte`, not
a container GUA. The `yugabyted` command advertises and listens on `yugabyte`
(`--advertise_address=yugabyte --listen=yugabyte`); never derive it from
`getent` or pin a literal GUA, because stale persisted addresses wedge YSQL.

The image's `is_port_available` opens an IPv4 socket and fails on this bridge, so
`yugabyte-overlay/yugabyted` carries the upstream `bin/yugabyted` with the
AF_UNSPEC fix from upstream PR
[#23158](https://github.com/yugabyte/yugabyte-db/pull/23158), bind-mounted `:ro`.
On any image bump, refresh the overlay (re-fetch upstream `bin/yugabyted` for the
new tag, re-apply the `is_port_available` patch), verify on QA first, then prod.
The overlay is allowlisted in `.gitleaks.toml` and `.gitguardian.yaml`.

## Recovery and backups

Disaster recovery is quorum replication across fault domains, which loses no data
on node or zone failure. Point-in-time recovery and backups are the slower,
separate layer for corruption and accidental deletion, the failure class
replication copies everywhere. Object storage is a backup destination, not a DR
mechanism. On any audit recovery, preserve `audit.chain_heads` so the hash chain
continues. Procedures: [`docs/runbooks/recovery.md`](docs/runbooks/recovery.md).

## Binding rules for code in this repo

1. **Everything is a node** (see Architecture). Behavior follows NodeType
   metadata, never hardcoded type names.
2. **No shell-outs in tack Go.** No shell scripts and no `os/exec` of CLIs.
   Engine CLIs (`fdbbackup`, `yb-admin`, `ysql_dump`, `tar`) run inside one-shot
   containers through the Docker Go SDK helpers in `internal/ops/dockerctl.go`.

## No config files

Server configuration is environment variables only, through `caarlos0/env`.
There is no config file. Do not introduce TOML, YAML, JSON, or any file-based
config loading.

## Logging

Use `log/slog` throughout; `telemetry.Setup` initializes the global logger and
handlers retrieve the context logger via `telemetry.L(ctx)`. Log every
significant event: entity lifecycle, background jobs, auth failures, startup, and
shutdown. Use named `slog.Attr` fields, never positional. Message names use
`noun.verb` (`issue.created`, `worker.started`). Three levels, used strictly:
`Info` for normal flow, `Debug` for trace detail, `Error` for actual failures.

## File size and concern separation

No file exceeds 200 lines; split by concern when it does (one file per entity for
conversions, bulk ops separate from CRUD). Name a file after its responsibility;
`utils.go` and `helpers.go` are last resorts for genuinely shared code.

## Readability over conciseness

Use the full domain term, not abbreviations (`workspaceID`, not `wsID`). Every
error includes context and the relevant identifier
(`fmt.Errorf("get issue %s: %w", id, err)`). Every package has a `// Package X`
doc comment; every exported type and non-obvious field has a doc comment.

## What not to do

- Do not add a config file format (TOML, YAML, JSON).
- Do not add error handling for scenarios that provably cannot happen.
- Do not add backwards-compatibility shims, unused exports, or re-exports.
- Do not add docstrings or comments to code you did not change.
- Do not add features not explicitly requested.
- Do not restate in this file values that live in the source-of-truth files
  above; point to them instead.
