-- Weekly audit partitions despite a far-future child (TACK-551).
--
-- pg_partman premakes only after the newest child of audit.events. A child
-- that starts in a later year, such as a restore proof week in 2031, leaves
-- the current week and the coming weeks without a partition, and every audit
-- write for those weeks fails. pg_partman also parses each child name as a
-- date. A child named outside events_pYYYY_MM_DD fails maintenance.
--
-- The guard refuses the migration while a child of audit.events has a name
-- outside events_pYYYY_MM_DD, and the error lists each such child. The guard
-- runs first: YugabyteDB keeps completed DDL when a migration transaction
-- rolls back, and a refused migration leaves the maintenance function as it
-- was. After the guard, maintenance creates the current week and the premake
-- weeks after it by their start times, whatever children exist later.
-- create_partition_time skips a week that already has its partition.
-- pg_partman names each new week from its start time in the session time
-- zone. The function sets TimeZone to UTC, and a Monday 00:00 UTC week gets
-- the name of that Monday under every caller time zone.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    stray_names TEXT;
BEGIN
    SELECT string_agg(c.relname, ', ' ORDER BY c.relname)
      INTO stray_names
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
     WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
       AND c.relname !~ '^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$';
    IF stray_names IS NOT NULL THEN
        RAISE EXCEPTION 'audit.events has children named outside events_pYYYY_MM_DD: %', stray_names;
    END IF;
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit.run_partition_maintenance()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, partman, audit
SET TimeZone = 'UTC'
AS $$
DECLARE
    current_week TIMESTAMPTZ := date_trunc('week', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
    premake_weeks INTEGER;
    coming_weeks TIMESTAMPTZ[];
BEGIN
    PERFORM partman.run_maintenance(p_parent_table => 'audit.events');
    SELECT premake INTO premake_weeks
      FROM partman.part_config
     WHERE parent_table = 'audit.events';
    IF premake_weeks IS NULL THEN
        RAISE EXCEPTION 'audit.events has no pg_partman premake setting';
    END IF;
    SELECT array_agg(current_week + make_interval(weeks => week_offset) ORDER BY week_offset)
      INTO coming_weeks
      FROM generate_series(0, premake_weeks) AS week_offset;
    PERFORM partman.create_partition_time('audit.events', coming_weeks);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION audit.run_partition_maintenance() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit.run_partition_maintenance() TO audit_writer;

-- +goose Down
-- +goose StatementBegin
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
