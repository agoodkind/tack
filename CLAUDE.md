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

**SQL does one thing: auth.**

```
users        : identity (email, display_name)
api_tokens   : bearer token → user_id
org_members  : auth gate: is this user allowed in this org
```

Nothing else lives in SQL. No entity tables, no config tables, no join tables.

**FoundationDB owns all data:**
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

- **MCP Streamable HTTP** (`/mcp`): primary interface. 50+ tools, dynamic per-user tool registration based on NodeType slugs. workspace_slug and project_identifier everywhere; no raw UUIDs from callers. orgID never appears as an input field.
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
- **SQL = auth only.** `users`, `api_tokens`, `org_members`. Nothing else.
- **orgID never leaks to callers.** Derived from entity resolution or workspace lookup internally. Never a service method parameter, never an API input field.
- **NodeListView is the single read layer.** Service layer uses NodeReader for all reads. No direct EntityRepository reads in service or handler code.
- **One FDB transaction per write.** CreateAtomic batches everything. No multi-step create sequences.
- **UUIDv7 for all new entities.** k-sortable, creation-order range scans without coordination.
- **Descriptions are Markdown TEXT**: not HTML, no stripped copy.
- **Updates are partial everywhere**: only provided fields change.
- **Migrations run via `./server migrate` only**: never on HTTP startup.
- **Optional MCP input fields use `*string` with `json:",omitempty"`**: required by `google/jsonschema-go`.
- **`jsonschema:"..."` tag values are descriptions**: not constraints. Do not use them for validation.
- `gen_random_uuid()` requires `CREATE EXTENSION pgcrypto` on YugabyteDB (run manually before first migration).
- YugabyteDB does not support `GENERATED ALWAYS AS STORED` columns; no tsvector columns.

---

## SQL schema (auth only)

```
users          identity: email, display_name, avatar_url
api_tokens     auth: token_hash → user_id
org_members    auth gate: org_id, user_id, role
```

That is the complete SQL surface.

---

## FDB key space (canonical reference)

All keys use the tuple layer. `orgID` is always an early component for tenant locality.

### Resolution (global, not org-scoped)
```
node_resolve          nodeID → {OrgID, WorkspaceID, ProjectID, NodeType}
```

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
- IPv6-only network with NAT64 gateway for external access
- Services: YugabyteDB (port 5433), FoundationDB, Meilisearch, Temporal -- all via docker-compose
- App runs as Docker container (`tack-server:latest`), logs via `docker logs tack-app-1`
- Deploy: `make deploy` (rsync + docker build --network host + restart)
- Seed (after deploy or to propagate type changes):
  ```bash
  ssh tack 'cd /root/tack && docker compose exec -e SEED_EMAIL=alex@goodkind.io -e SEED_NAME=Alexander -e SEED_ORG_SLUG=goodkind-io -e SEED_ORG_NAME=goodkind.io -e SEED_WORKSPACE_SLUG=main -e SEED_WORKSPACE_NAME=Main app /server seed'
  ```
- Org slug is `goodkind-io`, org name is `goodkind.io` (not the org name). Seed is idempotent and always re-runs SeedOrg + SeedWorkspace to propagate type/feature changes.

---

## What good looks like

- Multi-tenant from the start. Org is the tenancy root.
- Every read goes through NodeReader. Every write goes through EntityRepository. No repos called directly from handlers.
- Errors are typed (`domain.ErrNotFound`, `domain.ErrUnauthenticated`). No raw string errors leaking to clients.
- Logging uses `telemetry.L(ctx)` so every log line carries the request ID.
- orgID never appears as a parameter in service methods or API inputs.
- **No hardcoded type names in runtime code.** Exported NodeType constants do not exist. Seed uses unexported constants. Runtime code reads type keys from FDB-loaded NodeType data.
- **Features are `[]string`** on NodeType. User-extensible without code changes.
- **Hierarchy is a DAG** defined by CanLiveUnder. The Resolver walks the scope chain for N levels. No depth assumptions.
- **MCP tool names, parameter names, and describe responses** are all derived from type slugs and CanContain at registration time.
- **Universal fields test:** a field belongs on NodeValue/NodeListView only if it would exist on a node in a completely different product (CMS, CRM, game engine) built on the same architecture. Universal: `ID`, `OrgID` (tenant isolation), `NodeType`, `Name`, `CreatedAt`, `UpdatedAt`, `CreatedBy`, `UpdatedBy`, `Props`. Everything else (including `WorkspaceID`, `ProjectID`, `StateID`, `SequenceID`, `AssigneeIDs`, etc.) is a property in Props, a scope position in NodeResolve, or a generic relationship.
- No tech debt disguised as "we can fix this later for scale." If it won't scale, don't ship it.
