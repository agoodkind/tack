-- An operator command commits its audit event and database changes in one
-- transaction. The audit-consumer relay publishes committed outbox events to
-- Kafka. The consumer writes those events to audit.events as its only writer.
--
-- Operator commands can INSERT but cannot change or delete pending events.
-- The NOLOGIN audit_writer role owns the table. The relay logs in as
-- tack_audit_writer and inherits audit_writer's read and delete permissions.
--
-- Every statement supports retries. YugabyteDB keeps completed DDL when a
-- transaction rolls back. A retry must reconcile any existing objects.

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
