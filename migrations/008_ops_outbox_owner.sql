-- Move ownership of the ops outbox off audit_writer.
--
-- Migration 005 made audit_writer the table owner. The relay inherits that
-- role, and a table owner may write and drop regardless of grants, so the
-- read-and-delete grant that looked like the protection was decorative: the
-- relay could have inserted audit events no operator ever performed, into the
-- table that feeds the compliance ledger, and could have dropped the table.
--
-- This is a new migration rather than an edit to 005 because goose records
-- applied versions and never re-runs one. Editing 005 would have fixed only a
-- database created after the edit and left every existing deployment, testbed
-- and production alike, on the old ownership while the code claimed otherwise.
--
-- After this runs, three roles touch the table and none is the other:
--
--   ops_outbox_owner  owns it. NOLOGIN, granted to nobody, so owner powers
--                     are unreachable from any session.
--   audit_operator    INSERT only, held by operator commands.
--   audit_writer      SELECT and DELETE only, held by the relay.
--
-- Every statement is idempotent, because YugabyteDB keeps completed DDL when
-- a transaction rolls back and a retry must reconcile rather than fail.

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

-- The relay keeps exactly what it needs to drain the table, and nothing that
-- would let it put a row in.
-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON public.ops_outbox FROM audit_writer;
    GRANT SELECT, DELETE ON public.ops_outbox TO audit_writer;
END
$$;
-- +goose StatementEnd

-- The migration runner is no longer the owner, so it holds no standing rights
-- here. It writes through audit_operator like any other command.
-- +goose StatementBegin
DO $$
DECLARE
    app_role TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE ALL ON public.ops_outbox FROM %I', app_role);
END
$$;
-- +goose StatementEnd

-- The relay drains in creation order, and now() is stable within a
-- transaction, so created_at alone is not a total order and two rows can tie.
-- The event id breaks the tie and makes the drain order deterministic.
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
