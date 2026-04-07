-- +goose Up
CREATE TABLE modules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'backlog',
        -- backlog | planned | in_progress | paused | completed | cancelled
    start_date   DATE,
    target_date  DATE,
    sort_order   FLOAT NOT NULL DEFAULT 65535,
    metadata     JSONB NOT NULL DEFAULT '{}',
    created_by   UUID NOT NULL REFERENCES users(id),
    updated_by   UUID REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at  TIMESTAMPTZ
);

CREATE TABLE module_issues (
    module_id UUID NOT NULL REFERENCES modules(id) ON DELETE CASCADE,
    issue_id  UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    PRIMARY KEY (module_id, issue_id)
);

-- +goose Down
DROP TABLE module_issues;
DROP TABLE modules;
