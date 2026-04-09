# Tack

Tack is a fully-featured, horizontally scalable project management platform. It is not a personal tool or a prototype. Every decision should be made with production scale, multi-tenant correctness, and long-term maintainability in mind.

## What we are building

A complete replacement for Plane CE / Linear / Jira — with a better architecture. The product must support:

- Multiple orgs, each with multiple workspaces, teams, and users
- User-defined hierarchy: the type system is extensible, not fixed
- Horizontal scalability from day 0 — no single points of write contention
- MCP as a first-class interface, not a demo or convenience layer
- An eventual Connect-RPC API, TypeScript frontend, and TUI

Do not frame suggestions, gaps, or decisions in terms of "your use case" or "for now since it's just you." Every feature should be designed for a real multi-tenant product with real users.

## Architecture

**Data layer (hybrid):**
- **YugabyteDB** — structural PM entities (orgs, workspaces, projects, issues, epics, etc.). Postgres-compatible, automatic transparent sharding. `gen_random_uuid()` requires `CREATE EXTENSION pgcrypto` on YugabyteDB.
- **FoundationDB** — all relationships, extensibility, and high-frequency data. No SQL join tables. All many-to-many relationships live in FDB with forward + reverse indexes, written atomically.
- **Meilisearch** — search. Currently stubbed (Noop); real indexing on write is the next step.

**Data access philosophy:**
- Databases stay dumb. No complex SQL joins or query planner magic.
- Multi-source reads are explicit and concurrent using `errgroup`:
  ```go
  g.Go(func() error { issue, err = issueRepo.GetByID(ctx, id); return err })
  g.Go(func() error { assignees, err = fdb.AssignmentsOnNode(ctx, orgID, id); return err })
  g.Wait()
  ```
- SQL join tables are forbidden. Every bidirectional relationship lives in FDB with both directions indexed.
- SQL answers: "give me entities matching these structured filters, sorted."
- FDB answers: "who is assigned to X?", "what is user Y assigned to?", "what changed on Z?"

**Why no SQL join tables:**
YugabyteDB shards data by primary key. A join table `issue_assignees(issue_id, assignee_id)` answers "who is assigned to issue X?" fast, but "all issues assigned to user Y across all projects" requires a scatter-gather across every shard. FDB stores both directions contiguously — each is a single range scan.

**API layer:**
- MCP Streamable HTTP (`/mcp`) — current primary interface. Token resolves user → workspaces → custom node types → tool set.
- Connect-RPC — planned Phase 2.

**Auth:**
- `Authorization: Bearer <token>` → SHA-256 hash → `api_tokens` lookup.
- Dev mode (`ENV=development`): Bearer token is the raw user UUID (no DB lookup).
- SSO is planned; the token table is the stable abstraction.

**Build:**
- `go build ./...` — works everywhere (FDB is noop stub).
- `CGO_ENABLED=1 go build -tags fdb ./...` — production build with real FDB. Requires `foundationdb-clients` 7.4.x installed.
- FDB Go bindings pinned to `v0.0.0-20250923185926-685eda6efef7` (API 740, pre-8.0 bump).

## Key decisions (do not revisit without good reason)

- `created_by NOT NULL` on all entities — a seeded user is required before any data.
- All properties, relationships, and membership live in FDB — no JSONB columns, no SQL join tables.
- Descriptions are Markdown TEXT — not HTML, no stripped copy.
- `node_id UUID` on every core entity — the FDB bridge key.
- Migrations run via `./server migrate` only — never on HTTP startup.
- Goose requires `CREATE EXTENSION pgcrypto` to be run manually before the first migration on YugabyteDB.
- YugabyteDB does not support `GENERATED ALWAYS AS STORED` columns — no tsvector columns.
- Search is Meilisearch, not Postgres FTS.
- Updates are partial everywhere — only provided fields change.
- Optional MCP input fields use `*string` with `json:",omitempty"` — required by `google/jsonschema-go`.
- `jsonschema:"..."` tag values are interpreted as descriptions by the MCP SDK, not as constraints. Do not use them.
- `org_members` stays in SQL — it is the auth gate on every request and is always queried with org_id context.

## SQL schema (what stays)

Pure entity tables only. No join tables. No relational glue.

```
river_jobs        background job queue
orgs              tenancy root
users             identity only (email, name, avatar)
api_tokens        auth
org_members       auth gate — the ONLY SQL membership table
workspaces        core entity
projects          core entity
project_sequences atomic sequence allocator per (project, entityType)
states            workflow state definitions
labels            label definitions
issues            core entity
epics             core entity
modules           core entity
cycles            core entity
```

## FDB key space (canonical reference)

All keys use the tuple layer. `orgID` is always the second component — it scopes all data to a tenant.

### Membership
```
membership_by_user    orgID, userID, entityType, entityID   → {role, added_by, added_at}
membership_by_entity  orgID, entityType, entityID, userID   → {role, added_by, added_at}
membership_by_role    orgID, entityType, entityID, role, userID → nil
invitation            orgID, invitationID                   → {email, role, entity_type, entity_id, invited_by, expires_at}
invitation_by_email   orgID, email, invitationID            → nil
```

### Assignments (replaces SQL issue_assignees, epic_assignees)
```
assignment_on_node    orgID, nodeID, userID                 → {assigned_by, assigned_at}
assignment_to_user    orgID, userID, updatedAtNano, nodeID  → nil
```

### Labels on nodes (replaces SQL issue_labels, epic_labels)
```
label_on_node         orgID, nodeID, labelID                → {added_by, added_at}
issues_with_label     orgID, labelID, nodeID                → nil
```

### Containment (replaces SQL module_issues, cycle_issues)
```
issue_in_module           orgID, moduleID, issueID          → {added_by, added_at}
modules_containing_issue  orgID, issueID, moduleID          → nil
issue_in_cycle            orgID, cycleID, issueID           → {added_by, added_at}
cycles_containing_issue   orgID, issueID, cycleID           → nil
```

### Hierarchy (complements SQL parent_id and epic_id columns)
```
issue_children        orgID, parentIssueID, childIssueID    → nil
epic_children         orgID, parentEpicID, childEpicID      → nil
issues_in_epic        orgID, epicID, issueID                → nil
```

### Custom node instances
```
node_instance             orgID, workspaceID, nodeType, nodeID       → {name, created_by, created_at}
node_instance_by_project  orgID, projectID, nodeType, nodeID         → nil
node_instance_by_state    orgID, workspaceID, nodeType, stateID, nodeID → nil
node_assignee             orgID, nodeID, userID                      → {assigned_by, assigned_at}
node_label                orgID, nodeID, labelID                     → {added_by, added_at}
node_parent               orgID, nodeID                              → parentNodeID
node_children             orgID, parentNodeID, childNodeID           → nil
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
count_on_node         orgID, nodeID, counterName                     → int64
count_by_state        orgID, projectID, stateID                      → int64
reaction_on_node      orgID, nodeID, emoji, userID                   → {created_at}
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
link_on_node          orgID, nodeID, linkID                          → {url, title, link_type, created_by}
attachment_on_node    orgID, nodeID, attachmentID                    → {filename, size_bytes, mime_type, storage_key, uploaded_by}
draft_for_user_on_node orgID, userID, nodeID                         → {body, updated_at}
description_version   orgID, nodeID, savedAtNano, versionID          → {body, saved_by}
```

### Work tracking
```
work_log_on_node      orgID, nodeID, createdAtNano, logID            → {user_id, seconds, note}
work_log_by_user      orgID, userID, date, logID                     → nil
```

### Custom fields
```
property_definition   orgID, [workspaceID, [projectID,]] defID       → PropertyDef
property_value_on_node orgID, nodeID, propertyDefID                  → value
```

### Automation and rules
```
automation_rule       orgID, entityType, entityID, ruleID            → {trigger, actions, enabled}
automation_run_log    orgID, ruleID, ranAtNano, runID                 → {status, error}
transition_rule       orgID, projectID, fromStateID, toStateID       → {allowed, conditions, actions}
```

### Settings and roles
```
user_preference       orgID, userID, preferenceKey                   → value
org_setting           orgID, settingKey                              → value
role_definition       orgID, roleID                                  → {name, description}
role_permission       orgID, roleID, permissionKey                   → bool
```

### Custom type definitions
```
node_type_definition  orgID, typeID                                  → NodeType
sequence              orgID, scopeType, scopeID, nodeType            → int64
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

## Deployment (CT 117)

- LXC container at `3d06:bad:b01::117`, SSH alias `tack` (ProxyJump vault).
- Services: YugabyteDB (port 5433), FoundationDB, Meilisearch — all via docker-compose.
- Binary: `/root/tack/bin/server`, logs: `/var/log/tack.log`.
- Sync: `rsync -av --exclude='.git' --exclude='bin/' ~/Sites/tack/ tack:/root/tack/`

## What good looks like

- Multi-tenant from the start. Org is the tenancy root.
- Every write goes through the service layer. Repos are not called directly from HTTP handlers.
- Errors are typed (`domain.ErrNotFound`, `domain.ErrUnauthenticated`). No raw string errors leaking to clients.
- Logging uses `telemetry.L(ctx)` so every log line carries the request ID.
- No tech debt disguised as "we can fix this later for scale." If it won't scale, don't ship it.
