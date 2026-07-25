-- +goose Up
-- +goose StatementBegin
/* Install pg_partman and register audit.events for native weekly partition
   management. pg_partman scans existing coverage during registration, so its
   native events_pYYYY_MM_DD names must replace the hand-written
   events_YYYY_MM_DD names before create_parent runs. p_start_partition is set
   to the week after existing coverage. Retention stays off: the audit ledger
   never auto-drops. */
CREATE SCHEMA IF NOT EXISTS partman;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pg_partman WITH SCHEMA partman;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    child RECORD;
BEGIN
    FOR child IN
        SELECT c.relname
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace n ON n.oid = p.relnamespace
         WHERE n.nspname = 'audit'
           AND p.relname = 'events'
           AND c.relname ~ '^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        EXECUTE format('ALTER TABLE audit.%I RENAME TO %I',
                       child.relname,
                       regexp_replace(child.relname, '^events_', 'events_p'));
    END LOOP;
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    max_upper DATE;
    start_part DATE;
BEGIN
    /* Highest existing partition upper bound, parsed from the partition bound. */
    SELECT max((regexp_match(pg_get_expr(c.relpartbound, c.oid),
                             'TO \(''([0-9-]+)'''))[1]::date)
      INTO max_upper
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_namespace n ON n.oid = p.relnamespace
     WHERE n.nspname = 'audit' AND p.relname = 'events';

    /* Start pg_partman at the later of the week after existing coverage and the
       current week, so a fresh database with no children also works. */
    start_part := greatest(coalesce(max_upper, date_trunc('week', now())::date),
                           date_trunc('week', now())::date);

    PERFORM partman.create_parent(
        p_parent_table    => 'audit.events',
        p_control         => 'event_time',
        p_type            => 'native',
        p_interval        => '1 week',
        p_premake         => 12,
        p_start_partition => start_part::text
    );
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE partman.part_config
   SET retention = NULL,
       retention_keep_table = true,
       infinite_time_partitions = true
 WHERE parent_table = 'audit.events';
-- +goose StatementEnd

-- +goose StatementBegin
/* SECURITY DEFINER wrapper so the audit_writer role (INSERT only) can drive
   partition maintenance without holding CREATE on schema audit. The pinned
   search_path closes the definer-escalation risk. The wrapper targets only
   audit.events. */
CREATE OR REPLACE FUNCTION audit.run_partition_maintenance()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, partman, audit
AS $$
BEGIN
    PERFORM partman.run_maintenance(p_parent_table => 'audit.events');
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION audit.run_partition_maintenance() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.run_partition_maintenance() TO audit_writer;

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS audit.run_partition_maintenance();
-- +goose StatementEnd

-- +goose StatementBegin
/* Unregister audit.events from pg_partman. Leaves existing partitions in place. */
DELETE FROM partman.part_config WHERE parent_table = 'audit.events';
-- +goose StatementEnd
