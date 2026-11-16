-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- provides gen_random_uuid() on YugabyteDB

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
-- Authorization checks org membership on every request.
-- org_id references an org UUID in FDB without a foreign key constraint.
-- FDB stores workspace, project, and team membership.

CREATE TABLE org_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       SMALLINT NOT NULL DEFAULT 15,  -- 5=guest 10=viewer 15=member 20=admin
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, user_id)
);

-- +goose Down
DROP TABLE org_members;
DROP TABLE api_tokens;
DROP TABLE users;
