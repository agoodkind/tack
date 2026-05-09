# Tack

Tack is a fully-featured, horizontally scalable project management platform. It is not a personal tool or a prototype. Every decision should be made with production scale, multi-tenant correctness, and long-term maintainability in mind.

## What we are building

A complete replacement for Plane CE / Linear / Jira: with a better architecture. The product must support:

- Multiple orgs, each with multiple workspaces, teams, and users
- User-defined hierarchy: the type system is extensible, not fixed
- Horizontal scalability from day 0: no single points of write contention
- MCP as a first-class interface, not a demo or convenience layer
- An eventual Connect-RPC API, TypeScript frontend, and TUI

Do not frame suggestions, gaps, or decisions in terms of "your use case" or "for now since it's just you." Every feature should be designed for a real multi-tenant product with real users.

## Architecture

### The model: everything is a node in FDB

Every entity: org, workspace, project, state, label, issue, epic, cycle, module, custom type. Each is a node stored in FoundationDB. Same pattern across all entity types: NodeValue (primary record) + NodeListView (materialized read view) + NodeResolve (global resolution record). Every entity is globally addressable by its UUID alone. No org context required from callers.

**YugabyteDB owns SQL-only control data: auth and compliance audit.**

```
users              : identity (email, display_name)
api_tokens         : bearer token → user_id
org_members        : auth gate: is this user allowed in this org
audit.events       : append-only compliance audit ledger
audit.chain_heads  : per-org/shard hash-chain heads
audit.notarizations: signed Merkle checkpoints
audit.pii          : redactable PII payloads referenced by audit events
```

No product entity lives in SQL. No entity tables, config tables, relationship
tables, or read-model tables belong there. YugabyteDB is still critical data:
auth gates and the compliance audit ledger must be preserved on every recovery.

**FoundationDB owns all product data:**
- All entity storage (orgs, workspaces, projects, states, labels, issues, epics, cycles, modules, custom types)
- All relationships (assignments, containment, labels-on-nodes, hierarchy)
- All views (NodeListView materialized read records)
- All resolution records (NodeResolve: entityID → orgID, workspaceID, nodeType)
- Activity, comments, properties, sequences, automation rules

**Meilisearch:** full-text search. Fully wired: `searcher.Index()` called on every Create and Update. `EnsureIndex` filterable attrs: `org_id`, `workspace_id`, `project_id`, `entity_type`, `state_id`, `priority`, `is_draft`. Search returns facets.

### Data access

Every read goes through `NodeReader`. The service layer never calls `EntityRepository`, `PropertyRepository`, `AssignmentRepository`, or `LabelRepository` directly for reads.

```
NodeReader.Get(ctx, nodeID)           : resolves via NodeResolve record, no org context needed
NodeReader.List(ctx, NodeListQuery)   : parallel chunk fetch, returns []NodeListView
NodeReader.Stream(ctx, NodeListQuery) : unbounded scan, returns channel
NodeReader.Resolve(ctx, entityID)     : returns NodeResolve for any entity type
```

Every write goes through `EntityRepository.CreateAtomic` or `EntityRepository.Set`. These atomically write NodeValue + NodeListView + NodeResolve in a single FDB transaction.

orgID is never passed by callers. It is always derived:
- For entity-scoped ops: `reader.Resolve(ctx, entityID)` → `resolve.OrgID`
- For workspace-scoped ops: workspace entity → `ws.OrgID`
- The storage layer uses orgID internally for key locality. It never surfaces to the API or service layer as a parameter.

### Global entity resolution

Every entity has a resolution record written atomically on create:

```
FDB key: (node_resolve, entityID) → {OrgID, WorkspaceID, ProjectID, NodeType}
```

`NodeReader.Get(ctx, entityID)` and `NodeReader.Resolve(ctx, entityID)` use this record. Callers know one UUID; lookup works or fails based on auth.

Auth check: resolve entity → get OrgID → check `org_members` in SQL → allow or 403.

### NodeListView

Written atomically with every entity write. Contains everything needed to render a list row; no follow-up reads required.

```
FDB key: (node_list_view, orgID, workspaceID, nodeType, nodeID) → JSON NodeListView
```

NodeListView includes: ID, OrgID, WorkspaceID, ProjectID, NodeType, SequenceID, Name, Description, StateID, ParentID, EpicID, AssigneeIDs, LabelIDs, Priority, StartDate, DueDate, IsDraft, Status, CustomProps, CreatedBy, UpdatedBy, CreatedAt, UpdatedAt.

### Write path

All creates use `EntityRepository.CreateAtomic`: single FDB transaction:
1. Sequence allocation (atomic increment on sequence key)
2. NodeValue primary record + secondary indexes
3. Property values + property secondary indexes
4. NodeResolve record
5. NodeListView
6. Initial assignments
7. Initial labels

No cross-database transactions. No consistency gap.

### API layer

- **MCP Streamable HTTP** (`/mcp`): primary interface. 50+ tools, dynamic per-user tool registration based on NodeType reference and display metadata. Human-readable workspace and project inputs are address values declared by metadata. orgID never appears as an input field.
- **Connect-RPC** (`/tack.v1.*`): typed API for future frontend/TUI. Entity-scoped ops take entity UUID only. Collection ops take workspace_id or project_id.

### Auth

- `Authorization: Bearer <token>` → SHA-256 hash → `api_tokens` lookup → `userID`
- Dev mode (`ENV=development`): Bearer token is the raw user UUID (no DB lookup)
- Per-entity auth: resolve entity → orgID → `SELECT 1 FROM org_members WHERE org_id=$1 AND user_id=$2`

### Build

- `go build ./...`: works everywhere (FDB is noop stub, no CGO required)
- `CGO_ENABLED=1 go build -tags fdb ./...`: production build with real FDB
- FDB Go bindings pinned to `v0.0.0-20250923185926-685eda6efef7` (API 740)
- `foundationdb-clients` 7.4.x required on the build host for `-tags fdb`

---

## Key decisions

- **Everything is a node in FDB.** Orgs, workspaces, projects, states, labels, issues: all the same pattern. No entity lives in SQL.
- **SQL = auth plus compliance audit only.** `users`, `api_tokens`, `org_members`, and `audit.*`. Nothing else.
- **orgID never leaks to callers.** Derived from entity resolution or workspace lookup internally. Never a service method parameter, never an API input field.
- **NodeListView is the single read layer.** Service layer uses NodeReader for all reads. No direct EntityRepository reads in service or handler code.
- **One FDB transaction per write.** CreateAtomic batches everything. No multi-step create sequences.
- **UUIDv7 for all new entities.** k-sortable, creation-order range scans without coordination.
- **Human-readable references are declared metadata.** UUID is canonical identity; `NodeType.Reference` and generic address contracts declare caller-facing reference forms.
- **Type, tool, and display names are separate from node identity and address values.** Runtime behavior follows generic metadata and address contracts, including declared type metadata, display tokens, address kinds, address values, and scope.
- **Address lookup storage uses generic address/reference indexes.** Key families and report shapes use address/reference terminology and declare scope, kind, value, and target node explicitly.
- **Descriptions are Markdown TEXT**: not HTML, no stripped copy.
- **Updates are partial everywhere**: only provided fields change.
- **Migrations run via `./server migrate` only**: never on HTTP startup.
- **Optional MCP input fields use `*string` with `json:",omitempty"`**: required by `google/jsonschema-go`.
- **`jsonschema:"..."` tag values are descriptions**: not constraints. Do not use them for validation.
- `gen_random_uuid()` requires `CREATE EXTENSION pgcrypto` on YugabyteDB (run manually before first migration).
- YugabyteDB does not support `GENERATED ALWAYS AS STORED` columns; no tsvector columns.

---

## SQL schema (auth and audit only)

```
users                identity: email, display_name, avatar_url
api_tokens           auth: token_hash → user_id
org_members          auth gate: org_id, user_id, role
audit.events         append-only audit events, partitioned by event_time
audit.chain_heads    current hash-chain head per (org_id, shard)
audit.notarizations  signed Merkle roots over shard heads
audit.pii            redactable encrypted PII payloads
```

That is the complete SQL surface. Treat YugabyteDB backups as compliance
artifacts, not just convenience auth snapshots.

---

## Deprecated names in older artifacts

Some historical reports under the repo refer to a `stray_alias_state` repair
class and an `internal/ops/repair_stray_alias_state.go` file. Neither exists
in the current code base. The repair tooling has been refactored into three
generic classes registered in `internal/ops/repair_catalog.go`:

- `reference_property`: repair one UUID reference property from operator-declared source fields and policies. Subsumes the resolvable-raw-alias and conflict-winner cases that `stray_alias_state` previously handled (for example normalizing `done` / `todo` / scoped values like `CLYDE::Done` into a canonical `state_id`).
- `parent_reference`: repair a node `parent_id` and `child_of` edge from operator-declared source fields.
- `props_transform`: apply generic property delete, rename, and append-preserve transforms. Subsumes the "remove the stale raw `state` alias when canonical `state_id` is already valid" cleanup that `stray_alias_state` previously handled.

The state repair documented in
`state_audit_full_impact.md` (dated 2026-05-05) names `stray_alias_state` and
`repair_stray_alias_state.go` because both existed when that audit was
written. That document is left as-is as a historical artifact; the
2026-05-09 execution report at
`incident_2026-05-09_seed_parallel_org/state_repair_execution_report.md`
records the same work in the current toolchain's terminology.

---

## FDB key space (canonical reference)

All keys use the tuple layer. `orgID` is always an early component for tenant locality.

### Resolution (global, not org-scoped)
```
node_resolve          nodeID → {OrgID, WorkspaceID, ProjectID, NodeType}
```

### Address/reference indexes
```
address_index         nodeType, addressKind, address → nodeID
```

The `address_index` key family is global, not org-scoped. The current
implementation in `internal/adapters/foundationdb/keys.go` packs
`(address_index, nodeType, addressKind, address)` and stores the target
`nodeID` bytes; there is no `orgID` or `scopeID` component in the key. There
is no reverse `node_address_by_node` index in the current code base.

Note: the global-vs-scoped design of the address index and the absence of a
reverse index are open questions tracked separately. See the 2026-05-09
incident retro at
`incident_2026-05-09_seed_parallel_org/retro_log.md` section 1B for the
tradeoffs and required follow-ups.

### Materialized views
```
node_list_view        orgID, workspaceID, nodeType, nodeID → JSON NodeListView
```

### Entity storage
```
node_instance             orgID, workspaceID, nodeType, nodeID       → NodeValue JSON
node_instance_by_project  orgID, projectID, nodeType, nodeID         → nil
node_instance_by_state    orgID, workspaceID, nodeType, stateID, nodeID → nil
node_by_property          orgID, workspaceID, nodeType, propDefID, encodedValue, nodeID → nil
node_by_sequence          orgID, projectID, nodeType, sequenceID     → nodeID
```

### Sequences
```
sequence              orgID, scopeType, scopeID, nodeType            → int64 (atomic counter)
```

### Assignments
```
assignment_on_node    orgID, nodeID, userID                 → {assigned_by, assigned_at}
assignment_to_user    orgID, userID, updatedAtNano, nodeID  → nil
```

### Labels on nodes
```
label_on_node         orgID, nodeID, labelID                → {added_by, added_at}
issues_with_label     orgID, labelID, nodeID                → nil
```

### Containment
```
issue_in_module           orgID, moduleID, issueID          → {added_by, added_at}
modules_containing_issue  orgID, issueID, moduleID          → nil
issue_in_cycle            orgID, cycleID, issueID           → {added_by, added_at}
cycles_containing_issue   orgID, issueID, cycleID           → nil
```

### Hierarchy
```
issue_children        orgID, parentIssueID, childIssueID    → nil
epic_children         orgID, parentEpicID, childEpicID      → nil
issues_in_epic        orgID, epicID, issueID                → nil
issue_epic_reverse    orgID, issueID                        → epicID
```

### Relations between nodes
```
relation_from_node    orgID, sourceNodeID, relationType, targetNodeID → {created_by, created_at}
relation_to_node      orgID, targetNodeID, relationType, sourceNodeID → nil
```
relationType: blocks, blocked_by, duplicate_of, relates_to, cloned_from, split_from

### Comments
```
comment_on_node       orgID, nodeID, createdAtNano, commentID        → {body, author_id, edited_at}
reply_to_comment      orgID, parentCommentID, createdAtNano, replyID → {body, author_id, edited_at}
reaction_on_comment   orgID, commentID, emoji, userID                → {created_at}
```

### Activity log
```
activity_on_node      orgID, nodeID, createdAtNano, eventID          → {verb, field, old_value, new_value, actor_id}
activity_by_user      orgID, userID, createdAtNano, eventID          → nil
activity_on_workspace orgID, workspaceID, createdAtNano, eventID     → nil
```

### Membership
```
membership_by_user    orgID, userID, entityType, entityID   → {role, added_by, added_at}
membership_by_entity  orgID, entityType, entityID, userID   → {role, added_by, added_at}
membership_by_role    orgID, entityType, entityID, role, userID → nil
invitation            orgID, invitationID                   → {email, role, entity_type, entity_id, invited_by, expires_at}
invitation_by_email   orgID, email, invitationID            → nil
```

### Watchers and mentions
```
watcher_of_node       orgID, nodeID, userID                          → {level}
node_watched_by_user  orgID, userID, nodeID                          → nil
mention_in_node       orgID, nodeID, mentionedUserID, contextID      → {context_type, snippet}
mention_of_user       orgID, mentionedUserID, createdAtNano, contextID → nil
```

### Notifications
```
notification_for_user       orgID, userID, createdAtNano, notifID    → {type, actor_id, entity_type, entity_id, summary, read_at}
unread_notification_count   orgID, userID                            → int64
```

### Counters (atomic, maintained on every write)
```
count_on_node          orgID, nodeID, counterName                    → int64
count_by_state         orgID, projectID, stateID                     → int64
reaction_on_node       orgID, nodeID, emoji, userID                  → {created_at}
reaction_count_on_node orgID, nodeID, emoji                          → int64
```
counterName: comments, sub_issues, attachments, reactions, blockers, work_logs

### Positioning and views
```
sort_position_in_view       orgID, viewType, viewID, nodeID          → float64
board_layout_for_user       orgID, userID, projectID                 → {column_order, hidden_columns, group_by}
starred_by_user             orgID, userID, entityType, entityID      → {starred_at}
saved_view_for_user         orgID, userID, viewID                    → {name, filters, sort, group_by}
saved_view_on_entity        orgID, entityType, entityID, viewID      → {name, filters, ...}
```

### Content
```
link_on_node           orgID, nodeID, linkID                         → {url, title, link_type, created_by}
attachment_on_node     orgID, nodeID, attachmentID                   → {filename, size_bytes, mime_type, storage_key, uploaded_by}
draft_for_user_on_node orgID, userID, nodeID                         → {body, updated_at}
description_version    orgID, nodeID, savedAtNano, versionID         → {body, saved_by}
```

### Work tracking
```
work_log_on_node      orgID, nodeID, createdAtNano, logID            → {user_id, seconds, note}
work_log_by_user      orgID, userID, date, logID                     → nil
```

### Custom fields
```
property_definition    orgID, [workspaceID, [projectID,]] defID      → PropertyDef
property_value_on_node orgID, nodeID, propertyDefID                  → value
```

### Type definitions
```
node_type_definition  orgID, typeID                                  → NodeType
```

### Automation and rules
```
automation_rule       orgID, entityType, entityID, ruleID            → {trigger, actions, enabled}
automation_run_log    orgID, ruleID, ranAtNano, runID                → {status, error}
transition_rule       orgID, projectID, fromStateID, toStateID       → {allowed, conditions, actions}
```

### Settings and roles
```
user_preference       orgID, userID, preferenceKey                   → value
org_setting           orgID, settingKey                              → value
role_definition       orgID, roleID                                  → {name, description}
role_permission       orgID, roleID, permissionKey                   → bool
```

### Integrations and ops
```
webhook               orgID, webhookID                               → {url, secret, events, enabled}
webhook_delivery      orgID, webhookID, deliveredAtNano, deliveryID  → {status, response_code}
search_sync_state     orgID, entityType, entityID                    → {last_indexed_at, checksum}
search_sync_queue     orgID, entityType, entityID                    → {queued_at}
audit_log             orgID, createdAtNano, auditID                  → {actor_id, action, target_type, target_id, before, after}
audit_log_by_actor    orgID, actorID, createdAtNano, auditID         → nil
presence_on_node      orgID, nodeID, userID                          → {last_seen_at}
```

---

## Deployment (CT 117)

- LXC container at `3d06:bad:b01::117`, SSH alias `tack` (ProxyJump vault)
- IPv6-only host with NAT64 gateway for external IPv4 reach
- Services: YugabyteDB (port 5433), FoundationDB, Meilisearch, Temporal. All via docker-compose.
- App runs as Docker container (`tack-server:latest`), logs via `docker logs tack-app-1`
- Deploy: `make deploy` (rsync + docker build --network host + restart)

### Container networking: IPv6-only with GUA via NDP proxy

The docker-compose `default` network is IPv6-only. Containers get real Global Unicast Addresses out of `3d06:bad:b01:0:7ac::/96`. That sub-prefix is carved out of the host's on-link `3d06:bad:b01::/64`. CT 117 NDP-proxies the sub-prefix back onto eth0. The rest of the LAN sees container addresses as if they were on the wire.

Inter-container traffic is IPv6-only. The embedded Docker DNS returns AAAA only because the bridge has `enable_ipv4: false`. Service-to-service hostnames (`fdb`, `yugabyte`, `meilisearch`, `temporal`) resolve to v6.

Pieces of the contract:
- `/etc/sysctl.d/99-tack-ipv6.conf`: `net.ipv6.conf.all.forwarding=1`, `net.ipv6.conf.eth0.proxy_ndp=1`
- `ndppd` running with `rule 3d06:bad:b01:0:7ac::/96 { auto }` on eth0
- `/etc/docker/daemon.json`: `{"ipv6": true, "ip6tables": true}`
- `docker-compose.yml` `networks.default` block with `enable_ipv4: false`, `enable_ipv6: true`, `gateway_mode_v6: routed`, subnet `3d06:bad:b01:0:7ac::/96`, gateway `3d06:bad:b01:0:7ac::1`
- No OPNsense static route. The /64 is on-link, so NDP proxy on CT 117 is enough.

**Do NOT** flip the bridge back to IPv4 or dual-stack. The whole point is forced-v6 inter-container traffic. Adding v4 lets services silently fall back.

### YugabyteDB identity and data contract

YugabyteDB runs as the `yugabyte` Compose service on the IPv6-only bridge. Its
network packets still use a real GUA from `3d06:bad:b01:0:7ac::/96`, but its
database identity must be the stable Docker DNS name `yugabyte`, not the
container's current IPv6 address.

The `yugabyted` command in `docker-compose.yml` must advertise and listen on
`yugabyte`:

```
--advertise_address=yugabyte
--listen=yugabyte
```

Do not derive `--advertise_address` from `getent ahostsv6 $(hostname)`, and do
not pin normal operation to a literal container GUA. Docker Compose service IPs
are replaceable; the service DNS name is the stable identity. Literal GUAs can
be persisted into Yugabyte master and tserver metadata, and stale persisted
addresses can wedge YSQL even while processes appear to be running.

YugabyteDB stores:
- Auth tables: `users`, `api_tokens`, `org_members`
- Compliance audit tables: `audit.events`, `audit.chain_heads`,
  `audit.notarizations`, `audit.pii`

Backups must include the live Yugabyte volume and logical CSV dumps of auth
tables. Any recovery that replaces `tack_yugabyte-data` must also preserve and
restore or merge `audit.*`; restoring only auth is data loss. During audit
recovery, preserve `audit.chain_heads` so future audit writes continue from the
canonical hash-chain heads.

- Seed (after deploy or to propagate type changes):
  ```bash
  ssh tack 'cd /root/tack && docker compose exec -e SEED_EMAIL=alex@goodkind.io -e SEED_NAME=Alexander -e SEED_ORG_SLUG=goodkind-io -e SEED_ORG_NAME=goodkind.io -e SEED_WORKSPACE_SLUG=main -e SEED_WORKSPACE_NAME=Main app /server seed'
  ```
- Seed is idempotent and always re-runs SeedOrg + SeedWorkspace to propagate type/feature changes.

---

## What good looks like

- Multi-tenant from the start. Org is the tenancy root.
- Every read goes through NodeReader. Every write goes through EntityRepository. No repos called directly from handlers.
- Errors are typed (`domain.ErrNotFound`, `domain.ErrUnauthenticated`). No raw string errors leaking to clients.
- Logging uses `telemetry.L(ctx)` so every log line carries the request ID.
- orgID never appears as a parameter in service methods or API inputs.
- **Runtime type behavior is data-driven.** Seed uses unexported bootstrap constants. Runtime code reads type keys from FDB-loaded NodeType data.
- **Features are `[]string`** on NodeType. User-extensible without code changes.
- **Human-readable references are metadata-declared.** `NodeType.Reference` and generic address contracts describe caller-facing reference forms, while UUID remains canonical identity.
- **Everything remains a node.** Human-readable references and addresses attach to node identity through metadata, and node records plus generic indexes preserve storage ownership.
- **Tool/type display naming is separate from node identity and address values.** MCP tool names, parameter names, and describe responses come from type display/command metadata and CanContain; address values remain caller-facing node references.
- **Hierarchy is a DAG** defined by CanLiveUnder. The Resolver walks the scope chain for N levels. No depth assumptions.
- **Universal fields test:** a field belongs on NodeValue/NodeListView only if it would exist on a node in a completely different product (CMS, CRM, game engine) built on the same architecture. Universal: `ID`, `OrgID` (tenant isolation), `NodeType`, `Name`, `CreatedAt`, `UpdatedAt`, `CreatedBy`, `UpdatedBy`, `Props`. Everything else (including `WorkspaceID`, `ProjectID`, `StateID`, `SequenceID`, `AssigneeIDs`, etc.) is a property in Props, a scope position in NodeResolve, or a generic relationship.
- No tech debt disguised as "we can fix this later for scale." If it won't scale, don't ship it.

---

## No TOML. No Config Files.

Server configuration is environment variables only. There is no config file.
Do not introduce TOML, YAML, JSON, or any file-based config loading.
The `caarlos0/env` library is the correct and only config mechanism for the Go server.

## Logging

Use `log/slog` throughout. The global logger is initialized by `telemetry.Setup`.
Inside request handlers and services, retrieve the context logger via `telemetry.L(ctx)`.

Every significant event must be logged. "Significant" means:

- Entity created, updated, deleted, moved (service layer, `issue.created`, `issue.moved`, etc.)
- Background jobs scheduled or completed
- Auth failures
- Startup and shutdown

Use named `slog.Attr` fields, never positional:

```go
// Correct
telemetry.L(ctx).Info("issue.created",
    slog.String("issue_id", created.ID.String()),
    slog.String("project_id", i.ProjectID.String()),
    slog.Int("sequence_id", int(created.SequenceID)),
)

// Wrong
slog.Info("issue created", "id", created.ID)
```

Three levels, used strictly:

- `Info`: normal flow events (entity lifecycle, startup, shutdown, request summary)
- `Debug`: trace-level detail (individual SQL queries via QueryTracer, FDB key writes)
- `Error`: actual failures (auth rejected, repo error, initialization failure)

Log message names use `noun.verb` dot notation: `issue.created`, `hook.blocked`, `worker.started`.

## XDG Base Directories

XDG applies to file paths (log files), never to configuration values.

The Go server's `LOG_FILE` env var can be pointed at any path. The systemd unit and
docker-compose use explicit paths. There is no XDG resolution server-side.

## File Size and Concern Separation

No file should exceed 200 lines. If it does, split it.

Split by concern, not by accident:

- One file per entity for conversion functions: `convert_issue.go`, `convert_project.go`, etc.
- Bulk operations in a separate file from CRUD: `issue_bulk.go`, `issue_handler_bulk.go`
- One file per logical grouping in MCP tools: `issue.go` (CRUD), `issue_bulk.go`, `issue_move.go`

Name files after their responsibility. A file named `utils.go` or `helpers.go` is a last resort,
only for genuinely shared code with no better home.

## Readability Over Conciseness

### Variable names

Use the full domain term, not abbreviations:

```go
// Correct
workspaceID, projectID, issueID

// Wrong
wsID, pID, iID
```

### Error messages

Every error must include context about what failed and the relevant identifier:

```go
// Correct
fmt.Errorf("get issue %s: %w", id, err)
fmt.Errorf("create issue in project %s: %w", i.ProjectID, err)
fmt.Errorf("bulk delete %d issues: %w", len(issueIDs), err)

// Wrong
return nil, err
```

### Package doc comments

Every package must have a `// Package X ...` doc comment explaining:
1. What the package does in one sentence
2. Any design decision that is not obvious from the code

```go
// Package service implements business logic for all Tack entities.
// It coordinates SQL repositories and FoundationDB stores, using errgroup
// for concurrent multi-source reads.
package service
```

### Type and field comments

Every exported type and non-obvious field must have a doc comment:

```go
// BulkUpdatePatch describes a set of changes to apply atomically to multiple issues.
// Fields with nil pointer values are left unchanged. AssigneeIDs replaces the full
// assignee set when non-nil; an empty slice clears all assignees.
type BulkUpdatePatch struct {
    IssueIDs  []uuid.UUID
    ProjectID uuid.UUID
    // StateID replaces the state on all matched issues when non-nil.
    StateID *uuid.UUID
    // SetEpicID must be true to apply an EpicID change (distinguishes nil-to-clear from not-set).
    SetEpicID bool
    EpicID    *uuid.UUID
}
```

## What Not To Do

- Do not add TOML, YAML, or any config file format.
- Do not add error handling for scenarios that provably cannot happen.
- Do not add backwards-compatibility shims, unused exports, or re-exports.
- Do not add docstrings or comments to code you did not change.
- Do not add features not explicitly requested.
- Do not use `utils.go` or `helpers.go` as a dumping ground.
- Do not run `go build ./...` from a directory that is not the module root.
  The correct command is: `bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/server/"`
