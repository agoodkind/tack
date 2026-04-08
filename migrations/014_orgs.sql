-- +goose Up
CREATE TABLE orgs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id    UUID UNIQUE NOT NULL DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       SMALLINT NOT NULL DEFAULT 15,  -- 5=guest 10=viewer 15=member 20=admin
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, user_id)
);

ALTER TABLE workspaces
    ADD COLUMN org_id UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE;

CREATE INDEX idx_workspaces_org ON workspaces(org_id);

-- +goose Down
DROP INDEX idx_workspaces_org;
ALTER TABLE workspaces DROP COLUMN org_id;
DROP TABLE org_members;
DROP TABLE orgs;
