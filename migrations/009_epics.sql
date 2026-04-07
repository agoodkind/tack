-- +goose Up
CREATE TABLE epics (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id           UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id            UUID REFERENCES epics(id) ON DELETE SET NULL,
    state_id             UUID REFERENCES states(id) ON DELETE SET NULL,
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

CREATE TABLE epic_assignees (
    epic_id     UUID NOT NULL REFERENCES epics(id) ON DELETE CASCADE,
    assignee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (epic_id, assignee_id)
);

CREATE TABLE epic_labels (
    epic_id  UUID NOT NULL REFERENCES epics(id) ON DELETE CASCADE,
    label_id UUID NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (epic_id, label_id)
);

-- Wire up the epic FK deferred from 007
ALTER TABLE issues
    ADD CONSTRAINT fk_issues_epic
    FOREIGN KEY (epic_id) REFERENCES epics(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE issues DROP CONSTRAINT fk_issues_epic;
DROP TABLE epic_labels;
DROP TABLE epic_assignees;
DROP TABLE epics;
