-- Give ops_outbox_owner ownership of the operator outbox.
-- Table owners can insert rows and drop tables regardless of grants.
-- The relay inherits audit_writer; it must not own the table.
-- ops_outbox_owner cannot log in and does not grant membership to any role.
-- audit_operator permits operator commands to INSERT only.
-- audit_writer permits the relay to SELECT and DELETE only.
--
-- Use a new migration because goose does not rerun applied versions.
-- Every statement supports retries because YugabyteDB keeps completed DDL
-- when a transaction rolls back.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ops_outbox_owner') THEN
        CREATE ROLE ops_outbox_owner NOINHERIT NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE public.ops_outbox OWNER TO ops_outbox_owner;
-- +goose StatementEnd

-- Grant the relay SELECT and DELETE access to public.ops_outbox.
-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON public.ops_outbox FROM audit_writer;
    GRANT SELECT, DELETE ON public.ops_outbox TO audit_writer;
END
$$;
-- +goose StatementEnd

-- Revoke the current migration role's explicit grants on public.ops_outbox.
-- This does not remove superuser privileges. Migration 018 later grants
-- tack_migrator SELECT for schema dumps. Operator commands record events
-- using audit_operator.
-- +goose StatementBegin
DO $$
DECLARE
    app_role TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE ALL ON public.ops_outbox FROM %I', app_role);
END
$$;
-- +goose StatementEnd

-- now() is stable within a transaction. Order rows by created_at and event_id
-- to resolve ties between rows from one transaction.
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_outbox_drain_idx
    ON public.ops_outbox (created_at, event_id);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS public.ops_outbox_created_at_idx;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_outbox_created_at_idx
    ON public.ops_outbox (created_at);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS public.ops_outbox_drain_idx;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE public.ops_outbox OWNER TO audit_writer;
-- +goose StatementEnd
