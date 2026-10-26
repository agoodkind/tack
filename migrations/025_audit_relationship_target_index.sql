-- Index relationship change rows by the nodes at each end (TACK-597).
--
-- A relationship change row stores the zero UUID in entity_id and names the
-- relationship in context.target: kind 'relationship', source_id,
-- relation_type, and target_id. `audit last-change` reads the relationship
-- rows of one node within one org. Without these indexes, that read scans
-- every row of the org window and decodes its context.
--
-- Each index covers only rows with a relationship target. The planner uses a
-- partial index only for a query that repeats its kind predicate, and the
-- read repeats it. PostgreSQL native partitioning creates the matching index
-- on every existing weekly partition and on every partition that pg_partman
-- creates later.

-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS events_relationship_source_time
    ON audit.events ((org_id, (context->'target'->>'source_id')) HASH, event_time DESC)
    WHERE context->'target'->>'kind' = 'relationship';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS events_relationship_target_time
    ON audit.events ((org_id, (context->'target'->>'target_id')) HASH, event_time DESC)
    WHERE context->'target'->>'kind' = 'relationship';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS audit.events_relationship_target_time;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS audit.events_relationship_source_time;
-- +goose StatementEnd
