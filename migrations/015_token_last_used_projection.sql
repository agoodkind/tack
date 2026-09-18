-- The token's last use is projected by the audit-consumer from the
-- auth.token_used event that recorded it (TACK-502). The application no
-- longer writes the token table on the request path, so its role loses
-- UPDATE there. The ledger writer gains the one column it sets and the two
-- columns that update reads: the id it matches on and the stored value it
-- keeps when the event is older.
--
-- Every statement is idempotent, so a retry after a partial run reconciles.

-- +goose Up
REVOKE UPDATE ON public.api_tokens FROM app_auth;
GRANT USAGE ON SCHEMA public TO audit_writer;
GRANT SELECT (id, last_used), UPDATE (last_used) ON public.api_tokens TO audit_writer;

-- +goose Down
REVOKE SELECT (id, last_used), UPDATE (last_used) ON public.api_tokens FROM audit_writer;
GRANT UPDATE ON public.api_tokens TO app_auth;
