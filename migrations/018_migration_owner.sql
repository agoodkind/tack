-- tack_migrator owns the audit, partman, and public schemas (TACK-554).
-- CREATEROLE is for `ops audit seed-roles`, which also sets LOGIN and the
-- password. The audit tables force row-level security on their owner: the
-- policies below grant SELECT only.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_migrator') THEN
        CREATE ROLE tack_migrator NOLOGIN NOSUPERUSER NOCREATEDB CREATEROLE INHERIT;
    END IF;
    EXECUTE 'GRANT CREATE ON DATABASE ' || quote_ident(current_database()) || ' TO tack_migrator';
END$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    owned RECORD;
BEGIN
    FOR owned IN
        SELECT nspname FROM pg_namespace WHERE nspname IN ('audit', 'partman', 'public')
    LOOP
        EXECUTE 'ALTER SCHEMA ' || quote_ident(owned.nspname) || ' OWNER TO tack_migrator';
    END LOOP;

    FOR owned IN
        SELECT n.nspname, c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname IN ('audit', 'partman', 'public')
           AND c.relkind IN ('r', 'p', 'v')
    LOOP
        EXECUTE 'ALTER TABLE ' || quote_ident(owned.nspname) || '.' || quote_ident(owned.relname)
            || ' OWNER TO tack_migrator';
    END LOOP;

    /* A sequence that a table column owns changes owner with its table. */
    FOR owned IN
        SELECT n.nspname, c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname IN ('audit', 'partman', 'public')
           AND c.relkind = 'S'
           AND NOT EXISTS (
               SELECT 1 FROM pg_depend d
                WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
                  AND d.deptype IN ('a', 'i'))
    LOOP
        EXECUTE 'ALTER SEQUENCE ' || quote_ident(owned.nspname) || '.' || quote_ident(owned.relname)
            || ' OWNER TO tack_migrator';
    END LOOP;

    FOR owned IN
        SELECT p.oid::regprocedure::text AS signature
          FROM pg_proc p
          JOIN pg_namespace n ON n.oid = p.pronamespace
         WHERE n.nspname IN ('audit', 'partman', 'public')
           AND p.prokind IN ('f', 'p')
    LOOP
        EXECUTE 'ALTER ROUTINE ' || owned.signature || ' OWNER TO tack_migrator';
    END LOOP;
END$$;
-- +goose StatementEnd

DROP POLICY IF EXISTS events_migrator_select ON audit.events;
CREATE POLICY events_migrator_select ON audit.events FOR SELECT TO tack_migrator USING (true);
DROP POLICY IF EXISTS notarizations_migrator_select ON audit.notarizations;
CREATE POLICY notarizations_migrator_select ON audit.notarizations FOR SELECT TO tack_migrator USING (true);
DROP POLICY IF EXISTS chain_heads_migrator_select ON audit.chain_heads;
CREATE POLICY chain_heads_migrator_select ON audit.chain_heads FOR SELECT TO tack_migrator USING (true);
DROP POLICY IF EXISTS pii_migrator_select ON audit.pii;
CREATE POLICY pii_migrator_select ON audit.pii FOR SELECT TO tack_migrator USING (true);

-- +goose Down
DROP POLICY IF EXISTS events_migrator_select ON audit.events;
DROP POLICY IF EXISTS notarizations_migrator_select ON audit.notarizations;
DROP POLICY IF EXISTS chain_heads_migrator_select ON audit.chain_heads;
DROP POLICY IF EXISTS pii_migrator_select ON audit.pii;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_migrator') THEN
        RETURN;
    END IF;
    /* The session role takes every object that tack_migrator owns. */
    EXECUTE 'REASSIGN OWNED BY tack_migrator TO ' || quote_ident(session_user);
    EXECUTE 'REVOKE CREATE ON DATABASE ' || quote_ident(current_database()) || ' FROM tack_migrator';
    DROP ROLE tack_migrator;
END$$;
-- +goose StatementEnd
