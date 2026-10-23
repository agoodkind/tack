-- Allow audit_writer to claim ops_outbox rows with FOR UPDATE SKIP LOCKED.
--
-- YugabyteDB requires UPDATE privilege on at least one column for row locking.
-- The SELECT and DELETE grants from migration 008 do not permit the claim query.
-- Migration 024 grants UPDATE on created_at, which controls drain order, without
-- granting updates to event.
--
-- Up grants the current database user membership in ops_outbox_owner, grants
-- the column privilege, then revokes that membership. Down acquires the same
-- membership, revokes the column privilege, then revokes the membership.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    GRANT UPDATE (created_at) ON public.ops_outbox TO audit_writer;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    REVOKE UPDATE (created_at) ON public.ops_outbox FROM audit_writer;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd
