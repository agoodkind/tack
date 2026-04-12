-- +goose Up

-- org_members.org_id is now a soft UUID reference — orgs live in FDB, not SQL.
-- Drop the FK before dropping the orgs table.
ALTER TABLE org_members DROP CONSTRAINT IF EXISTS org_members_org_id_fkey;

-- Drop entity tables in dependency order.
-- Labels reference workspaces and projects.
DROP TABLE IF EXISTS labels;
-- States reference projects; projects has a self-referential FK to states (default_state_id).
ALTER TABLE projects DROP CONSTRAINT IF EXISTS fk_projects_default_state;
DROP TABLE IF EXISTS states;
DROP TABLE IF EXISTS projects;
-- Workspaces reference orgs.
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS orgs;

-- +goose Down
-- Intentionally empty — entity tables have been migrated to FDB.
-- To restore: re-run the original schema and backfill from FDB.
