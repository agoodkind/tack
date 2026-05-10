-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    total_count BIGINT;
    distinct_count BIGINT;
BEGIN
    SELECT count(*), count(DISTINCT event_id) INTO total_count, distinct_count
      FROM audit.events;
    IF total_count <> distinct_count THEN
        RAISE EXCEPTION 'audit.events has duplicate event_id rows: total=% distinct=%',
            total_count, distinct_count;
    END IF;
END$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS events_event_id_uniq
    ON audit.events (event_id);

-- +goose Down
DROP INDEX IF EXISTS audit.events_event_id_uniq;
