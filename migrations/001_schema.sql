-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- provides gen_random_uuid() on YugabyteDB

-- Complete tack schema.
-- UUID primary keys everywhere — no write hotspots on YugabyteDB.
-- created_by NOT NULL — a seeded user is required before any data exists.
-- No SQL join tables — all many-to-many relationships live in FoundationDB
-- with forward + reverse indexes. See CLAUDE.md for the full FDB key space.
-- No JSONB properties columns — custom properties live entirely in FDB.
-- Descriptions are Markdown TEXT — not selected on list queries.

-- ── River job queue ───────────────────────────────────────────────────────────

CREATE TABLE river_jobs (
    id           BIGSERIAL PRIMARY KEY,
    state        TEXT NOT NULL DEFAULT 'available'
                     CHECK(state IN ('available','cancelled','completed','discarded',
                                     'pending','retryable','running','scheduled')),
    attempt      SMALLINT NOT NULL DEFAULT 0,
    max_attempts SMALLINT NOT NULL,
    attempted_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finalized_at TIMESTAMPTZ,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    priority     SMALLINT NOT NULL DEFAULT 1,
    args         JSONB NOT NULL DEFAULT '{}',
    errors       JSONB,
    kind         TEXT NOT NULL,
    queue        TEXT NOT NULL DEFAULT 'default',
    tags         TEXT[] NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_river_jobs_kind_state ON river_jobs(kind, state);
CREATE INDEX idx_river_jobs_scheduled  ON river_jobs(scheduled_at, priority)
    WHERE state IN ('available','scheduled','retryable');

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
-- The ONLY SQL membership table. Auth gate — queried on every request.
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

CREATE TABLE project_sequences (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL,
    next_seq    BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (project_id, entity_type)
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

-- ── Epics ─────────────────────────────────────────────────────────────────────

CREATE TABLE epics (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id    UUID REFERENCES epics(id) ON DELETE SET NULL,
    state_id     UUID REFERENCES states(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    priority     TEXT NOT NULL DEFAULT 'none',
    sequence_id  INT NOT NULL DEFAULT 1,
    sort_order   FLOAT NOT NULL DEFAULT 65535,
    start_date   DATE,
    target_date  DATE,
    completed_at TIMESTAMPTZ,
    archived_at  TIMESTAMPTZ,
    is_draft     BOOLEAN NOT NULL DEFAULT false,
    created_by   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
);

-- ── Issues ────────────────────────────────────────────────────────────────────

CREATE TABLE issues (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id         UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    epic_id         UUID REFERENCES epics(id) ON DELETE SET NULL,
    parent_id       UUID REFERENCES issues(id) ON DELETE SET NULL,
    state_id        UUID REFERENCES states(id) ON DELETE SET NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    priority        TEXT NOT NULL DEFAULT 'none',
    sequence_id     INT NOT NULL DEFAULT 1,
    sort_order      FLOAT NOT NULL DEFAULT 65535,
    start_date      DATE,
    target_date     DATE,
    completed_at    TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    is_draft        BOOLEAN NOT NULL DEFAULT false,
    external_source TEXT,
    external_id     TEXT,
    created_by      UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

-- ── Modules ───────────────────────────────────────────────────────────────────

CREATE TABLE modules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'backlog'
                     CHECK(status IN ('backlog','planned','in_progress','paused','completed','cancelled')),
    start_date   DATE,
    target_date  DATE,
    sort_order   FLOAT NOT NULL DEFAULT 65535,
    created_by   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at  TIMESTAMPTZ
);

-- ── Cycles ────────────────────────────────────────────────────────────────────

CREATE TABLE cycles (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id          UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    start_date       DATE,
    end_date         DATE,
    sort_order       FLOAT NOT NULL DEFAULT 65535,
    completed_points INT NOT NULL DEFAULT 0,
    created_by       UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at      TIMESTAMPTZ
);

-- ── Indexes ───────────────────────────────────────────────────────────────────

CREATE INDEX idx_issues_project        ON issues(project_id)          WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_epic           ON issues(epic_id)             WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_state_project  ON issues(project_id, state_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_parent         ON issues(parent_id)           WHERE deleted_at IS NULL;
CREATE INDEX idx_epics_project         ON epics(project_id)           WHERE deleted_at IS NULL;
CREATE INDEX idx_epics_parent          ON epics(parent_id)            WHERE deleted_at IS NULL;
CREATE INDEX idx_modules_project       ON modules(project_id);
CREATE INDEX idx_cycles_project        ON cycles(project_id);

-- Full-text search is handled by Meilisearch.
-- YugabyteDB does not support GENERATED ALWAYS AS STORED — no tsvector columns.

-- +goose Down
DROP INDEX idx_cycles_project;
DROP INDEX idx_modules_project;
DROP INDEX idx_epics_parent;
DROP INDEX idx_epics_project;
DROP INDEX idx_issues_parent;
DROP INDEX idx_issues_state_project;
DROP INDEX idx_issues_epic;
DROP INDEX idx_issues_project;
DROP TABLE cycles;
DROP TABLE modules;
DROP TABLE issues;
DROP TABLE epics;
ALTER TABLE projects DROP CONSTRAINT fk_projects_default_state;
DROP TABLE states;
DROP TABLE project_sequences;
DROP TABLE projects;
DROP TABLE workspaces;
DROP TABLE org_members;
DROP TABLE api_tokens;
DROP TABLE users;
DROP TABLE orgs;
DROP TABLE river_jobs;
