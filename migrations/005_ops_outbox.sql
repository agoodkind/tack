-- The ops outbox is transactional, so an operator command's audit event
-- commits with the command's own work and the two can never disagree. A relay
-- inside the audit-consumer drains committed rows to Kafka, and the consumer
-- projects them into the ledger as the only writer of audit.events.
--
-- Privileges are split so an operator cannot erase their own audit record
-- before it ships. The table belongs to audit_writer, which is a NOLOGIN role
-- no command authenticates as. A command may only INSERT. The relay logs in as
-- tack_audit_writer, inherits audit_writer, and so may read and delete.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- transaction rolls back, so an interrupted migration can leave some of these
-- objects behind, and a retry must reconcile rather than fail.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.ops_outbox (
    event_id   UUID        PRIMARY KEY,
    event      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_outbox_created_at_idx
    ON public.ops_outbox (created_at);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE public.ops_outbox OWNER TO audit_writer;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    app_role TEXT := current_user;
BEGIN
    EXECUTE format('REVOKE ALL ON public.ops_outbox FROM %I', app_role);
    EXECUTE format('GRANT INSERT ON public.ops_outbox TO %I', app_role);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.ops_outbox_created_at_idx;
DROP TABLE IF EXISTS public.ops_outbox;
-- +goose StatementEnd
