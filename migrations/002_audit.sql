-- +goose Up
-- +goose StatementBegin

-- Database grants and row-level security protect the append-only audit ledger.
-- NOLOGIN roles separate event insertion, ledger reads, and selected personal-data
-- updates. This migration revokes PUBLIC privileges on the audit schema and
-- its tables. It grants audit_archiver SELECT on audit.events without assigning
-- the role to a login or granting partition deletion privileges.
--
-- audit.events partitions rows by week and hashes (org_id, shard) across tablets.
-- The writer updates audit.chain_heads atomically with each event insertion.
-- audit.notarizations stores Ed25519-signed Merkle roots over all 256 shard heads.
-- audit.pii stores personal data that audit_redactor can erase without changing
-- the event hash chain. event.pii_ref references that data by UUID.
--
-- Each event stores a shard SMALLINT 0..255 computed at write time as
-- crc32(actor_id || event_id) % 256. The HASH key on (org_id, shard) distributes
-- events from high-traffic actors and orgs across tablets.

CREATE SCHEMA audit;

-- Define audit.events.

CREATE TABLE audit.events (
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
) PARTITION BY RANGE (event_time);

-- This migration creates partitions through the next 8 weeks for immediate
-- writes after deployment.
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit.ensure_events_partition(week_start DATE)
RETURNS VOID AS $$
DECLARE
    week_end DATE := (week_start + 7)::DATE;
    part_name TEXT := 'events_' || to_char(week_start, 'YYYY_MM_DD');
BEGIN
    -- YugabyteDB selects the partition tablet count, typically one per node.
    -- Production can SPLIT partitions as load increases. The logical 256-shard
    -- layout does not depend on the physical tablet count.
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS audit.%I PARTITION OF audit.events ' ||
        'FOR VALUES FROM (%L) TO (%L)',
        part_name, week_start, week_end
    );
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    base DATE := date_trunc('week', now())::DATE;
    i INT;
BEGIN
    FOR i IN 0..8 LOOP
        PERFORM audit.ensure_events_partition((base + (i * 7))::DATE);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- Each weekly partition has local indexes; writes do not update other partitions.
-- INCLUDE columns support index-only reads without reading table rows.

CREATE INDEX events_actor_time
    ON audit.events ((org_id, actor_id) HASH, event_time DESC)
    INCLUDE (action, entity_kind, entity_id, event_id);

CREATE INDEX events_entity_time
    ON audit.events ((org_id, entity_kind, entity_id) HASH, event_time DESC)
    INCLUDE (action, actor_id, event_id);

CREATE INDEX events_action_time
    ON audit.events ((org_id, action) HASH, event_time DESC)
    INCLUDE (actor_id, entity_kind, entity_id, event_id);

CREATE INDEX events_seq_check
    ON audit.events (org_id, shard, seq);

-- +goose StatementBegin
CREATE TABLE audit.chain_heads (
    org_id     UUID         NOT NULL,
    shard      SMALLINT     NOT NULL,
    last_seq   BIGINT       NOT NULL,
    last_hash  BYTEA        NOT NULL,
    updated_at TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY ((org_id, shard) HASH)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE audit.notarizations (
    org_id       UUID         NOT NULL,
    notarized_at TIMESTAMPTZ  NOT NULL,
    merkle_root  BYTEA        NOT NULL,
    shard_heads  JSONB        NOT NULL,
    signature    BYTEA        NOT NULL,
    signing_key  TEXT         NOT NULL,
    PRIMARY KEY ((org_id) HASH, notarized_at ASC)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE audit.pii (
    pii_ref     UUID PRIMARY KEY,
    payload     BYTEA,
    redacted    BOOLEAN     NOT NULL DEFAULT false,
    redacted_at TIMESTAMPTZ
);
-- +goose StatementEnd

-- Group privileges in NOLOGIN roles. Application LOGIN roles inherit them.
-- An operator rotates login passwords after migration; this file does not
-- contain password literals.

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_writer') THEN
        CREATE ROLE audit_writer NOINHERIT NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_reader') THEN
        CREATE ROLE audit_reader NOINHERIT NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_redactor') THEN
        CREATE ROLE audit_redactor NOINHERIT NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_archiver') THEN
        CREATE ROLE audit_archiver NOINHERIT NOLOGIN;
    END IF;
END$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA audit TO audit_writer, audit_reader, audit_redactor, audit_archiver;

-- audit_writer inserts append-only rows and updates per-shard chain heads
-- atomically with events.
GRANT INSERT ON audit.events, audit.notarizations, audit.pii TO audit_writer;
GRANT SELECT, INSERT, UPDATE ON audit.chain_heads TO audit_writer;
GRANT SELECT ON audit.events TO audit_writer;

-- audit_reader can SELECT only.
GRANT SELECT ON audit.events, audit.notarizations, audit.chain_heads TO audit_reader;

-- audit_redactor can update only these three audit.pii columns, not events.
GRANT UPDATE (payload, redacted, redacted_at) ON audit.pii TO audit_redactor;
GRANT SELECT ON audit.pii TO audit_redactor;

-- Allow audit_archiver to read events for retention processing.
GRANT SELECT ON audit.events TO audit_archiver;

-- Revoke PUBLIC privileges on the audit schema and its tables.
REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA audit FROM PUBLIC;

-- Row-level security blocks UPDATE and DELETE except for the explicit policies
-- below. Superusers bypass it; deploy non-superuser application credentials.

ALTER TABLE audit.events          ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.notarizations   ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.chain_heads     ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.pii             ENABLE ROW LEVEL SECURITY;

ALTER TABLE audit.events          FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.notarizations   FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.chain_heads     FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.pii             FORCE ROW LEVEL SECURITY;

-- The writer inserts events; the reader and archiver select them.
CREATE POLICY events_writer_insert ON audit.events FOR INSERT TO audit_writer
    WITH CHECK (true);
CREATE POLICY events_reader_select ON audit.events FOR SELECT TO audit_reader
    USING (true);
CREATE POLICY events_writer_select ON audit.events FOR SELECT TO audit_writer
    USING (true);
CREATE POLICY events_archiver_select ON audit.events FOR SELECT TO audit_archiver
    USING (true);

-- The notarizer inserts notarizations as the writer; the reader selects them.
CREATE POLICY notarizations_writer_insert ON audit.notarizations FOR INSERT TO audit_writer
    WITH CHECK (true);
CREATE POLICY notarizations_reader_select ON audit.notarizations FOR SELECT TO audit_reader
    USING (true);

-- The writer reads and updates chain heads; the reader selects them.
CREATE POLICY chain_heads_writer_all ON audit.chain_heads FOR ALL TO audit_writer
    USING (true) WITH CHECK (true);
CREATE POLICY chain_heads_reader_select ON audit.chain_heads FOR SELECT TO audit_reader
    USING (true);

-- The writer inserts personal data; the redactor updates allowed columns;
-- the reader selects rows.
CREATE POLICY pii_writer_insert ON audit.pii FOR INSERT TO audit_writer
    WITH CHECK (true);
CREATE POLICY pii_writer_select ON audit.pii FOR SELECT TO audit_writer
    USING (true);
CREATE POLICY pii_redactor_update ON audit.pii FOR UPDATE TO audit_redactor
    USING (true) WITH CHECK (true);
CREATE POLICY pii_redactor_select ON audit.pii FOR SELECT TO audit_redactor
    USING (true);
CREATE POLICY pii_reader_select ON audit.pii FOR SELECT TO audit_reader
    USING (true);

-- +goose Down
-- +goose StatementBegin
DROP POLICY IF EXISTS events_writer_insert      ON audit.events;
DROP POLICY IF EXISTS events_reader_select      ON audit.events;
DROP POLICY IF EXISTS events_writer_select      ON audit.events;
DROP POLICY IF EXISTS events_archiver_select    ON audit.events;
DROP POLICY IF EXISTS notarizations_writer_insert ON audit.notarizations;
DROP POLICY IF EXISTS notarizations_reader_select ON audit.notarizations;
DROP POLICY IF EXISTS chain_heads_writer_all    ON audit.chain_heads;
DROP POLICY IF EXISTS chain_heads_reader_select ON audit.chain_heads;
DROP POLICY IF EXISTS pii_writer_insert         ON audit.pii;
DROP POLICY IF EXISTS pii_writer_select         ON audit.pii;
DROP POLICY IF EXISTS pii_redactor_update       ON audit.pii;
DROP POLICY IF EXISTS pii_redactor_select       ON audit.pii;
DROP POLICY IF EXISTS pii_reader_select         ON audit.pii;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS audit.ensure_events_partition(DATE);
DROP TABLE IF EXISTS audit.pii;
DROP TABLE IF EXISTS audit.notarizations;
DROP TABLE IF EXISTS audit.chain_heads;
DROP TABLE IF EXISTS audit.events CASCADE;
DROP SCHEMA IF EXISTS audit;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_writer')   THEN DROP ROLE audit_writer;   END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_reader')   THEN DROP ROLE audit_reader;   END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_redactor') THEN DROP ROLE audit_redactor; END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_archiver') THEN DROP ROLE audit_archiver; END IF;
END$$;
-- +goose StatementEnd
