-- +goose Up
-- Add node_id bridge to FDB for all core entities.
-- Properties live entirely in FDB — no JSONB properties column.
-- Description is plain Markdown TEXT — no HTML, no stripped copy.

ALTER TABLE workspaces
    ADD COLUMN node_id UUID UNIQUE DEFAULT gen_random_uuid(),
    DROP COLUMN metadata;

ALTER TABLE projects
    ADD COLUMN node_id UUID UNIQUE DEFAULT gen_random_uuid(),
    DROP COLUMN metadata;

ALTER TABLE issues
    ADD COLUMN node_id UUID UNIQUE DEFAULT gen_random_uuid(),
    DROP COLUMN metadata,
    RENAME COLUMN description_html TO description;

ALTER TABLE issues
    DROP COLUMN description_stripped;

-- +goose Down
ALTER TABLE issues
    ADD COLUMN description_stripped TEXT,
    RENAME COLUMN description TO description_html,
    DROP COLUMN node_id;

ALTER TABLE projects
    ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}',
    DROP COLUMN node_id;

ALTER TABLE workspaces
    ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}',
    DROP COLUMN node_id;
