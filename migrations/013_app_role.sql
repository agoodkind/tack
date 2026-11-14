-- The application connects as its own non-superuser role (TACK-180).
--
-- A yugabyte superuser connection bypasses ledger grants and policies.
-- This migration creates the NOLOGIN base role app_auth. The application's
-- LOGIN role tack_app inherits app_auth; `ops audit seed-roles` creates it.
-- app_auth can read and write only users, api_tokens, and org_members.
-- It cannot access the audit schema, operator outbox, or migration ledger.
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

-- The engine grants CREATE on public to PUBLIC by default. USAGE does not
-- revoke CREATE. Without this revocation the application login could create
-- arbitrary objects beside the auth tables.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO app_auth;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.users, public.api_tokens, public.org_members TO app_auth;

-- app_auth cannot access the ledger. Only roles with explicit audit schema
-- grants can access it.
REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON SCHEMA audit FROM app_auth;

-- +goose Down
REVOKE ALL ON public.users, public.api_tokens, public.org_members FROM app_auth;
REVOKE USAGE ON SCHEMA public FROM app_auth;
GRANT CREATE ON SCHEMA public TO PUBLIC;
