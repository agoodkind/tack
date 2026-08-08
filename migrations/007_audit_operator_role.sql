-- The operator outbox role is separate from audit_writer, which owns and
-- relays public.ops_outbox. Operator commands authenticate as a member of
-- this role, so grants prevent them from changing or deleting pending events.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- transaction rolls back, so a retry must reconcile partial work.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_operator') THEN
        CREATE ROLE audit_operator NOINHERIT NOLOGIN;
    END IF;
END$$;
-- +goose StatementEnd

REVOKE ALL ON TABLE public.ops_outbox FROM audit_operator;
GRANT INSERT ON TABLE public.ops_outbox TO audit_operator;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_operator') THEN
        REVOKE ALL ON TABLE public.ops_outbox FROM audit_operator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_audit_operator')
        AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_operator') THEN
        REVOKE audit_operator FROM tack_audit_operator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_operator') THEN
        DROP ROLE audit_operator;
    END IF;
END$$;
-- +goose StatementEnd
