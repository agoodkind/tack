-- One event id, one ledger row.
--
-- audit.events is range-partitioned on event_time, so its unique index has to
-- carry event_time too, and it therefore only rejects a redelivery that
-- repeats the same instant. A producer that re-derives an event keeps the
-- event id and stamps a fresh time, and the ledger accepted it as a second
-- row: the TACK-429 reconstruction wrote its whole history twice on the
-- testbed on 2026-08-16, 1788 events over 1788 identities.
--
-- This table is not partitioned, so it can hold the identity constraint the
-- ledger cannot. The consumer claims an identity in the same transaction as
-- the append, and a claim that loses reports the event as already projected.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back, so a retry must reconcile partial work.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS audit.projected_events (
    event_id     UUID        PRIMARY KEY,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE audit.projected_events OWNER TO audit_writer;
-- +goose StatementEnd

GRANT SELECT, INSERT ON audit.projected_events TO audit_writer;
GRANT SELECT ON audit.projected_events TO audit_reader;
REVOKE ALL ON audit.projected_events FROM PUBLIC;

ALTER TABLE audit.projected_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.projected_events FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
         WHERE schemaname = 'audit' AND tablename = 'projected_events'
           AND policyname = 'projected_events_writer_insert'
    ) THEN
        CREATE POLICY projected_events_writer_insert ON audit.projected_events
            FOR INSERT TO audit_writer WITH CHECK (true);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
         WHERE schemaname = 'audit' AND tablename = 'projected_events'
           AND policyname = 'projected_events_writer_select'
    ) THEN
        CREATE POLICY projected_events_writer_select ON audit.projected_events
            FOR SELECT TO audit_writer USING (true);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
         WHERE schemaname = 'audit' AND tablename = 'projected_events'
           AND policyname = 'projected_events_reader_select'
    ) THEN
        CREATE POLICY projected_events_reader_select ON audit.projected_events
            FOR SELECT TO audit_reader USING (true);
    END IF;
END$$;
-- +goose StatementEnd

-- Adopt the ledger the deployment already holds. Without this, the first
-- redelivery of any existing event would claim a free identity and fall
-- through to the ledger's own index instead, which is a weaker check.
-- +goose StatementBegin
INSERT INTO audit.projected_events (event_id)
SELECT DISTINCT event_id FROM audit.events
ON CONFLICT (event_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit.projected_events;
-- +goose StatementEnd
