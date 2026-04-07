-- +goose Up
CREATE TABLE projects (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    identifier       TEXT NOT NULL,  -- short prefix e.g. "ENG"
    description      TEXT NOT NULL DEFAULT '',
    network          SMALLINT NOT NULL DEFAULT 0,  -- 0=secret, 2=public
    default_state_id UUID,                         -- FK added after states table in 005
    metadata         JSONB NOT NULL DEFAULT '{}',
    created_by       UUID NOT NULL REFERENCES users(id),
    updated_by       UUID REFERENCES users(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, identifier)
);

-- Atomic sequence allocator: one row per (project, entity_type).
-- App layer: INSERT ... ON CONFLICT DO UPDATE SET next_seq = next_seq + 1 RETURNING next_seq - 1
-- No trigger, no race condition at any scale.
CREATE TABLE project_sequences (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL,  -- 'issue' | 'epic'
    next_seq    BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (project_id, entity_type)
);

CREATE TABLE project_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    member_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       SMALLINT NOT NULL DEFAULT 15,  -- 5=guest 10=viewer 15=member 20=admin
    is_active  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(project_id, member_id)
);

-- +goose Down
DROP TABLE project_members;
DROP TABLE project_sequences;
DROP TABLE projects;
