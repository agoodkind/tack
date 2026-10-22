-- Migration 020 adds outbox part storage, consumer assembly state,
-- permanent read manifest parts, and completed transport digests.
-- audit_writer manages assembly state and reads and deletes outbox parts.
-- It inserts and reads permanent parts and digests; audit_reader reads permanent parts.
--
-- public.ops_outbox_write_encoded inserts parts before the outbox row.
-- The SECURITY DEFINER function uses ops_outbox_owner privileges
-- and the search path pg_catalog, public.
-- app_auth, tack_migrator, and audit_operator may execute the function
-- but cannot insert directly into public.ops_outbox.
-- The migration grants tack_migrator membership in ops_outbox_owner and grants
-- ops_outbox_owner CREATE on public, changes the function owner, then revokes both grants.
--
-- Every statement supports repeated execution after YugabyteDB retains completed
-- schema changes when a migration transaction rolls back.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.ops_outbox_parts (
    event_id   UUID   NOT NULL,
    part_index BIGINT NOT NULL,
    message    BYTEA  NOT NULL,
    PRIMARY KEY (event_id HASH, part_index ASC)
);
-- +goose StatementEnd

REVOKE ALL ON public.ops_outbox_parts FROM PUBLIC;
GRANT SELECT, DELETE ON public.ops_outbox_parts TO audit_writer;
GRANT INSERT ON public.ops_outbox_parts TO ops_outbox_owner;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_outbox_write_encoded(p_event_id UUID, p_header JSONB, p_parts BYTEA[])
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    part_position INTEGER;
BEGIN
    FOR part_position IN 1 .. coalesce(array_length(p_parts, 1), 0) LOOP
        INSERT INTO public.ops_outbox_parts (event_id, part_index, message)
        VALUES (p_event_id, part_position - 1, p_parts[part_position]);
    END LOOP;
    INSERT INTO public.ops_outbox (event_id, event) VALUES (p_event_id, p_header);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) TO app_auth, tack_migrator, audit_operator;
GRANT ops_outbox_owner TO tack_migrator;
GRANT CREATE ON SCHEMA public TO ops_outbox_owner;
ALTER FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) OWNER TO ops_outbox_owner;
REVOKE CREATE ON SCHEMA public FROM ops_outbox_owner;
REVOKE ops_outbox_owner FROM tack_migrator;

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS audit.event_assemblies (
    event_id         UUID        PRIMARY KEY,
    org_id           UUID        NOT NULL,
    shard            SMALLINT    NOT NULL,
    part_count       BIGINT      NOT NULL,
    transport_digest TEXT        NOT NULL,
    header           BYTEA,
    commit_version   TEXT        NOT NULL DEFAULT '',
    ready            BOOLEAN     NOT NULL DEFAULT false,
    invalid_reason   TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS event_assemblies_ready_idx
    ON audit.event_assemblies (created_at ASC, event_id ASC)
    WHERE ready AND invalid_reason IS NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS event_assemblies_waiting_idx
    ON audit.event_assemblies (created_at ASC, event_id ASC)
    WHERE NOT ready;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS audit.event_assembly_parts (
    event_id   UUID   NOT NULL,
    part_index BIGINT NOT NULL,
    digest     TEXT   NOT NULL,
    payload    BYTEA  NOT NULL,
    PRIMARY KEY (event_id HASH, part_index ASC)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS audit.event_parts (
    org_id     UUID   NOT NULL,
    event_id   UUID   NOT NULL,
    part_index BIGINT NOT NULL,
    digest     TEXT   NOT NULL,
    payload    BYTEA  NOT NULL,
    PRIMARY KEY ((org_id, event_id) HASH, part_index ASC)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS audit.completed_transports (
    event_id         UUID        PRIMARY KEY,
    transport_digest TEXT        NOT NULL,
    completed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

REVOKE ALL ON audit.event_assemblies, audit.event_assembly_parts, audit.event_parts, audit.completed_transports FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON audit.event_assemblies, audit.event_assembly_parts TO audit_writer;
GRANT SELECT, INSERT ON audit.event_parts, audit.completed_transports TO audit_writer;
GRANT SELECT ON audit.event_parts TO audit_reader;

ALTER TABLE audit.event_assemblies ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.event_assemblies FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.event_assembly_parts ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.event_assembly_parts FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.event_parts ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.event_parts FORCE ROW LEVEL SECURITY;
ALTER TABLE audit.completed_transports ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.completed_transports FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS event_assemblies_writer_all ON audit.event_assemblies;
CREATE POLICY event_assemblies_writer_all ON audit.event_assemblies
    FOR ALL TO audit_writer USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS event_assembly_parts_writer_all ON audit.event_assembly_parts;
CREATE POLICY event_assembly_parts_writer_all ON audit.event_assembly_parts
    FOR ALL TO audit_writer USING (true) WITH CHECK (true);

DROP POLICY IF EXISTS event_parts_writer_insert ON audit.event_parts;
CREATE POLICY event_parts_writer_insert ON audit.event_parts
    FOR INSERT TO audit_writer WITH CHECK (true);
DROP POLICY IF EXISTS event_parts_writer_select ON audit.event_parts;
CREATE POLICY event_parts_writer_select ON audit.event_parts
    FOR SELECT TO audit_writer USING (true);
DROP POLICY IF EXISTS event_parts_reader_select ON audit.event_parts;
CREATE POLICY event_parts_reader_select ON audit.event_parts
    FOR SELECT TO audit_reader USING (true);

DROP POLICY IF EXISTS completed_transports_writer_insert ON audit.completed_transports;
CREATE POLICY completed_transports_writer_insert ON audit.completed_transports
    FOR INSERT TO audit_writer WITH CHECK (true);
DROP POLICY IF EXISTS completed_transports_writer_select ON audit.completed_transports;
CREATE POLICY completed_transports_writer_select ON audit.completed_transports
    FOR SELECT TO audit_writer USING (true);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_migrator') THEN
        DROP POLICY IF EXISTS event_parts_migrator_select ON audit.event_parts;
        CREATE POLICY event_parts_migrator_select ON audit.event_parts
            FOR SELECT TO tack_migrator USING (true);
        DROP POLICY IF EXISTS completed_transports_migrator_select ON audit.completed_transports;
        CREATE POLICY completed_transports_migrator_select ON audit.completed_transports
            FOR SELECT TO tack_migrator USING (true);
    END IF;
END$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS audit.completed_transports;
DROP TABLE IF EXISTS audit.event_parts;
DROP TABLE IF EXISTS audit.event_assembly_parts;
DROP TABLE IF EXISTS audit.event_assemblies;
DROP FUNCTION IF EXISTS public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]);
DROP TABLE IF EXISTS public.ops_outbox_parts;
