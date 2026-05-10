-- +goose Up
-- +goose StatementBegin
CREATE TABLE audit.events_v2 (
    org_id          UUID        NOT NULL,
    shard           SMALLINT    NOT NULL,
    event_time      TIMESTAMPTZ NOT NULL,
    event_id        UUID        NOT NULL,
    seq             BIGINT      NOT NULL,
    actor_id        UUID        NOT NULL,
    actor_kind      SMALLINT    NOT NULL,
    action          TEXT        NOT NULL,
    entity_kind     TEXT        NOT NULL,
    entity_id       UUID        NOT NULL,
    context         JSONB       NOT NULL,
    delta           JSONB       NOT NULL,
    pii_ref         UUID,
    prev_hash       BYTEA       NOT NULL,
    row_hash        BYTEA       NOT NULL,
    idempotency_key TEXT        NOT NULL,
    PRIMARY KEY ((org_id, shard) HASH, event_time ASC, seq ASC, event_id ASC)
);
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS events_v2_event_id_uniq
    ON audit.events_v2 (event_id);

CREATE INDEX events_v2_actor_time
    ON audit.events_v2 ((org_id, actor_id) HASH, event_time DESC)
    INCLUDE (action, entity_kind, entity_id, event_id);

CREATE INDEX events_v2_entity_time
    ON audit.events_v2 ((org_id, entity_kind, entity_id) HASH, event_time DESC)
    INCLUDE (action, actor_id, event_id);

CREATE INDEX events_v2_seq_check
    ON audit.events_v2 (org_id, shard, seq);

GRANT INSERT, SELECT ON audit.events_v2 TO audit_writer;
GRANT SELECT ON audit.events_v2 TO audit_reader;
GRANT SELECT ON audit.events_v2 TO audit_archiver;

ALTER TABLE audit.events_v2 ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events_v2 FORCE  ROW LEVEL SECURITY;

CREATE POLICY events_v2_writer_insert   ON audit.events_v2 FOR INSERT TO audit_writer   WITH CHECK (true);
CREATE POLICY events_v2_writer_select   ON audit.events_v2 FOR SELECT TO audit_writer   USING (true);
CREATE POLICY events_v2_reader_select   ON audit.events_v2 FOR SELECT TO audit_reader   USING (true);
CREATE POLICY events_v2_archiver_select ON audit.events_v2 FOR SELECT TO audit_archiver USING (true);

CREATE TABLE audit.events_v2_dlq (
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    topic       TEXT        NOT NULL,
    partition   INT         NOT NULL,
    "offset"    BIGINT      NOT NULL,
    payload     BYTEA       NOT NULL,
    error       TEXT        NOT NULL,
    PRIMARY KEY ((topic, partition) HASH, "offset" ASC)
);

GRANT INSERT, SELECT ON audit.events_v2_dlq TO audit_writer;
GRANT SELECT ON audit.events_v2_dlq TO audit_reader;

ALTER TABLE audit.events_v2_dlq ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events_v2_dlq FORCE  ROW LEVEL SECURITY;

CREATE POLICY events_v2_dlq_writer_all    ON audit.events_v2_dlq FOR ALL    TO audit_writer USING (true) WITH CHECK (true);
CREATE POLICY events_v2_dlq_reader_select ON audit.events_v2_dlq FOR SELECT TO audit_reader USING (true);

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS events_v2_writer_insert     ON audit.events_v2;
DROP POLICY IF EXISTS events_v2_writer_select     ON audit.events_v2;
DROP POLICY IF EXISTS events_v2_reader_select     ON audit.events_v2;
DROP POLICY IF EXISTS events_v2_archiver_select   ON audit.events_v2;
DROP POLICY IF EXISTS events_v2_dlq_writer_all    ON audit.events_v2_dlq;
DROP POLICY IF EXISTS events_v2_dlq_reader_select ON audit.events_v2_dlq;
-- +goose StatementEnd
DROP TABLE IF EXISTS audit.events_v2_dlq;
DROP TABLE IF EXISTS audit.events_v2;
