-- +goose Up

-- ── audit.consumer_offsets ──────────────────────────────────────────────────
-- Kafka offset bookkeeping, committed with the projected events.

-- +goose StatementBegin
CREATE TABLE audit.consumer_offsets (
    consumer_group TEXT        NOT NULL,
    topic          TEXT        NOT NULL,
    partition      INT         NOT NULL,
    "offset"       BIGINT      NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY ((consumer_group, topic) HASH, partition ASC)
);
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE ON audit.consumer_offsets TO audit_writer;
GRANT SELECT ON audit.consumer_offsets TO audit_reader;

ALTER TABLE audit.consumer_offsets ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.consumer_offsets FORCE ROW LEVEL SECURITY;

CREATE POLICY consumer_offsets_writer_all ON audit.consumer_offsets FOR ALL TO audit_writer
    USING (true) WITH CHECK (true);
CREATE POLICY consumer_offsets_reader_select ON audit.consumer_offsets FOR SELECT TO audit_reader
    USING (true);

-- ── audit.events idempotency index ──────────────────────────────────────────
-- Idempotent projection across Kafka redelivery on producer-assigned event_id.

-- +goose StatementBegin
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

-- ── audit.events_dlq ────────────────────────────────────────────────────────
-- Dead-letter table for unprocessable Kafka payloads.

CREATE TABLE audit.events_dlq (
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    topic       TEXT        NOT NULL,
    partition   INT         NOT NULL,
    "offset"    BIGINT      NOT NULL,
    payload     BYTEA       NOT NULL,
    error       TEXT        NOT NULL,
    PRIMARY KEY ((topic, partition) HASH, "offset" ASC)
);

GRANT INSERT, SELECT ON audit.events_dlq TO audit_writer;
GRANT SELECT ON audit.events_dlq TO audit_reader;

ALTER TABLE audit.events_dlq ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events_dlq FORCE ROW LEVEL SECURITY;

CREATE POLICY events_dlq_writer_all ON audit.events_dlq FOR ALL TO audit_writer
    USING (true) WITH CHECK (true);
CREATE POLICY events_dlq_reader_select ON audit.events_dlq FOR SELECT TO audit_reader
    USING (true);

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS events_dlq_writer_all          ON audit.events_dlq;
DROP POLICY IF EXISTS events_dlq_reader_select       ON audit.events_dlq;
DROP POLICY IF EXISTS consumer_offsets_writer_all    ON audit.consumer_offsets;
DROP POLICY IF EXISTS consumer_offsets_reader_select ON audit.consumer_offsets;
-- +goose StatementEnd

DROP TABLE IF EXISTS audit.events_dlq;
DROP INDEX IF EXISTS audit.events_event_id_uniq;
DROP TABLE IF EXISTS audit.consumer_offsets;
