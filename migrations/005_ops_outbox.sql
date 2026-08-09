-- The ops outbox is transactional, so an operator command's audit event
-- commits with the command's own work and the two can never disagree. A relay
-- inside the audit-consumer drains committed rows to Kafka, and the consumer
-- projects them into the ledger as the only writer of audit.events.
--
-- Privileges are split so nobody who touches this table can rewrite history
-- with it. Three roles, none of which is the other:
--
--   ops_outbox_owner  owns the table. NOLOGIN, and granted to nobody, so no
--                     session can reach owner powers. A table owner may write
--                     and drop regardless of grants, which is exactly why the
--                     owner cannot be a role anything inherits.
--   audit_operator    INSERT only. Operator commands authenticate as a login
--                     role that inherits this, so a command can record and
--                     cannot erase its own pending event.
--   audit_writer      SELECT and DELETE only. The relay inherits this, so it
--                     can drain the table and cannot forge a row into it.
--
-- An earlier revision made audit_writer the owner. That handed the relay
-- owner powers through inheritance, so the relay could have inserted forged
-- events and dropped the table. Ownership now sits with a role nothing can
-- assume.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- transaction rolls back, so an interrupted migration can leave some of these
-- objects behind, and a retry must reconcile rather than fail.

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
CREATE TABLE IF NOT EXISTS public.ops_outbox (
    event_id   UUID        PRIMARY KEY,
    event      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- The relay drains in creation order, and now() is stable within a
-- transaction, so created_at alone is not a total order. The event id breaks
-- the tie and makes the drain order deterministic.
-- +goose StatementBegin
DROP INDEX IF EXISTS public.ops_outbox_created_at_idx;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_outbox_drain_idx
    ON public.ops_outbox (created_at, event_id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE public.ops_outbox OWNER TO ops_outbox_owner;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON public.ops_outbox FROM audit_writer;
    GRANT SELECT, DELETE ON public.ops_outbox TO audit_writer;
END
$$;
-- +goose StatementEnd

-- The migration runner is not the owner any more, so it keeps no standing
-- rights on the table. It writes through audit_operator like any other
-- command, which migration 007 grants.
-- +goose StatementBegin
DO $$
DECLARE
    app_role TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE ALL ON public.ops_outbox FROM %I', app_role);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.ops_outbox_drain_idx;
DROP TABLE IF EXISTS public.ops_outbox;
-- +goose StatementEnd
