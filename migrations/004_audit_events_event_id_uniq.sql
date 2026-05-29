-- +goose Up
-- +goose StatementBegin
/*
audit.events is partitioned by RANGE (event_time), so a unique index must
include the partition key. The index therefore covers (event_id, event_time)
rather than event_id alone. That is still sufficient for idempotent projection:
the Kafka consumer carries event_time inside the event payload, so a redelivered
event reinserts with the same event_id and the same event_time and trips the
constraint. The guard below rejects pre-existing duplicate (event_id, event_time)
pairs before the index is built so the failure is legible.
*/
DO $$
DECLARE
    total_count BIGINT;
    distinct_count BIGINT;
BEGIN
    SELECT count(*), count(DISTINCT (event_id, event_time))
      INTO total_count, distinct_count
      FROM audit.events;
    IF total_count <> distinct_count THEN
        RAISE EXCEPTION 'audit.events has duplicate (event_id, event_time) rows: total=% distinct=%',
            total_count, distinct_count;
    END IF;
END$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS events_event_id_uniq
    ON audit.events (event_id, event_time);

-- +goose Down
DROP INDEX IF EXISTS audit.events_event_id_uniq;
