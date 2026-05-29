-- +goose Up
-- +goose StatementBegin
/*
Guard against pre-existing chain-race orphans (TACK-271). If two concurrent
Record() calls already inserted rows that share an (org_id, shard, seq) key,
the unique index below would fail with an opaque message. This block detects
those groups first and raises with a count so an operator can remediate before
the index is created.
*/
DO $$
DECLARE
    dup_groups BIGINT;
BEGIN
    SELECT count(*) INTO dup_groups FROM (
        SELECT org_id, shard, seq
          FROM audit.events
         GROUP BY org_id, shard, seq
        HAVING count(*) > 1
    ) d;
    IF dup_groups > 0 THEN
        RAISE EXCEPTION
            'audit.events has % (org_id, shard, seq) duplicate groups; remediate TACK-271 chain-race orphans before adding the unique index',
            dup_groups;
    END IF;
END$$;
-- +goose StatementEnd

/*
The non-unique events_seq_check index from migration 002 covered the same
columns for chain-walk reads. Replacing it with a UNIQUE index makes two
concurrent Record() calls for one (org_id, shard) unable to both insert at the
same seq, because one INSERT fails with 23505 and the recorder retries against
the advanced chain head. Migration 004 already proved YugabyteDB accepts a
unique index on this RANGE(event_time)-partitioned table without the partition
key (yugabyte-db issue #7520).
*/
DROP INDEX IF EXISTS audit.events_seq_check;

CREATE UNIQUE INDEX IF NOT EXISTS events_org_shard_seq_uniq
    ON audit.events (org_id, shard, seq);

-- +goose Down
DROP INDEX IF EXISTS audit.events_org_shard_seq_uniq;

CREATE INDEX IF NOT EXISTS events_seq_check
    ON audit.events (org_id, shard, seq);
