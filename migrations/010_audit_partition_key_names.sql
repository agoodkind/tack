-- Rename audit.events partition primary keys to the engine's default names.
-- Migration 004 renamed events_YYYY_MM_DD partitions to events_pYYYY_MM_DD
-- without renaming their events_YYYY_MM_DD_pkey constraints.
-- ysql_dump 2024.2 writes CREATE TABLE ... PARTITION OF without a constraint
-- clause. Restoration creates events_pYYYY_MM_DD_pkey, but the dump's later
-- ALTER INDEX ... ATTACH PARTITION references the old constraint and fails.
-- pg_partman already uses the default names for new partitions.
--
-- Constraint renaming changes only the catalog, not key storage.
-- This migration skips keys with default names and supports partial retries.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    child RECORD;
BEGIN
    FOR child IN
        SELECT c.relname,
               con.conname,
               c.relname || '_pkey' AS default_name
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
          JOIN pg_constraint con ON con.conrelid = c.oid AND con.contype = 'p'
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND con.conname <> c.relname || '_pkey'
    LOOP
        EXECUTE format('ALTER TABLE audit.%I RENAME CONSTRAINT %I TO %I',
                       child.relname, child.conname, child.default_name);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- The old names were an accident of migration 004, not a contract anything
-- reads, and restoring them would make the schema dump unrestorable again.
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
