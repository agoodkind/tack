-- The audit-consumer projects each token's last use from auth.token_used
-- events (TACK-502). The application does not update tokens during requests.
-- Revoke its token UPDATE privilege. Grant the ledger writer UPDATE on
-- last_used and SELECT on id and last_used. The writer matches the id and
-- preserves the stored last_used when an older event arrives.
--
-- Every statement supports retries after a partial run.

-- +goose Up
REVOKE UPDATE ON public.api_tokens FROM app_auth;
GRANT USAGE ON SCHEMA public TO audit_writer;
GRANT SELECT (id, last_used), UPDATE (last_used) ON public.api_tokens TO audit_writer;

-- +goose Down
REVOKE SELECT (id, last_used), UPDATE (last_used) ON public.api_tokens FROM audit_writer;
GRANT UPDATE ON public.api_tokens TO app_auth;
