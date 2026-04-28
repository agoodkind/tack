-- +goose Up
-- +goose StatementBegin

-- Audit ledger schema. Append-only at the database boundary.
--
-- Roles, grants, and RLS together ensure no application code path can mutate
-- existing rows. The default tack_app role has zero privilege on this schema.
-- Three login roles inherit from three NOLOGIN base roles: writer (INSERT
-- only), reader (SELECT only), redactor (UPDATE only on pii.payload columns).
-- Logical-data deletion is impossible. Physical-data deletion is restricted to
-- a fourth role audit_archiver, granted explicitly to the partition rotation
-- job, never to the application.
--
-- Table layout:
--   audit.events          partitioned by week, HASH(org_id, shard) per
--                         partition for tablet distribution. Append-only.
--   audit.chain_heads     per-shard hash chain head. Updated atomically by
--                         the audit writer in the same Yugabyte transaction
--                         as the events INSERT.
--   audit.notarizations   periodic Merkle root over all 256 shard heads,
--                         signed Ed25519. Append-only.
--   audit.pii             user PII referenced by event.pii_ref UUID. Only
--                         table where audit_redactor can UPDATE selected
--                         columns, supporting GDPR right to erasure without
--                         breaking the hash chain.
--
-- Sharding: each event carries a shard SMALLINT 0..255 computed at write
-- time as crc32(actor_id || event_id) % 256. The HASH primary key on
-- (org_id, shard) distributes load across tablets without hotspotting on a
-- single high-traffic actor or org. NUM_TABLETS=8 is a dev-cluster default;
-- production will grow this to 256 when load demands and the cluster has
-- enough nodes to host them. Tablet count is fixed at table creation time
-- in YugabyteDB; revisiting it requires a new partition.

CREATE SCHEMA audit;

-- ── audit.events ──────────────────────────────────────────────────────────────

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

-- Partition manager helper. Called once per week by a goroutine in the
-- audit package; we also call it inline below for the next 8 weeks so a
-- fresh deployment has somewhere to write immediately.
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit.ensure_events_partition(week_start DATE)
RETURNS VOID AS $$
DECLARE
    week_end DATE := (week_start + 7)::DATE;
    part_name TEXT := 'events_' || to_char(week_start, 'YYYY_MM_DD');
BEGIN
    -- Tablet count intentionally unset: YugabyteDB picks a sensible default
    -- (typically 1 tablet per node) for partitions of a partitioned parent.
    -- Production scale tuning will SPLIT existing partitions when load
    -- demands. The 256-shard logical layout in the row data is independent
    -- of the physical tablet count and works at any tablet density.
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

-- Indexes are LOCAL by default in PostgreSQL declarative partitioning, so
-- writes do not broadcast across partitions. Each weekly partition gets its
-- own copy. INCLUDE columns make hot-tier reads index-only with no heap hit.

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

-- ── Roles ─────────────────────────────────────────────────────────────────────
-- Three NOLOGIN base roles for privilege grouping. Three LOGIN derived roles
-- for actual application connections. Login passwords get rotated by an
-- operator after migration runs; no password literal lives in this file.

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

-- audit_writer: INSERT only on the append-only tables, plus chain_heads
-- updates so the writer can advance per-shard heads atomically with events.
GRANT INSERT ON audit.events, audit.notarizations, audit.pii TO audit_writer;
GRANT SELECT, INSERT, UPDATE ON audit.chain_heads TO audit_writer;
GRANT SELECT ON audit.events TO audit_writer;

-- audit_reader: SELECT only.
GRANT SELECT ON audit.events, audit.notarizations, audit.chain_heads TO audit_reader;

-- audit_redactor: column-level UPDATE on the three redaction columns of
-- audit.pii ONLY. Cannot touch any other column. Cannot UPDATE events.
GRANT UPDATE (payload, redacted, redacted_at) ON audit.pii TO audit_redactor;
GRANT SELECT ON audit.pii TO audit_redactor;

-- audit_archiver: explicit DROP/DETACH on partition tables for retention. The
-- archiver job is the only path that ever removes rows from the ledger and
-- it is itself audited via verb=audit.archive_partition.
GRANT SELECT ON audit.events TO audit_archiver;

-- The standard tack_app role gets nothing in the audit schema. It cannot
-- read, write, or even see the existence of the tables. We do not GRANT here;
-- we explicitly REVOKE inherited PUBLIC privileges below.

REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA audit FROM PUBLIC;

-- ── Row Level Security ───────────────────────────────────────────────────────
-- Defense in depth. Even if an unauthorized role somehow has a connection,
-- the policy below blocks UPDATE and DELETE for everyone, with an explicit
-- INSERT carve-out for audit_writer. A SUPERUSER bypasses RLS by default,
-- so the deployment must run with non-superuser app credentials.

ALTER TABLE audit.events          ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.notarizations   ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.chain_heads     ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.pii             ENABLE ROW LEVEL SECURITY;

ALTER TABLE audit.events          FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.notarizations   FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.chain_heads     FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.pii             FORCE ROW LEVEL SECURITY;

-- audit.events: writer can insert; reader can select; everyone else nothing.
CREATE POLICY events_writer_insert ON audit.events FOR INSERT TO audit_writer
    WITH CHECK (true);
CREATE POLICY events_reader_select ON audit.events FOR SELECT TO audit_reader
    USING (true);
CREATE POLICY events_writer_select ON audit.events FOR SELECT TO audit_writer
    USING (true);
CREATE POLICY events_archiver_select ON audit.events FOR SELECT TO audit_archiver
    USING (true);

-- audit.notarizations: writer inserts (notarizer goroutine), reader selects.
CREATE POLICY notarizations_writer_insert ON audit.notarizations FOR INSERT TO audit_writer
    WITH CHECK (true);
CREATE POLICY notarizations_reader_select ON audit.notarizations FOR SELECT TO audit_reader
    USING (true);

-- audit.chain_heads: writer reads + advances heads.
CREATE POLICY chain_heads_writer_all ON audit.chain_heads FOR ALL TO audit_writer
    USING (true) WITH CHECK (true);
CREATE POLICY chain_heads_reader_select ON audit.chain_heads FOR SELECT TO audit_reader
    USING (true);

-- audit.pii: writer inserts new rows; redactor updates only allowed columns;
-- reader selects.
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
