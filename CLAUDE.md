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
- **FoundationDB** — extensibility layer: node types, property definitions, property values, activity log. All properties live entirely in FDB — no JSONB columns on YugabyteDB entities.
- **Meilisearch** — search. Currently stubbed (Noop); real indexing on write is the next step.

**API layer:**
- MCP Streamable HTTP (`/mcp`) — current primary interface. 48 tools. Token resolves user → workspaces → custom node types → tool set.
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
- Properties live entirely in FDB — no JSONB metadata columns anywhere.
- Descriptions are Markdown TEXT — not HTML, no stripped copy.
- `node_id UUID` on every core entity — the FDB bridge key.
- Migrations run via `./server migrate` only — never on HTTP startup.
- Goose requires `CREATE EXTENSION pgcrypto` to be run manually before the first migration on YugabyteDB.
- YugabyteDB does not support `GENERATED ALWAYS AS STORED` columns — no tsvector columns.
- Search is Meilisearch, not Postgres FTS.
- Updates are partial everywhere — only provided fields change.
- Optional MCP input fields use `*string` with `json:",omitempty"` — required by `google/jsonschema-go`.
- `jsonschema:"..."` tag values are interpreted as descriptions by the MCP SDK, not as constraints. Do not use them.

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
