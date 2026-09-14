-- Fewer weekly audit partitions ahead of time (TACK-493).
--
-- pg_partman kept twelve future weekly partitions of audit.events, each with
-- its indexes, each object split into three tablets replicated to every
-- ledger node: about a third of the 383 tablets a node carried, all empty.
-- Two weeks ahead is enough: the audit consumer runs partition maintenance
-- on boot and daily, so a missed day still leaves a week of headroom.
--
-- The block below drops only future partitions that hold no rows and start
-- after the kept window. Past and current partitions, and any future one
-- with a row, stay. Retention is untouched: the ledger never auto-drops old
-- data. Every statement is idempotent, because YugabyteDB keeps completed
-- DDL when a migration transaction rolls back.

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
    /* premake 2 keeps the current week and the two after it. */
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
        lower_bound := to_date(substring(child.relname from 8), 'YYYY_MM_DD');
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
