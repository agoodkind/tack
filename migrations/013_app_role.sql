-- The application connects as its own non-superuser role (TACK-180).
--
-- Until now DATABASE_URL was the yugabyte superuser, which owns every table
-- and so bypasses every grant and policy the audit migrations fenced the
-- ledger with. This creates app_auth, the NOLOGIN base role the application's
-- LOGIN role (tack_app, created by `ops audit seed-roles`) inherits, and
-- grants it exactly the three auth tables the application reads and writes:
-- users, api_tokens, org_members. It holds nothing on the audit schema, on
-- the operator outbox, or on the migration ledger. Migrations, seed-roles,
-- and the backup commands keep the superuser through the ops sidecar.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back, so a retry must reconcile partial work.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_auth') THEN
        CREATE ROLE app_auth NOINHERIT NOLOGIN;
    END IF;
END$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO app_auth;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.users, public.api_tokens, public.org_members TO app_auth;

-- Nothing the application holds reaches the ledger: the schema is closed to
-- everyone the audit migrations did not name, and app_auth is not named.
REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON SCHEMA audit FROM app_auth;

-- +goose Down
REVOKE ALL ON public.users, public.api_tokens, public.org_members FROM app_auth;
REVOKE USAGE ON SCHEMA public FROM app_auth;
