-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- provides gen_random_uuid() on YugabyteDB

-- Tack schema — structural PM entities only.
-- UUID primary keys everywhere (no write hotspots on YugabyteDB).
-- created_by NOT NULL — a seeded user is required before any data exists.
-- No SQL join tables — all many-to-many relationships live in FoundationDB.
-- No entity tables (issues, epics, cycles, modules) — those live in FDB.
-- No JSONB properties columns — custom properties live entirely in FDB.

-- ── Orgs ──────────────────────────────────────────────────────────────────────

CREATE TABLE orgs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Users ─────────────────────────────────────────────────────────────────────

CREATE TABLE users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email        TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    avatar_url   TEXT,
    is_active    BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    label      TEXT NOT NULL DEFAULT '',
    last_used  TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Org membership ────────────────────────────────────────────────────────────
-- The ONLY SQL membership table. Auth gate queried on every request.
-- All sub-org membership (workspace, project, team, custom nodes) lives in FDB.

CREATE TABLE org_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       SMALLINT NOT NULL DEFAULT 15,  -- 5=guest 10=viewer 15=member 20=admin
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, user_id)
);

-- ── Workspaces ────────────────────────────────────────────────────────────────

CREATE TABLE workspaces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_workspaces_org ON workspaces(org_id);

-- ── Projects ──────────────────────────────────────────────────────────────────

CREATE TABLE projects (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id          UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    identifier       TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    network          SMALLINT NOT NULL DEFAULT 0,  -- 0=secret 2=public
    default_state_id UUID,
    created_by       UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, identifier)
);

-- ── States ────────────────────────────────────────────────────────────────────

CREATE TABLE states (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT 'backlog'
                   CHECK(group_name IN ('backlog','todo','started','completed','cancelled')),
    color      TEXT NOT NULL DEFAULT '#cccccc',
    sort_order FLOAT NOT NULL DEFAULT 65535,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE projects
    ADD CONSTRAINT fk_projects_default_state
    FOREIGN KEY (default_state_id) REFERENCES states(id) ON DELETE SET NULL;

-- ── Labels ────────────────────────────────────────────────────────────────────

CREATE TABLE labels (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    color        TEXT NOT NULL DEFAULT '#cccccc',
    sort_order   FLOAT NOT NULL DEFAULT 65535,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
ALTER TABLE projects DROP CONSTRAINT fk_projects_default_state;
DROP TABLE labels;
DROP TABLE states;
DROP TABLE projects;
DROP TABLE workspaces;
DROP TABLE org_members;
DROP TABLE api_tokens;
DROP TABLE users;
DROP TABLE orgs;
