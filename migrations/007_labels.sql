-- +goose Up
CREATE TABLE labels (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,  -- NULL = workspace-level
    parent_id    UUID REFERENCES labels(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    color        TEXT NOT NULL DEFAULT '',
    sort_order   FLOAT NOT NULL DEFAULT 65535,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
);

-- +goose Down
DROP TABLE labels;
