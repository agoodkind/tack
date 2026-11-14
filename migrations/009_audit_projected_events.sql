-- audit.events partitions rows by event_time and includes event_time in its
-- unique index. That index cannot reject the same event id with a new time.
-- audit.projected_events enforces uniqueness across all event times without
-- partitioning. The consumer claims an event id in the append transaction
-- and reports an existing claim as already projected.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back. A retry must reconcile partial work.

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

-- Register existing ledger event ids before processing redeliveries.
-- Otherwise an existing event with a new time could claim an unused id
-- and pass the ledger's time-dependent uniqueness check.
-- +goose StatementBegin
INSERT INTO audit.projected_events (event_id)
SELECT DISTINCT event_id FROM audit.events
ON CONFLICT (event_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit.projected_events;
-- +goose StatementEnd
