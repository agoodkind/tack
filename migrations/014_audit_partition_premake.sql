-- Reduce future weekly audit partitions (TACK-493).
--
-- Twelve future weekly partitions and their indexes required about a third
-- of each ledger node's 383 tablets despite containing no rows. Two future
-- weeks permit a missed daily maintenance run. The audit consumer also runs
-- partition maintenance at startup.
--
-- Drop only empty future partitions that start after the retained window.
-- Preserve past and current partitions and all partitions with rows.
-- Keep retention disabled; the ledger must not drop old data automatically.
-- Every statement supports retries because YugabyteDB keeps completed DDL
-- when a migration transaction rolls back.

-- +goose Up
-- +goose StatementBegin
UPDATE partman.part_config
   SET premake = 2
 WHERE parent_table = 'audit.events';
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    child RECORD;
    lower_bound DATE;
    keep_through DATE;
    row_count BIGINT;
BEGIN
    /* Keep the current week and the two after it. With premake 2, the next
       maintenance run adds weeks until the newest partition starts at least
       two weeks after now, which is a third future week on any day after the
       start of a week. */
    keep_through := date_trunc('week', now())::date + 14;
    FOR child IN
        SELECT c.relname
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
          JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
         WHERE parent_ns.nspname = 'audit' AND p.relname = 'events'
           AND child_ns.nspname = 'audit'
           AND c.relname ~ '^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$'
    LOOP
        /* events_pYYYY_MM_DD: the date starts at the ninth character. */
        lower_bound := to_date(substring(child.relname from 9), 'YYYY_MM_DD');
        IF lower_bound > keep_through THEN
            EXECUTE format('SELECT count(*) FROM audit.%I', child.relname)
               INTO row_count;
            IF row_count = 0 THEN
                EXECUTE format('DROP TABLE audit.%I', child.relname);
            END IF;
        END IF;
    END LOOP;
END$$;
-- +goose StatementEnd

-- +goose Down
UPDATE partman.part_config
   SET premake = 12
 WHERE parent_table = 'audit.events';
