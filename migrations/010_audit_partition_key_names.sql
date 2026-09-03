-- Give every audit.events partition the primary-key name the engine would have
-- given it, so the ledger's own schema dump restores.
--
-- Migration 004 renamed the hand-written partitions events_YYYY_MM_DD to the
-- pg_partman form events_pYYYY_MM_DD and left their primary-key constraints
-- named events_YYYY_MM_DD_pkey. The engine's own dumper (ysql_dump on the
-- 2024.2 line) writes a partition as "CREATE TABLE ... PARTITION OF" with no
-- constraint clause, so a restore names the key events_pYYYY_MM_DD_pkey, and
-- the dump's later "ALTER INDEX ... ATTACH PARTITION" names the old one and
-- fails. The restore drill on QA stopped at exactly that statement on
-- 2026-09-03. Every partition pg_partman created since carries the default
-- name already, so after this the dump names what the restore creates.
--
-- Renaming a constraint is a catalog change; the key's storage is untouched.
-- Idempotent: a partition whose key already carries the default name is left
-- alone, so a retry after a partial run reconciles rather than fails.

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
