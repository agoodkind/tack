-- +goose Up
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

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS consumer_offsets_writer_all    ON audit.consumer_offsets;
DROP POLICY IF EXISTS consumer_offsets_reader_select ON audit.consumer_offsets;
-- +goose StatementEnd

DROP TABLE IF EXISTS audit.consumer_offsets;
