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
    target_attached BOOLEAN;
    target_oid OID;
BEGIN
    /* Preflight before YugabyteDB performs nontransactional renames. */
    FOR child IN
        SELECT c.relname,
               regexp_replace(c.relname, '^events_', 'events_p') AS target_name
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND c.relname ~ '^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        SELECT target.oid, EXISTS (
                   SELECT 1 FROM pg_inherits WHERE inhrelid = target.oid)
          INTO target_oid, target_attached
          FROM pg_class target
          JOIN pg_namespace target_ns ON target_ns.oid = target.relnamespace
         WHERE target_ns.nspname = 'audit'
           AND target.relname = child.target_name;

        IF target_oid IS NOT NULL THEN
            IF target_attached THEN
                RAISE EXCEPTION 'cannot rename audit.% to audit.%: attached target relation already exists',
                    child.relname, child.target_name;
            END IF;
            RAISE EXCEPTION 'cannot rename audit.% to audit.%: detached target relation already exists',
                child.relname, child.target_name;
        END IF;
    END LOOP;

    FOR child IN
        SELECT c.relname,
               regexp_replace(c.relname, '^events_', 'events_p') AS target_name
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND c.relname ~ '^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        EXECUTE format('ALTER TABLE audit.%I RENAME TO %I',
                       child.relname,
                       child.target_name);
    END LOOP;
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    child_count INTEGER;
    max_upper DATE;
    parsed_count INTEGER;
    registered BOOLEAN;
    start_part DATE;
BEGIN
    SELECT EXISTS (
        SELECT 1
          FROM partman.part_config
         WHERE parent_table = 'audit.events'
    ) INTO registered;

    IF registered AND EXISTS (
        SELECT 1
          FROM partman.part_config
         WHERE parent_table = 'audit.events'
           AND (partition_type <> 'native'
                OR partition_interval <> '7 days'
                OR control <> 'event_time')
    ) THEN
        RAISE EXCEPTION
            'existing pg_partman registration for audit.events has incompatible configuration';
    END IF;

    IF NOT registered THEN
        /* Parse the complete TIMESTAMPTZ literal rather than its leading date. */
        WITH child_bounds AS (
            SELECT (regexp_match(pg_get_expr(c.relpartbound, c.oid),
                                 'TO \(''([^'']+)''\)'))[1] AS upper_bound
              FROM pg_inherits i
              JOIN pg_class c ON c.oid = i.inhrelid
              JOIN pg_class p ON p.oid = i.inhparent
              JOIN pg_namespace n ON n.oid = p.relnamespace
             WHERE n.nspname = 'audit' AND p.relname = 'events'
        )
        SELECT count(*), count(upper_bound),
               max((upper_bound::timestamptz AT TIME ZONE 'UTC')::date)
          INTO child_count, parsed_count, max_upper
          FROM child_bounds;

        IF child_count > 0 AND parsed_count = 0 THEN
            RAISE EXCEPTION
                'cannot determine audit.events partition upper bound from % children',
                child_count;
        END IF;

        /* Start at the later of existing coverage and the current week. */
        start_part := greatest(
            coalesce(max_upper, date_trunc('week', now())::date),
            date_trunc('week', now())::date
        );

        PERFORM partman.create_parent(
            p_parent_table    => 'audit.events',
            p_control         => 'event_time',
            p_type            => 'native',
            p_interval        => '1 week',
            p_premake         => 12,
            p_start_partition => start_part::text
        );
    END IF;
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE partman.part_config
   SET premake = 12,
       retention = NULL,
       retention_keep_table = true,
       infinite_time_partitions = true
 WHERE parent_table = 'audit.events';
-- +goose StatementEnd

-- +goose StatementBegin
/* Let audit_writer maintain only audit.events without CREATE on audit. */
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
DO $$
DECLARE
    child RECORD;
BEGIN
    FOR child IN
        SELECT c.relname,
               regexp_replace(c.relname, '^events_p', 'events_') AS target_name
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND c.relname ~ '^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        IF to_regclass(format('audit.%I', child.target_name)) IS NOT NULL THEN
            RAISE EXCEPTION 'cannot rename audit.% to audit.%: target relation already exists',
                child.relname, child.target_name;
        END IF;
    END LOOP;
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
DROP FUNCTION IF EXISTS audit.run_partition_maintenance();
-- +goose StatementEnd

-- +goose StatementBegin
/* Unregister without dropping ledger partitions. The pinned YugabyteDB build
   creates no default partition, and create_parent safely reuses its retained
   partman.template_audit_events table on a later Up. */
DELETE FROM partman.part_config WHERE parent_table = 'audit.events';
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    child RECORD;
BEGIN
    FOR child IN
        SELECT c.relname,
               regexp_replace(c.relname, '^events_p', 'events_') AS target_name
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND c.relname ~ '^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        EXECUTE format('ALTER TABLE audit.%I RENAME TO %I',
                       child.relname,
                       child.target_name);
    END LOOP;
END$$;
-- +goose StatementEnd
