# Tack project contract

Design Tack as a production project management platform for many tenants and horizontal scale. Tack replaces Plane CE, Linear, and Jira. Do not design features as personal tools or prototypes, or defer required scale support because current use is small. Keep [CLAUDE.md](CLAUDE.md) as a symlink to this file.

## Contract and current sources

Keep durable rules and settled decisions in this contract. Read code and configuration for current keys, schemas, environment variables, services, and hosts. Correct descriptive claims when verified sources contradict them. Correct implementation defects without weakening a binding rule.

Use the owning source when checking current values.

Source references:

| Subject | Required source |
| --- | --- |
| FoundationDB key families | [Key definitions](internal/adapters/foundationdb/keys.go) |
| Authentication and audit SQL schema | [Migrations](migrations/) |
| Server environment variables | [Configuration](internal/config/config.go) |
| Container services and mounts | [Compose stack](docker-compose.yml) |
| Operator commands | Run `./server ops help` and read [ops](internal/ops/) for implementation details. |
| Guest and network declarations | Read the Configs [OpenTofu roots and modules](https://github.com/agoodkind/configs/tree/main/opentofu). |
| Roadmap and ticket state | Query TACK issues through the MCP tools. |
| Cross-session context | Read the session memory handoff. Verify mutable claims against current sources. |

## Repository ownership and deployment

The Configs repository owns Proxmox, Linux containers (LXCs), host networking, the rendered environment, secret files, audit signing keys, the FoundationDB cluster directory, and the SeaweedFS object-store LXC. Tack owns application code, the Compose stack, container overlays, and `./server ops` inside the guest.

Commit configuration changes to Configs before applying them. Do not edit live host configuration by hand. Treat a live value that lacks a Configs declaration as configuration drift and an incident.

Use the reviewed Configs deployment path. The [release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf) updates app and audit-consumer images and runs audited checks against an existing installation. It does not prepare a fresh guest, install stack configuration or credentials, or apply migrations. Require those prerequisites separately. Do not build, push, or replace release images from a laptop.

The [image workflow](.github/workflows/build-push.yml) builds and publishes `tack-server` and `tack-audit-consumer` images for commits on `main`. Require `./server ops deploy verify` on the guest after an image update. Verify expected tags and digests for the services that the release changes; a guest that runs only `app` requires that service's verification. Image verification does not establish provisioning or public behavior.

Keep SeaweedFS in its Configs-owned LXC. Run its `weed` service under systemd and use its S3 endpoint from Tack. Do not add SeaweedFS to the Tack Compose stack. Read the Configs declaration for its current endpoint and port.

## Architecture

Represent every product entity as a FoundationDB node. Store each node's required records atomically.

Required records:

| Record | Current Go type |
| --- | --- |
| Primary record | `Node` |
| Materialized read row | `NodeView` |
| Global resolution record | `NodeResolve` |

Represent edges as `Relationship` records. Derive behavior from `NodeType` metadata rather than hardcoded type names. Add new entity types through metadata and data without changing application code.

Use FoundationDB for all product data.

Use YugabyteDB SQL only for authentication and compliance auditing.

Permitted SQL records:

- Store users in `users`.
- Store API tokens in `api_tokens`.
- Store organization memberships in `org_members`.
- Store the compliance ledger in `audit.*`.

Do not store product entities, configuration, relationships, or product read models in SQL. Preserve authentication records and the audit ledger during every recovery; treat YugabyteDB backups as compliance artifacts.

Use `NodeReader` for service reads rather than reading storage repositories directly. Create and update nodes through the checked atomic mutation interface, `node.Mutator.ApplyMutation`.

Commit all parts of a node change in one FoundationDB transaction:

- Commit the primary record.
- Commit property values.
- Commit the resolution record.
- Commit the read row.
- Commit indexes.
- Commit reference claims.
- Commit relationships, including assignments and labels.
- Commit required audit evidence.

Do not use a cross-database transaction or a multi-step entity create. Do not substitute `NodeRepository.Set` for a user-facing update that requires index reconciliation.

Address every entity by its UUID without requiring an organization parameter from the caller. Derive `orgID` from `reader.Resolve` for an entity operation or from the workspace node for a workspace operation. Keep organization selection inside storage and authorization; do not expose `orgID` as a caller-controlled API or service input. The service may use the derived organization internally. Derive containing scopes from relationships rather than adding scope positions to `NodeResolve`.

Authenticate `Authorization: Bearer <token>` by looking up its SHA-256 hash in `api_tokens` and obtaining `userID`. Only `ENV=development` accepts a raw user UUID without the token-table lookup. Resolve each entity's organization and require the user's membership in `org_members` before allowing the operation.

Use MCP Streamable HTTP at `/mcp` as the primary interface. Register each user's tools from `NodeType` metadata. Treat human-readable workspace and project inputs as metadata-declared address values; do not accept `orgID` as an input. Use Connect-RPC at `/tack.v1.*` as the typed interface for frontend and terminal clients. Entity operations accept UUIDs; collection operations accept `workspace_id` or `project_id`.

Apply these data requirements:

- Create new entities with UUIDv7. Its time-ordered identifiers support range scans without a shared identifier allocator.
- Keep the UUID as canonical identity. Declare human-readable references in metadata and distinguish tool, type, and display names from node identity and address values.
- Use `CanLiveUnder` metadata to define a directed acyclic graph of permissible parent types. Require one canonical direct parent for each node that needs a parent. Keep the node's [reference and numbering scope](docs/reference/nodes/2026-10-16-node-integrity.md#scope-containment-and-deletion) independent of its containment parent. Resolve scope chains without assuming a fixed depth.
- Add a field to `Node` or `NodeView` only when a different product using this architecture would also need it. Represent other data as properties or relationships.
- Store descriptions as Markdown text. Do not store HTML descriptions.
- Change only supplied fields during updates.
- Declare optional MCP string inputs as `*string` with `json:",omitempty"` for `google/jsonschema-go`. Use `jsonschema:"..."` tags for descriptions; they do not enforce validation constraints.
- Require scale support before shipping a design. Do not defer it as future technical debt.

Apply the universal-field test to these fields:

- Use `ID` for node identity.
- Use `OrgID` for organization identity.
- Use `NodeType` for the metadata-defined type.
- Use `Name` for the node name.
- Use timestamps for creation and update times.
- Use `CreatedBy` for the creating actor.
- Use `UpdatedBy` for the updating actor.
- Use `Props` for property values.

## Networking and engine updates

Keep the default Docker network IPv6-only. Do not enable IPv4 or dual-stack networking. Require service hostnames, including `fdb` and `yugabyte`, to resolve only to IPv6 addresses. Read the [YugabyteDB networking contract](docs/reference/deployment/yugabyte-networking.md) before changing service networking.

Use the stable Docker DNS name `yugabyte` for the base database identity, advertised address, and listen address. Do not derive that identity with `getent` or pin a container's global IPv6 address. Preserve stable identities in deployment overrides.

Refresh the patched `yugabyted` overlay after every image change. Fetch the launcher for the new image tag, reapply the required IPv6 port-check patch, and verify on QA before production. Follow [Update the YugabyteDB image](docs/runbooks/deployment/update-yugabyte-image.md) for the required image, overlay, and restore checks.

Read [YugabyteDB port checks](docs/explanation/deployment/yugabyte-overlay.md) before changing the launcher's address-family selection.

## Tests and provisioning

Run tests in QA or Docker containers. Run production tests only with explicit operator authorization. Follow [Run tests](docs/runbooks/run-tests.md) for container test selection, engine cleanup, and required QA data-generator runs.

Treat QA as disposable. Destroy and recreate it as needed to match production, using vault-sourced secrets. Validate every migration, seed, backfill, and restore on QA before production.

Follow [Provision an environment](docs/runbooks/deployment/provision-environment.md) for a fresh guest, QA recreation, or recovery. Require a verified empty FoundationDB cluster before initial configuration. A failed status query does not prove emptiness. Never run `configure new` against an existing cluster or pass `--allow-fdb-init` on production.

Preserve this provisioning order:

1. Check or initialize FoundationDB.
2. Apply migrations.
3. Prepare audit roles.
4. Start continuous FoundationDB backup when configured.
5. Seed product data.

Require pending migrations, including migration 011, before starting app or consumer images that require them. Run migrations through `./server migrate` or the ordered provisioning command, never during HTTP startup. An image-only release does not apply migrations.

Treat `seed_deferred` as incomplete and rerun provisioning after the app starts.

Complete the provisioning runbook's direct initial seed procedure when an environment needs initial data but lacks seed settings.

Keep normal provisioning and migration credentials on the nonsuperuser `tack_migrator` role that owns the schema. Use the engine superuser only for a from-empty bootstrap, supply that credential only for the run, and keep it out of the rendered environment file. Permit database writes only through a migration or reviewed ops command. Preserve the audit and partman schema guard.

Keep ledger reader and redactor connections out of the running application and MCP tools. Use the host commands `audit query`, `audit get`, `audit export`, and `audit redact-actor` with their reader or redactor connections. Run audit queue commands through `audit-ops`, which has the ledger reader and broker connection. Use `tack-ops` for the recorded `ops db sql` diagnostic command. Require its reason, delivered alarm mail, recorded intent, and single statement in a read-only session. Do not use the app's `tack_app` login to read the ledger.

## Recovery

Require quorum replication across fault domains to preserve committed data after a node or zone failure. Use point-in-time recovery and backups for corruption or accidental deletion that replication also copies. Treat object storage as a backup destination rather than a substitute for replication. Preserve `audit.chain_heads` during audit recovery to continue each hash chain. Follow [Recover stores](docs/runbooks/recovery/restore-data.md) for the recovery procedure.

## Implementation requirements

Build with `make build`. Use the go-makefile pipeline and its baseline checks for vet, golangci, staticcheck-extra, and govulncheck. Do not call `go build` directly. Run the repository's canonical lint and quality checks for Go implementations.

Do not invoke command-line tools through `os/exec` or add shell scripts to Tack Go implementations. Run engine tools such as `fdbbackup`, `yb-admin`, `ysql_dump`, and `tar` inside one-shot containers through the Docker Go SDK in [dockerctl.go](internal/ops/dockerctl.go). Do not create or use non-Go scripts, including temporary or standalone helpers, without explicit operator approval.

Record every `./server ops` command through the shared `clispec` audit entry point. Do not weaken or bypass recording for a command. Keep operator identity replaceable through `audit.OperatorIdentitySource` and follow the [operator identity contract](docs/reference/audit/operator-identity.md).

Add `ops qa datagen` coverage in the same change for every new node type, property type, relationship shape, audit verb, or user-visible code path. Run the generator in a one-shot `app` container that can connect to FoundationDB. Supply its required audit credentials to that command without adding reader connections to the running application. Require `TACK_DATAGEN_ALLOW_TARGET` to be exactly `qa` or `local` for writes. Set it on QA and leave it empty in production. Keep dry run as the default without requiring the write marker.

Declare `Lifetime: clispec.Lifetime{Ticket: "TACK-nnn", RemoveBy: time.Date(...)}` for every one-time command. Register it under `backfill` with the `once-` prefix and require `make build` to fail after its removal day. Declare `clispec.Permanent` for every other command. Keep seed sample-data reads inside `internal/service`.

Load server configuration from environment variables through `caarlos0/env`. Do not add TOML, YAML, JSON, or another file-based configuration loader. Preserve existing secret-file references without introducing a configuration format.

Use `log/slog`. Initialize the global logger with `telemetry.Setup` and obtain the context logger with `telemetry.L(ctx)` in handlers. Log significant entity lifecycle events, background jobs, authentication failures, startup, and shutdown. Use named `slog.Attr` fields rather than positional fields and use `noun.verb` messages such as `issue.created` or `worker.started`. Use only these logging levels: `Info` for normal events, `Debug` for trace details, and `Error` for actual failures.

Limit each file to 200 lines and split it by responsibility when necessary. Use one conversion file per entity. Separate bulk operations from basic create, read, update, and delete operations. Prefer responsibility-based filenames; reserve `utils.go` and `helpers.go` for genuinely shared code.

Use complete domain terms, such as `workspaceID` rather than `wsID`. Include context and the relevant identifier in every error, as in `fmt.Errorf("get issue %s: %w", id, err)`. Add a `// Package X` comment to every package and document every exported type and non-obvious field.

Do not add error handling for impossible conditions, compatibility shims, unused exports, re-exports, or unrequested features. Do not add comments to unchanged code. Do not copy current values from owning sources into this contract.

## Auditability gate

Require complete audit evidence before merging or releasing any new or changed operation that processes user data, including reads, lists, searches, exports, creates, updates, and deletes. Apply the gate to public APIs, generated tools, operator commands, background jobs, provider hooks, storage writers, custom node types, and relationships.

Follow the [complete audit recording contract](docs/reference/audit/2026-10-21-audit-completeness-design.md) through shared operation definitions, transactional outboxes, and the permanent ledger. Keep `audit.events` as the single ledger table and `audit-consumer` as its single writer. Continue each `(org, shard)` hash chain through `audit.chain_heads`. Do not rename or archive the table, or introduce `events_v2`. Read the [Kafka audit design](docs/explanation/audit/audit-kafka-cutover.md) before changing that contract.

1. Record the verified actor or explicit unauthenticated identity, operation, complete targets, original time, committed order, and outcome. Preserve the initiating identity during background work, retries, and recovery. Record refusals, failures, partial work, and interruptions.
2. Record actual old and new values for every changed field and relationship, including removals. Commit change evidence atomically with database changes. Record complete relationship identities and apply protected-value storage and redaction rules.
3. Admit reads durably and finalize evidence before returning data, including each streaming page or chunk. Record returned identities, result counts, selection criteria, and payload fingerprints through the shared read contract. A fingerprint cannot reconstruct returned content.
4. Refuse an operation when required identity or recording fails. Do not release read results or commit changes without required audit evidence. Record external-operation intent before effects and verify actual state during recovery.
5. Add regression coverage through the affected public operation with real dependencies. Inspect permanent audit records for the actual actor, targets, outcomes, change values, read manifests, and fingerprints. Exercise authorization refusals and recording failures. Declarations and log messages do not prove coverage.
6. Include audit evidence and test results in the PR. Require `runtime audit coverage` and applicable integration checks before merge. Verify accepted behavior on QA before a production release.

Do not add recording opt-outs, exclude operations from coverage, weaken audit assertions, bypass required checks, or defer recording to another ticket. Treat an unaudited implementation as a defect. A healthy deployment or unrelated passing test does not satisfy this gate.
