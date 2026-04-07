-- +goose Up
-- Generated stored tsvectors — updated automatically on INSERT/UPDATE by Postgres.
ALTER TABLE issues ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(description_stripped, '')), 'B')
    ) STORED;

ALTER TABLE epics ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(name, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(description_stripped, '')), 'B')
    ) STORED;

CREATE INDEX idx_issues_search   ON issues USING gin(search_vector);
CREATE INDEX idx_epics_search    ON epics  USING gin(search_vector);
CREATE INDEX idx_issues_project  ON issues(project_id)  WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_epic     ON issues(epic_id)     WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_state    ON issues(state_id)    WHERE deleted_at IS NULL;
CREATE INDEX idx_epics_project   ON epics(project_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_modules_project ON modules(project_id);
CREATE INDEX idx_cycles_project  ON cycles(project_id);

-- +goose Down
DROP INDEX idx_cycles_project;
DROP INDEX idx_modules_project;
DROP INDEX idx_epics_project;
DROP INDEX idx_issues_state;
DROP INDEX idx_issues_epic;
DROP INDEX idx_issues_project;
DROP INDEX idx_epics_search;
DROP INDEX idx_issues_search;
ALTER TABLE epics  DROP COLUMN search_vector;
ALTER TABLE issues DROP COLUMN search_vector;
