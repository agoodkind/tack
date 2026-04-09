# Tack — Living Plan

A flexible, horizontally scalable PM platform. Familiar enough for Linear/Jira users, more powerful underneath.

---

## Phase 1 — Foundation ✅ COMPLETE

Everything needed to use tack as a PM tool via MCP.

### Data layer
- [x] YugabyteDB schema — orgs, workspaces, projects, states, labels, issues, epics, modules, cycles
- [x] No SQL join tables — all many-to-many relationships in FDB with forward + reverse indexes
- [x] FoundationDB adapter — assignment, label, containment, membership, activity, property, node type stores
- [x] Meilisearch running (stub in service layer — ILIKE fallback active)
- [x] Single squashed migration (001_schema.sql) — no migration ladder
- [x] FDB cluster file via bind mount — stable across container restarts

### Auth
- [x] Bearer token → SHA-256 hash → `api_tokens` table
- [x] Dev mode: Bearer = raw user UUID
- [x] `./server seed` — bootstraps user, org, workspace, API token
- [x] `./server migrate` — runs goose, never on HTTP startup

### MCP interface
- [x] Streamable HTTP transport (`/mcp`) — stateless, official go-sdk
- [x] Token resolves user → workspaces → custom node types → 48 tools
- [x] Per-user server cache (60s TTL)
- [x] Dynamic tools per custom node type (AllowedOps driven)
- [x] Full tool surface: workspaces, projects, states, labels, issues, epics, cycles, modules, properties, activity, search

### Product behaviour
- [x] `tack_create_project` seeds Backlog/Todo/In Progress/Done/Canceled states automatically
- [x] Issue assignees persist in FDB, returned on `tack_get_issue`
- [x] Issue labels persist in FDB
- [x] Partial updates everywhere — only provided fields change
- [x] `tack_list_workspaces` — resolves from auth token via org membership
- [x] `tack_describe_workspace` — full introspection entry point

### Observability
- [x] Structured JSON logging (slog), log rotation (lumberjack)
- [x] HTTP request middleware — request_id, method, path, status, latency
- [x] pgx query tracer — every SQL query logged at debug level

---

## Phase 1.5 — Import & Polish 🔄 IN PROGRESS

Make the product import-ready and production-grade before building the API layer.

### Plane importer
- [ ] `./server import --plane-dsn postgres://...` subcommand
- [ ] Map: workspace → workspace (under seeded org)
- [ ] Map: project → project (with states, labels)
- [ ] Map: members → users + org_members
- [ ] Map: issues → issues (assignees/labels via FDB)
- [ ] Map: epics, modules, cycles
- [ ] HTML description → Markdown conversion
- [ ] Dependency order: users → org → workspace → projects → states → labels → issues

### Missing product gaps
- [ ] `tack_list_issues` populate assignees/labels (currently omitted for performance — needs batch FDB read)
- [ ] Comments (FDB `comment_on_node` key space exists, no tool yet)
- [ ] Relations between issues (`relation_from_node` / `relation_to_node` key space exists, no tool yet)
- [ ] Sub-issues (`parent_id` column exists, `issue_children` FDB key space exists, no tool yet)
- [ ] Default state templates (currently hardcoded 5 states — replace with org-level templates)
- [ ] Wire Meilisearch into service layer on write + backfill

### Deployment hardening
- [ ] `docker-compose.yml` — add `restart: on-failure` for the server binary itself (currently run manually)
- [ ] Systemd unit file for the server binary
- [ ] FDB `configure new single memory` on first boot (currently manual)
- [ ] `./server seed` idempotency (already done — verify)

---

## Phase 2 — Connect-RPC API

Programmatic access beyond MCP. Frontend and integrations depend on this.

- [ ] Define protobuf service definitions for all entities
- [ ] Connect-RPC handler layer (HTTP/JSON + gRPC from same handlers)
- [ ] Auth middleware shared with MCP layer
- [ ] Pagination cursors on list endpoints
- [ ] Webhook delivery (FDB `webhook` / `webhook_delivery` key space exists)
- [ ] GitHub integration — link PRs to issues, auto-close on merge

---

## Phase 3 — Frontend

TypeScript + connect-es (Connect-RPC browser client).

- [ ] Workspace / project list
- [ ] Issue board (kanban) — drag-and-drop sort order via FDB `sort_position_in_view`
- [ ] Issue detail — comments, activity, relations, properties
- [ ] Custom node type views (Campaigns, Incidents, etc.)
- [ ] Realtime presence (`presence_on_node` FDB key space exists)

---

## Phase 4 — TUI

Bubble Tea terminal client using Connect-RPC.

- [ ] Issue list + detail
- [ ] Create/update issues
- [ ] Cycle management

---

## FDB key space status

| Prefix | Status |
|---|---|
| `membership_by_user/entity/role` | ✅ implemented |
| `assignment_on_node` / `assignment_to_user` | ✅ implemented |
| `label_on_node` / `issues_with_label` | ✅ implemented |
| `issue_in_module/cycle` + reverses | ✅ implemented |
| `activity_on_node` / `activity_by_user` | ✅ implemented |
| `property_definition` / `property_value_on_node` | ✅ implemented |
| `node_type_definition` | ✅ implemented |
| `comment_on_node` / `reply_to_comment` | ⬜ schema defined, not implemented |
| `relation_from_node` / `relation_to_node` | ⬜ schema defined, not implemented |
| `issue_children` / `epic_children` / `issues_in_epic` | ⬜ schema defined, not implemented |
| `notification_for_user` / `unread_notification_count` | ⬜ schema defined, not implemented |
| `watcher_of_node` / `node_watched_by_user` | ⬜ schema defined, not implemented |
| `count_on_node` | ⬜ schema defined, not implemented |
| `sort_position_in_view` | ⬜ schema defined, not implemented |
| All others | ⬜ schema defined in CLAUDE.md |

---

## Deployment (CT 117)

```
YugabyteDB   port 5433   docker
FoundationDB port 4500   docker  (cluster file: /etc/foundationdb/fdb.cluster — bind mount)
Meilisearch  port 7700   docker
Server       port 8000   host binary /root/tack/bin/server

MCP endpoint: http://tack.home.goodkind.io:8000/mcp
Token:        tack_ZBF9kJA7W7VXPvkV3G99Qdzc_xBAoSvhI_kcoeDNK7I
```

**Redeploy:**
```bash
rsync -av --exclude='.git' --exclude='bin/' ~/Sites/tack/ tack:/root/tack/
ssh tack 'cd /root/tack && CGO_ENABLED=1 go build -tags fdb -o bin/server ./cmd/server'
ssh tack 'kill $(pgrep -x server 2>/dev/null); sleep 1; DATABASE_URL="postgres://yugabyte:yugabyte@localhost:5433/tack?sslmode=disable" FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster ENV=production PORT=8000 nohup ./bin/server > /var/log/tack.log 2>&1 &'
```

**Fresh DB:**
```bash
ssh tack 'docker exec tack-yugabyte-1 sh -c "PGPASSWORD=yugabyte ysqlsh -h \$(hostname -i) -p 5433 -U yugabyte postgres -c \"DROP DATABASE tack\""'
ssh tack 'docker exec tack-yugabyte-1 sh -c "PGPASSWORD=yugabyte ysqlsh -h \$(hostname -i) -p 5433 -U yugabyte postgres -c \"CREATE DATABASE tack\""'
ssh tack 'docker exec tack-yugabyte-1 sh -c "PGPASSWORD=yugabyte ysqlsh -h \$(hostname -i) -p 5433 -U yugabyte tack -c \"CREATE EXTENSION pgcrypto\""'
ssh tack 'cd /root/tack && DATABASE_URL="..." ./bin/server migrate'
ssh tack 'cd /root/tack && DATABASE_URL="..." SEED_EMAIL="..." SEED_NAME="..." ./bin/server seed'
```
