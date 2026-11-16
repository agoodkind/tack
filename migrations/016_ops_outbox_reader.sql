-- `ops audit consumer-offsets` reports how many operator-command events wait
-- in public.ops_outbox and reports the oldest event's creation time. It uses
-- the ledger reader, which does not have the relay's writer credential.
-- The reader gains SELECT on the two columns needed for count and age.
-- It cannot read event payloads.
--
-- Every statement is idempotent. A retry after a partial run applies the same
-- grant again.

-- +goose Up
GRANT SELECT (event_id, created_at) ON public.ops_outbox TO audit_reader;

-- +goose Down
REVOKE SELECT (event_id, created_at) ON public.ops_outbox FROM audit_reader;
