-- +goose Up
CREATE TABLE states (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    color        TEXT NOT NULL DEFAULT '#cccccc',
    group_name   TEXT NOT NULL DEFAULT 'backlog',
        -- backlog | unstarted | started | completed | cancelled
    sequence     FLOAT NOT NULL DEFAULT 65535,
    is_default   BOOLEAN NOT NULL DEFAULT false,
    created_by   UUID NOT NULL REFERENCES users(id),
    updated_by   UUID REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,
    UNIQUE(project_id, name)
);

ALTER TABLE projects
    ADD CONSTRAINT fk_projects_default_state
    FOREIGN KEY (default_state_id) REFERENCES states(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE projects DROP CONSTRAINT fk_projects_default_state;
DROP TABLE states;
