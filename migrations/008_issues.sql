-- +goose Up
CREATE TABLE issues (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id           UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id            UUID REFERENCES issues(id) ON DELETE SET NULL,
    state_id             UUID REFERENCES states(id) ON DELETE SET NULL,
    epic_id              UUID,  -- FK added in 008 after epics table exists
    name                 TEXT NOT NULL,
    description_html     TEXT NOT NULL DEFAULT '',
    description_stripped TEXT,
    priority             TEXT NOT NULL DEFAULT 'none',
    sequence_id          INT NOT NULL DEFAULT 1,
    sort_order           FLOAT NOT NULL DEFAULT 65535,
    start_date           DATE,
    target_date          DATE,
    completed_at         TIMESTAMPTZ,
    archived_at          TIMESTAMPTZ,
    is_draft             BOOLEAN NOT NULL DEFAULT false,
    external_source      TEXT,
    external_id          TEXT,
    metadata             JSONB NOT NULL DEFAULT '{}',
    created_by           UUID NOT NULL REFERENCES users(id),
    updated_by           UUID REFERENCES users(id),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ
);

CREATE TABLE issue_assignees (
    issue_id    UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    assignee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, assignee_id)
);

CREATE TABLE issue_labels (
    issue_id UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    label_id UUID NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);

-- +goose Down
DROP TABLE issue_labels;
DROP TABLE issue_assignees;
DROP TABLE issues;
