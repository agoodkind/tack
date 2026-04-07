-- +goose Up
CREATE TABLE cycles (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    start_date       DATE,
    end_date         DATE,
    sort_order       FLOAT NOT NULL DEFAULT 65535,
    -- Scrum velocity snapshot written on sprint close (idempotent)
    completed_points INT NOT NULL DEFAULT 0,
    metadata         JSONB NOT NULL DEFAULT '{}',
    created_by       UUID NOT NULL REFERENCES users(id),
    updated_by       UUID REFERENCES users(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at      TIMESTAMPTZ
);

CREATE TABLE cycle_issues (
    cycle_id UUID NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    PRIMARY KEY (cycle_id, issue_id)
);

-- +goose Down
DROP TABLE cycle_issues;
DROP TABLE cycles;
